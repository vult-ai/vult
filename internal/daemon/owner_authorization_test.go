package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerAuthorizationIssuesOnlyExactPeerAndPayloadGrant(t *testing.T) {
	peer := local.Peer{UID: 1000, PID: 1234}
	payload := json.RawMessage(`{"book_id":"11111111-1111-4111-8111-111111111111"}`)
	request := local.OwnerRequest{Action: "export", Payload: payload, BusName: ":1.25"}
	for _, variant := range []string{"valid", "rejected", "late_cancel", "disabled", "missing_pid", "unknown_claim"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &Server{AuthorizeOwner: func(ctx context.Context, got local.Peer, q local.OwnerRequest) error {
				if got != peer || q.Action != request.Action || q.BusName != request.BusName || string(q.Payload) != string(payload) {
					t.Fatal("owner authorization changed the socket identity or exact request")
				}
				if variant == "rejected" {
					return errors.New("example provider detail must not escape")
				}
				if variant == "late_cancel" {
					cancel()
				}
				return nil
			}}
			input := marshal(request)
			gotPeer := peer
			switch variant {
			case "disabled":
				s.AuthorizeOwner = nil
			case "missing_pid":
				gotPeer.PID = 0
			case "unknown_claim":
				input = json.RawMessage(`{"action":"export","payload":{},"bus_name":":1.25","uid":0}`)
			}
			out, err := s.ownerAuthorization(ctx, gotPeer, input)
			if variant != "valid" {
				if err == nil || len(out) != 0 || len(s.grants) != 0 || len(s.ownerPrompts) != 0 {
					t.Fatal("denied authorization retained authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var reply struct{ Token string }
			if json.Unmarshal(out, &reply) != nil || reply.Token == "" {
				t.Fatal("grant missing")
			}
			grant := s.grants[tokenKey(reply.Token)]
			if grant.UID != peer.UID || grant.Action != request.Action || grant.Digest != local.Digest(payload) {
				t.Fatal("grant changed the exact owner request")
			}
			q := local.Request{Method: request.Action, Authorization: reply.Token, Payload: payload}
			if !s.consume(peer.UID, q) || s.consume(peer.UID, q) {
				t.Fatal("grant was not usable exactly once")
			}
		})
	}
}

func TestOwnerAuthorizationBoundsConcurrentPrompts(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finishPrompt := func() { once.Do(func() { close(finish) }) }
	defer finishPrompt()
	s := &Server{AuthorizeOwner: func(context.Context, local.Peer, local.OwnerRequest) error {
		close(started)
		<-finish
		return ledger.ErrDenied
	}}
	request := marshal(local.OwnerRequest{Action: "book_list", Payload: json.RawMessage(`{}`), BusName: ":1.25"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ownerAuthorization(context.Background(), local.Peer{UID: 1000, PID: 1234}, request)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	if _, err := s.ownerAuthorization(context.Background(), local.Peer{UID: 1000, PID: 1234}, request); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("same user opened a second prompt")
	}
	s.mu.Lock()
	s.ownerPrompts[1001], s.ownerPrompts[1002], s.ownerPrompts[1003] = true, true, true
	s.mu.Unlock()
	if _, err := s.ownerAuthorization(context.Background(), local.Peer{UID: 1004, PID: 1235}, request); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("global prompt limit ignored")
	}
	finishPrompt()
	<-done
	if s.ownerPrompts[1000] || len(s.grants) != 0 {
		t.Fatal("failed prompt retained authority or its user slot")
	}
}

func TestOwnerAuthorizationDisconnectAndExtraBytesCancelWithoutGrant(t *testing.T) {
	for _, variant := range []string{"disconnect", "extra_bytes"} {
		t.Run(variant, func(t *testing.T) {
			started := make(chan struct{})
			s := &Server{AuthorizeOwner: func(ctx context.Context, _ local.Peer, _ local.OwnerRequest) error {
				close(started)
				<-ctx.Done()
				return nil // A late positive result must never become a grant.
			}}
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			payload := marshal(local.OwnerRequest{Action: "book_list", Payload: json.RawMessage(`{}`), BusName: ":1.25"})
			header := local.Header{Method: "owner_authorization", Size: len(payload)}
			if !s.admit(1000, header) {
				t.Fatal("valid owner request not admitted")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				s.servePayload(ctx, local.Peer{UID: 1000, PID: 1234}, header, server, server)
			}()
			if _, err := client.Write(payload); err != nil {
				t.Fatal(err)
			}
			<-started
			if variant == "disconnect" {
				client.Close()
			} else {
				if _, err := client.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				line, err := bufio.NewReader(client).ReadBytes('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					t.Fatal(err)
				}
				if err == nil {
					var response local.Response
					if json.Unmarshal(line, &response) != nil || response.Error != ledger.ErrDenied.Error() || response.Result != nil {
						t.Fatal("cancelled connection returned authority")
					}
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("disconnected request retained a prompt worker")
			}
			if len(s.grants) != 0 || len(s.ownerPrompts) != 0 {
				t.Fatal("cancelled request retained owner authority")
			}
		})
	}
}

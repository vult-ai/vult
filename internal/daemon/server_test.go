package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/ledger"
)

func TestProposalErrorsExposeOnlyCanonicalMessages(t *testing.T) {
	refused := &ledger.RefusedError{Reason: "Closing with open review items needs a reason."}
	if got := safeError(refused); got != refused.Error() {
		t.Fatal("native refusal lost its explanation", got)
	}
	for _, known := range []error{ledger.ErrApprovalRequired, ledger.ErrProposalStale, ledger.ErrProposalExpired} {
		if got := safeError(fmt.Errorf("example private context: %w", known)); got != known.Error() {
			t.Fatal("proposal error lost its safe next step or leaked context", got)
		}
	}
	if got := safeError(errors.New("example private context")); got != "operation failed; no automatic retry" {
		t.Fatal("unknown error leaked its context")
	}
}

func TestOwnerAuthorizationBindsActionPayloadAndPeer(t *testing.T) {
	for _, variation := range []string{"valid", "peer", "action", "payload", "expired"} {
		t.Run(variation, func(t *testing.T) {
			s := &Server{}
			payload := marshal(struct {
				Book string `json:"book"`
			}{"example"})
			a := local.Authorization{Action: "export", Digest: local.Digest(json.RawMessage(payload)), UID: 501}
			if _, e := s.authorize(501, marshal(a)); !errors.Is(e, ledger.ErrDenied) {
				t.Fatal("non-root authorization succeeded")
			}
			out, e := s.authorize(0, marshal(a))
			if e != nil {
				t.Fatal(e)
			}
			var token struct {
				Token string `json:"token"`
			}
			json.Unmarshal(out, &token)
			q := local.Request{Method: "export", Authorization: token.Token, Payload: payload}
			uid := uint32(501)
			switch variation {
			case "peer":
				uid++
			case "action":
				q.Method = "backup"
			case "payload":
				q.Payload = marshal(struct{}{})
			case "expired":
				g := s.grants[tokenKey(token.Token)]
				g.Expires = time.Now().Add(-time.Second)
				s.grants[tokenKey(token.Token)] = g
			}
			if s.consume(uid, q) != (variation == "valid") {
				t.Fatal("incorrect authorization verdict")
			}
			if s.consume(501, local.Request{Method: "export", Authorization: token.Token, Payload: payload}) {
				t.Fatal("authorization was replayed")
			}
		})
	}
}

func TestSessionsArePeerBoundAndBodyAdmissionIsBounded(t *testing.T) {
	s := &Server{sessions: map[string]session{tokenKey("example"): {UID: 501, Expires: time.Now().Add(time.Hour), Scope: ledger.Scope{BookID: ledger.ID()}}}}
	if !s.admit(501, local.Header{Method: "get_book", Authorization: "example", Size: 2}) {
		t.Fatal("valid session denied")
	}
	for _, h := range []local.Header{
		{Method: "authorize", Size: 2}, {Method: "backup", Authorization: "example", Size: 2},
		{Method: "post_journal", Authorization: "example", Size: (1 << 20) + 1},
		{Method: "get_book", Authorization: "example", Size: -1},
		{Method: "unknown", Authorization: "example", Size: 2},
	} {
		if s.admit(501, h) {
			t.Fatal("unapproved body admitted", h.Method)
		}
	}
	if _, ok := s.scope(502, "example"); ok {
		t.Fatal("session crossed peer boundary")
	}
	s.sessions[tokenKey("expired")] = session{UID: 501, Expires: time.Now().Add(-time.Second)}
	if _, ok := s.scope(501, "expired"); ok {
		t.Fatal("expired session accepted")
	}
	if _, ok := (&Server{}).scope(501, "example"); ok {
		t.Fatal("restart revived a session")
	}
}

func TestEncryptedBackupAuthenticatesContentAndKey(t *testing.T) {
	plain := []byte(`{"format":"example","amount_minor":"9007199254740993"}`)
	key := secret()
	encrypted, e := encryptBackup(plain, key)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(encrypted), string(plain)) {
		t.Fatal("backup contains plaintext")
	}
	decoded, e := decryptBackup(encrypted, key)
	if e != nil || string(decoded) != string(plain) {
		t.Fatal("backup round trip failed", e)
	}
	if _, e = decryptBackup(encrypted, secret()); e == nil {
		t.Fatal("wrong key accepted")
	}
	corrupt := append([]byte(nil), encrypted...)
	corrupt[len(corrupt)-1] ^= 1
	if _, e = decryptBackup(corrupt, key); e == nil {
		t.Fatal("corruption accepted")
	}
	if _, e = decryptBackup(encrypted[:len(encrypted)-8], key); e == nil {
		t.Fatal("truncation accepted")
	}
}

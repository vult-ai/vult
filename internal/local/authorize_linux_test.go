package local

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type ownerBusCall func(context.Context, string, ...any) *dbus.Call

func (call ownerBusCall) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	if flags != 0 {
		return &dbus.Call{Err: errors.New("unexpected bus flags")}
	}
	return call(ctx, method, args...)
}

func TestOwnerBusCancellationBoundsSynchronousWrites(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	socket := &observedBusSocket{Conn: client, writing: make(chan struct{})}
	connection, err := dbus.NewConn(socket)
	if err != nil {
		t.Fatal(err)
	}
	bus := &ownerBusConnection{connection, socket}
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		call := bus.object(bus.Object("org.example.Unresponsive", "/")).CallWithContext(ctx, "org.example.Unresponsive.Check", 0)
		done <- call.Err
	}()
	select {
	case <-socket.writing:
	case <-time.After(time.Second):
		t.Fatal("positive blocked-writer control did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unread request unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("context failed to interrupt a synchronous bus write")
	}
}

type observedBusSocket struct {
	net.Conn
	writing chan struct{}
	once    sync.Once
}

func (socket *observedBusSocket) Write(data []byte) (int, error) {
	socket.once.Do(func() { close(socket.writing) })
	return socket.Conn.Write(data)
}

func TestOwnerBusCloseReleasesBlockedWriter(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	socket := &observedBusSocket{Conn: client, writing: make(chan struct{})}
	connection, err := dbus.NewConn(socket)
	if err != nil {
		t.Fatal(err)
	}
	bus := &ownerBusConnection{connection, socket}
	defer bus.Close()
	written := make(chan struct{})
	go func() {
		defer close(written)
		bus.Object("org.example.Unresponsive", "/").CallWithContext(context.Background(), "org.example.Unresponsive.Check", 0)
	}()
	select {
	case <-socket.writing:
	case <-time.After(time.Second):
		t.Fatal("positive blocked-writer control did not start")
	}
	closed := make(chan struct{})
	go func() { bus.Close(); close(closed) }()
	for _, done := range []chan struct{}{closed, written} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("connection cleanup waited behind a stalled send")
		}
	}
}

func ownerActionDescription() actionDescription {
	return actionDescription{ID: polkitAction, Message: "$(vult.prompt)", Active: 2,
		Annotations: map[string]string{"org.freedesktop.policykit.owner": "unix-user:" + ServiceUser}}
}

func TestOwnerAuthorizationHelperHasNoOperationArguments(t *testing.T) {
	payload := SessionRequest{ConnectionID: "11111111-1111-4111-8111-111111111111", ImportDirectory: "/example/imports", ExportDirectory: "/example/exports"}
	command, err := ownerHelperCommand(context.Background(), "open_session", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.Args, []string{BinaryPath, "_owner_authorization"}) || !reflect.DeepEqual(command.Env, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}) || len(command.ExtraFiles) != 0 {
		t.Fatal("helper exposes operation details or inherited descriptors")
	}
	input, err := io.ReadAll(command.Stdin)
	var request ownerHelperRequest
	if err != nil || json.Unmarshal(input, &request) != nil || request.Action != "open_session" || Digest(request.Payload) != Digest(payload) {
		t.Fatal("private helper input lost the exact operation")
	}
	if command.Path != BinaryPath {
		t.Fatal("helper uses a caller-selected executable")
	}
}

func TestOwnerPolicyRequiresFreshActiveAdministrator(t *testing.T) {
	var policy struct {
		Actions []struct {
			ID       string `xml:"id,attr"`
			Message  string `xml:"message"`
			Defaults struct {
				Any      string `xml:"allow_any"`
				Inactive string `xml:"allow_inactive"`
				Active   string `xml:"allow_active"`
			} `xml:"defaults"`
			Annotation struct {
				Key   string `xml:"key,attr"`
				Value string `xml:",chardata"`
			} `xml:"annotate"`
		} `xml:"action"`
	}
	if err := xml.Unmarshal(ownerPolicy, &policy); err != nil || len(policy.Actions) != 1 {
		t.Fatal("invalid owner policy", err)
	}
	a := policy.Actions[0]
	if a.ID != polkitAction || a.Message != "$(vult.prompt)" || a.Defaults.Any != "no" || a.Defaults.Inactive != "no" || a.Defaults.Active != "auth_admin" || a.Annotation.Key != "org.freedesktop.policykit.owner" || a.Annotation.Value != "unix-user:"+ServiceUser {
		t.Fatal("owner policy permits missing, retained or unrelated authority")
	}
	for _, name := range []string{"", "ai.vult.owner", ":", ":1", ":1.", ":.2", ":1..2", ":1.2\n", ":1.2\x00", ":a.世界", ":1." + strings.Repeat("2", 253)} {
		if uniqueBusName(name) {
			t.Fatal("invalid bus subject accepted")
		}
	}
	if !uniqueBusName(":1.25") {
		t.Fatal("valid unique name rejected")
	}
	if registeredOwnerPolicy(nil) || registeredOwnerPolicy([]actionDescription{ownerActionDescription(), ownerActionDescription()}) {
		t.Fatal("absent or duplicate owner policy accepted")
	}
}

func TestOwnerAuthorizationChecksLiveIdentityAndExactPrompt(t *testing.T) {
	for _, variant := range []string{"valid", "uid", "pid", "uid_changed", "pid_changed", "name_lost", "permissive", "retained_policy", "message", "owner", "retained_result", "denied", "challenge", "cancelled", "late_cancel", "bus_error"} {
		t.Run(variant, func(t *testing.T) {
			peer := Peer{UID: 1000, PID: 1234}
			payload, _ := json.Marshal(SessionRequest{ConnectionID: "11111111-1111-4111-8111-111111111111", ImportDirectory: "/example/imports", ExportDirectory: "/example/exports"})
			request := OwnerRequest{Action: "open_session", Payload: payload, BusName: ":1.25"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			uidCalls, pidCalls, checks, cancellations := 0, 0, 0, 0
			cancellationID := ""
			broker := ownerBusCall(func(ctx context.Context, method string, args ...any) *dbus.Call {
				if !reflect.DeepEqual(args, []any{request.BusName}) {
					t.Fatal("bus lookup changed the unique subject")
				}
				switch method {
				case "org.freedesktop.DBus.GetConnectionUnixUser":
					uidCalls++
					if uidCalls == 2 && variant == "name_lost" || variant == "bus_error" {
						return &dbus.Call{Err: errors.New("example bus failure")}
					}
					uid := peer.UID
					if variant == "uid" || uidCalls == 2 && variant == "uid_changed" {
						uid++
					}
					return &dbus.Call{Body: []any{uid}}
				case "org.freedesktop.DBus.GetConnectionUnixProcessID":
					pidCalls++
					pid := peer.PID
					if variant == "pid" || pidCalls == 2 && variant == "pid_changed" {
						pid++
					}
					return &dbus.Call{Body: []any{pid}}
				default:
					t.Fatal("unexpected broker method", method)
					return nil
				}
			})
			authority := ownerBusCall(func(callCtx context.Context, method string, args ...any) *dbus.Call {
				switch method {
				case polkitMethod + "EnumerateActions":
					a := ownerActionDescription()
					switch variant {
					case "permissive":
						a.Any = 5
					case "retained_policy":
						a.Active = 4
					case "message":
						a.Message = "Approve"
					case "owner":
						a.Annotations["org.freedesktop.policykit.owner"] = "unix-user:other"
					}
					return &dbus.Call{Body: []any{[]actionDescription{a}}}
				case polkitMethod + "CheckAuthorization":
					checks++
					prompt, err := OwnerPrompt(request.Action, request.Payload)
					subject, ok := args[0].(busSubject)
					if err != nil || !ok || subject.Kind != "system-bus-name" || len(subject.Details) != 1 || subject.Details["name"].Value() != request.BusName || args[1] != polkitAction || !reflect.DeepEqual(args[2], map[string]string{"vult.prompt": prompt}) || args[3] != uint32(1) {
						t.Fatal("owner authority or disclosure changed")
					}
					cancellationID, ok = args[4].(string)
					if !ok || len(cancellationID) != 48 {
						t.Fatal("missing unique cancellation identity")
					}
					if variant == "cancelled" || variant == "late_cancel" {
						cancel()
						if variant == "cancelled" {
							return &dbus.Call{Err: context.Canceled}
						}
					}
					result := authorizationResult{Authorized: variant != "denied", Challenge: variant == "challenge", Details: map[string]string{}}
					if variant == "retained_result" {
						result.Details["polkit.temporary_authorization_id"] = "example"
					}
					return &dbus.Call{Body: []any{result}}
				case polkitMethod + "CancelCheckAuthorization":
					cancellations++
					if callCtx.Err() != nil || args[0] != cancellationID {
						t.Fatal("cancellation lost the live checker connection or identity")
					}
					if _, ok := callCtx.Deadline(); !ok {
						t.Fatal("cancellation cleanup is unbounded")
					}
					return &dbus.Call{}
				default:
					t.Fatal("unexpected authority method", method)
					return nil
				}
			})
			err := checkNativeOwner(ctx, broker, authority, peer, request)
			if (err == nil) != (variant == "valid") {
				t.Fatal("unexpected authorization result", err)
			}
			if variant == "valid" && (checks != 1 || uidCalls != 2 || pidCalls != 2) {
				t.Fatal("success did not recheck the live subject")
			}
			if (variant == "cancelled" || variant == "late_cancel") && cancellations != 1 {
				t.Fatal("cancelled prompt was forgotten")
			}
		})
	}
}

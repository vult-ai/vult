package local

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

const (
	systemBusPath = "/run/dbus/system_bus_socket"
	polkitName    = "org.freedesktop.PolicyKit1"
	polkitPath    = dbus.ObjectPath("/org/freedesktop/PolicyKit1/Authority")
	polkitMethod  = "org.freedesktop.PolicyKit1.Authority."
	polkitAction  = "ai.vult.owner"
	polkitFile    = "/usr/share/polkit-1/actions/ai.vult.owner.policy"
)

//go:embed ai.vult.owner.policy
var ownerPolicy []byte

var errOwnerAuthorization = errors.New("owner authorization cancelled or unavailable")

type ownerBusConnection struct {
	*dbus.Conn
	socket net.Conn
}

func (bus *ownerBusConnection) Close() {
	// godbus serializes Close behind synchronous writes. Close the transport
	// first so a stalled broker cannot retain an authorization worker.
	bus.socket.Close()
	bus.Conn.Close()
}

func (bus *ownerBusConnection) object(object dbus.BusObject) busObject {
	return deadlineBusObject{object, bus.socket}
}

type deadlineBusObject struct {
	dbus.BusObject
	socket net.Conn
}

func (object deadlineBusObject) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if object.socket.SetWriteDeadline(deadline) != nil {
		return &dbus.Call{Err: errOwnerAuthorization}
	}
	dismissed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		object.socket.SetWriteDeadline(time.Now())
		close(dismissed)
	})
	call := object.BusObject.CallWithContext(ctx, method, flags, args...)
	if !stop() {
		<-dismissed
	}
	// A normal cancelled check still needs a live transport for its explicit
	// CancelCheckAuthorization call, using a separate cleanup context.
	object.socket.SetWriteDeadline(time.Time{})
	return call
}

func systemBus(ctx context.Context) (*ownerBusConnection, error) {
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(startup, "unix", systemBusPath)
	if err != nil {
		return nil, errOwnerAuthorization
	}
	connection, ok := raw.(*net.UnixConn)
	if !ok {
		raw.Close()
		return nil, errOwnerAuthorization
	}
	uid, err := PeerUID(connection)
	if err != nil || uid != 0 {
		connection.Close()
		return nil, errOwnerAuthorization
	}
	stop := context.AfterFunc(startup, func() { connection.Close() })
	defer stop()
	bus, err := dbus.NewConn(connection)
	if err != nil {
		connection.Close()
		return nil, errOwnerAuthorization
	}
	if bus.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Geteuid()))}) != nil || bus.Hello() != nil || startup.Err() != nil {
		connection.Close()
		bus.Close()
		return nil, errOwnerAuthorization
	}
	return &ownerBusConnection{bus, connection}, nil
}

type ownerHelperRequest struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

func ownerHelperCommand(ctx context.Context, action string, payload any) (*exec.Cmd, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	input, err := json.Marshal(ownerHelperRequest{Action: action, Payload: data})
	if err != nil || len(input) > 16<<10 {
		return nil, errOwnerAuthorization
	}
	command := exec.CommandContext(ctx, BinaryPath, "_owner_authorization")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	command.Stdin = bytes.NewReader(input)
	return command, nil
}

func (c Client) authorizeOwner(ctx context.Context, input Authorization, payload any, _ string) (json.RawMessage, error) {
	command, err := ownerHelperCommand(ctx, input.Action, payload)
	if err != nil {
		return nil, err
	}
	result, err := command.Output()
	if err != nil {
		return nil, errOwnerAuthorization
	}
	return json.RawMessage(bytes.TrimSpace(result)), nil
}

// OwnerAuthorizationHelper keeps operation details off the polkit subject's
// command line. The helper is unprivileged and carries them only over pipes.
func OwnerAuthorizationHelper(ctx context.Context, input io.Reader, output io.Writer) error {
	if os.Geteuid() == 0 {
		return errOwnerAuthorization
	}
	data, err := io.ReadAll(io.LimitReader(input, (16<<10)+1))
	var request ownerHelperRequest
	if err != nil || len(data) > 16<<10 || strictjson.Decode(data, &request) != nil {
		return errOwnerAuthorization
	}
	if _, err := OwnerPrompt(request.Action, request.Payload); err != nil {
		return errOwnerAuthorization
	}
	client, err := NewClient()
	if err != nil {
		return errOwnerAuthorization
	}
	bus, err := systemBus(ctx)
	if err != nil {
		return err
	}
	defer bus.Close()
	for _, name := range bus.Names() {
		if uniqueBusName(name) {
			result, err := client.Call(ctx, "owner_authorization", "", OwnerRequest{Action: request.Action, Payload: request.Payload, BusName: name})
			if err != nil {
				return errOwnerAuthorization
			}
			_, err = output.Write(result)
			return err
		}
	}
	return errOwnerAuthorization
}

func NativeOwnerAuthorizer() func(context.Context, Peer, OwnerRequest) error {
	return authorizeNativeOwner
}

func authorizeNativeOwner(ctx context.Context, peer Peer, request OwnerRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if !uniqueBusName(request.BusName) || peer.UID == 0 || peer.PID == 0 || trustedPolicy(polkitFile) != nil {
		return errOwnerAuthorization
	}
	bus, err := systemBus(ctx)
	if err != nil {
		return err
	}
	// This connection is dedicated to one check. Closing it also removes the
	// caller subject if explicit cancellation cannot reach the authority.
	defer bus.Close()
	broker := bus.object(bus.BusObject())
	var owner string
	if boundedCall(ctx, broker, "org.freedesktop.DBus.GetNameOwner", polkitName).Store(&owner) != nil || !uniqueBusName(owner) {
		return errOwnerAuthorization
	}
	// Pin the authority's unique name: a restart must fail rather than resume a
	// different check or inherit a different action registration.
	return checkNativeOwner(ctx, broker, bus.object(bus.Object(owner, polkitPath)), peer, request)
}

type busObject interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

type actionDescription struct {
	ID, Description, Message, Vendor, URL, Icon string
	Any, Inactive, Active                       uint32
	Annotations                                 map[string]string
}

type authorizationResult struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}

type busSubject struct {
	Kind    string
	Details map[string]dbus.Variant
}

func boundedCall(ctx context.Context, object busObject, method string, args ...any) *dbus.Call {
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return object.CallWithContext(call, method, 0, args...)
}

func checkNativeOwner(ctx context.Context, broker, authority busObject, peer Peer, request OwnerRequest) error {
	if ctx.Err() != nil || !uniqueBusName(request.BusName) || peer.UID == 0 || peer.PID == 0 {
		return errOwnerAuthorization
	}
	prompt, err := OwnerPrompt(request.Action, request.Payload)
	if err != nil || len(prompt) > 8192 {
		return errOwnerAuthorization
	}
	var actions []actionDescription
	if boundedCall(ctx, authority, polkitMethod+"EnumerateActions", "C").Store(&actions) != nil || !registeredOwnerPolicy(actions) || !matchingBusPeer(ctx, broker, request.BusName, peer) {
		return errOwnerAuthorization
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errOwnerAuthorization
	}
	cancellationID := hex.EncodeToString(random[:])
	subject := busSubject{"system-bus-name", map[string]dbus.Variant{"name": dbus.MakeVariant(request.BusName)}}
	var result authorizationResult
	err = authority.CallWithContext(ctx, polkitMethod+"CheckAuthorization", 0, subject, polkitAction,
		map[string]string{"vult.prompt": prompt}, uint32(1), cancellationID).Store(&result)
	if err != nil || ctx.Err() != nil {
		// Use the same live checker connection and a separate bounded context:
		// the request context is already cancelled and cannot dismiss the dialog.
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		authority.CallWithContext(cleanup, polkitMethod+"CancelCheckAuthorization", 0, cancellationID)
		return errOwnerAuthorization
	}
	if !result.Authorized || result.Challenge || result.Details["polkit.temporary_authorization_id"] != "" || result.Details["polkit.retains_authorization_after_challenge"] != "" {
		return errOwnerAuthorization
	}
	if !matchingBusPeer(ctx, broker, request.BusName, peer) || ctx.Err() != nil {
		return errOwnerAuthorization
	}
	return nil
}

func matchingBusPeer(ctx context.Context, broker busObject, name string, peer Peer) bool {
	var uid, pid uint32
	return boundedCall(ctx, broker, "org.freedesktop.DBus.GetConnectionUnixUser", name).Store(&uid) == nil &&
		boundedCall(ctx, broker, "org.freedesktop.DBus.GetConnectionUnixProcessID", name).Store(&pid) == nil &&
		uid == peer.UID && pid == peer.PID && ctx.Err() == nil
}

func registeredOwnerPolicy(actions []actionDescription) bool {
	found := false
	for _, action := range actions {
		if action.ID != polkitAction {
			continue
		}
		if found || action.Message != "$(vult.prompt)" || action.Any != 0 || action.Inactive != 0 || action.Active != 2 || len(action.Annotations) != 1 || action.Annotations["org.freedesktop.policykit.owner"] != "unix-user:"+ServiceUser {
			return false
		}
		found = true
	}
	return found
}

func uniqueBusName(name string) bool {
	if len(name) < 4 || len(name) > 255 || name[0] != ':' {
		return false
	}
	parts := strings.Split(name[1:], ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

func trustedPolicy(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errOwnerAuthorization
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || current == path && (!info.Mode().IsRegular() || stat.Nlink != 1) || current != path && !info.IsDir() {
			return errOwnerAuthorization
		}
		if current == "/" {
			break
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return errOwnerAuthorization
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, int64(len(ownerPolicy))+1))
	if err != nil || !bytes.Equal(content, ownerPolicy) {
		return errOwnerAuthorization
	}
	return nil
}

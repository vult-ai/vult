package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const Version = "0.3.0"

type grant struct {
	Action, Digest string
	UID            uint32
	Expires        time.Time
}
type session struct {
	Scope   ledger.Scope
	UID     uint32
	Expires time.Time
}
type Server struct {
	Admin         ledger.Admin
	Ledger        ledger.Store
	RecoveryKey   string
	ProvisionKeys func(context.Context, string) error
	mu            sync.Mutex
	grants        map[string]grant
	sessions      map[string]session
}

func (s *Server) initMaps() {
	if s.grants == nil {
		s.grants = map[string]grant{}
	}
	if s.sessions == nil {
		s.sessions = map[string]session{}
	}
}
func tokenKey(t string) string { sum := sha256.Sum256([]byte(t)); return hex.EncodeToString(sum[:]) }
func (s *Server) authorize(uid uint32, input json.RawMessage) (json.RawMessage, error) {
	if uid != 0 {
		return nil, ledger.ErrDenied
	}
	var q local.Authorization
	if strictjson.Decode(input, &q) != nil || !local.OwnerAction(q.Action) || len(q.Digest) != 64 {
		return nil, ledger.ErrInvalid
	}
	if _, e := hex.DecodeString(q.Digest); e != nil {
		return nil, ledger.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initMaps()
	now := time.Now()
	for k, v := range s.grants {
		if now.After(v.Expires) {
			delete(s.grants, k)
		}
	}
	if len(s.grants) >= 32 {
		return nil, errors.New("too many pending owner authorizations")
	}
	token := secret()
	s.grants[tokenKey(token)] = grant{q.Action, q.Digest, q.UID, now.Add(time.Minute)}
	return marshal(struct {
		Token string `json:"token"`
	}{token}), nil
}
func (s *Server) consume(uid uint32, q local.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initMaps()
	k := tokenKey(q.Authorization)
	g, ok := s.grants[k]
	if !ok {
		return false
	}
	delete(s.grants, k)
	h := sha256.Sum256(q.Payload)
	return g.UID == uid && g.Action == q.Method && time.Now().Before(g.Expires) && g.Digest == hex.EncodeToString(h[:])
}
func (s *Server) scope(uid uint32, token string) (ledger.Scope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initMaps()
	key := tokenKey(token)
	value, ok := s.sessions[key]
	if !ok || value.UID != uid || !time.Now().Before(value.Expires) {
		delete(s.sessions, key)
		return ledger.Scope{}, false
	}
	return value.Scope, true
}
func (s *Server) admit(uid uint32, h local.Header) bool {
	if h.Size < 2 || h.Size > local.MaxMessage {
		return false
	}
	if h.Method == "document_upload" || h.Method == "document_download" {
		scope, ok := s.scope(uid, h.Authorization)
		if !ok || h.Size > 4096 || s.Ledger.Documents == nil {
			return false
		}
		if h.Method == "document_upload" {
			return scope.Access == "write" && h.StreamSize > 0 && h.StreamSize <= documents.MaxSize
		}
		return h.StreamSize == 0
	}
	if h.StreamSize != 0 && h.Method != "archive_restore" {
		return false
	}
	if h.Method == "status" {
		return h.Size <= 128
	}
	if h.Method == "authorize" {
		return uid == 0 && h.Size <= 4096
	}
	if local.OwnerAction(h.Method) {
		if h.Method == "archive_backup" && h.Size > 4096 {
			return false
		}
		if h.Method == "archive_restore" && (h.Size > 4096 || h.StreamSize < 1 || h.StreamSize > local.MaxEncryptedArchive) {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.initMaps()
		g, ok := s.grants[tokenKey(h.Authorization)]
		return ok && g.UID == uid && g.Action == h.Method && time.Now().Before(g.Expires)
	}
	if h.Size > 1<<20 || !ledger.Supported(h.Method) {
		return false
	}
	_, ok := s.scope(uid, h.Authorization)
	return ok
}
func marshal(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func (s *Server) Handle(ctx context.Context, uid uint32, q local.Request) (json.RawMessage, error) {
	if q.Method == "status" {
		if strictjson.Decode(q.Payload, &struct{}{}) != nil {
			return nil, ledger.ErrInvalid
		}
		return marshal(struct {
			Version string `json:"version"`
			Schema  string `json:"schema_sha256"`
			Ready   bool   `json:"ready"`
		}{Version, ledger.SchemaSHA256(), true}), nil
	}
	if q.Method == "authorize" {
		return s.authorize(uid, q.Payload)
	}
	if local.OwnerAction(q.Method) {
		if !s.consume(uid, q) {
			return nil, ledger.ErrDenied
		}
		return s.owner(ctx, uid, q.Method, q.Payload)
	}
	scope, ok := s.scope(uid, q.Authorization)
	if !ok {
		return nil, ledger.ErrDenied
	}
	return s.Ledger.Call(ctx, scope, q.Method, q.Payload)
}
func (s *Server) owner(ctx context.Context, uid uint32, method string, input []byte) (json.RawMessage, error) {
	decode := func(v any) error {
		if strictjson.Decode(input, v) != nil {
			return ledger.ErrInvalid
		}
		return nil
	}
	switch method {
	case "init":
		if decode(&struct{}{}) != nil {
			return nil, ledger.ErrInvalid
		}
		id, e := s.Admin.EnsureTenant(ctx)
		if e != nil {
			return nil, e
		}
		return marshal(struct {
			TenantID    string `json:"tenant_id"`
			RecoveryKey string `json:"recovery_key"`
		}{id, s.RecoveryKey}), nil
	case "book_create":
		var q struct {
			Name     string `json:"name"`
			Currency string `json:"currency"`
			Scale    int    `json:"scale"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		v, e := s.Admin.CreateBook(ctx, q.Name, q.Currency, q.Scale)
		return marshal(v), e
	case "book_list":
		if decode(&struct{}{}) != nil {
			return nil, ledger.ErrInvalid
		}
		v, e := s.Admin.Books(ctx)
		return marshal(v), e
	case "connection_create":
		var q struct {
			Name   string `json:"name"`
			BookID string `json:"book_id"`
			Access string `json:"access"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		v, e := s.Admin.CreateConnection(ctx, q.BookID, q.Name, q.Access)
		return marshal(v), e
	case "connection_list":
		if decode(&struct{}{}) != nil {
			return nil, ledger.ErrInvalid
		}
		v, e := s.Admin.Connections(ctx)
		return marshal(v), e
	case "connection_revoke":
		var q struct {
			ConnectionID string `json:"connection_id"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		if e := s.Admin.Revoke(ctx, q.ConnectionID); e != nil {
			return nil, e
		}
		s.mu.Lock()
		for k, v := range s.sessions {
			if v.Scope.ConnectionID == q.ConnectionID {
				delete(s.sessions, k)
			}
		}
		s.mu.Unlock()
		return marshal(struct {
			Status string `json:"status"`
		}{"revoked"}), nil
	case "period_close":
		var q struct {
			BookID  string `json:"book_id"`
			From    string `json:"from"`
			Through string `json:"through"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		if e := s.Admin.ClosePeriod(ctx, q.BookID, q.From, q.Through); e != nil {
			return nil, e
		}
		return marshal(struct {
			Status string `json:"status"`
		}{"closed"}), nil
	case "verify", "export":
		var q struct {
			BookID string `json:"book_id"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		if method == "verify" {
			v, e := s.Admin.Verify(ctx, q.BookID)
			return marshal(v), e
		}
		v, e := s.Admin.Export(ctx, q.BookID)
		return marshal(v), e
	case "backup":
		if decode(&struct{}{}) != nil {
			return nil, ledger.ErrInvalid
		}
		v, e := s.Admin.Snapshot(ctx)
		if e != nil {
			return nil, e
		}
		b, e := encryptBackup(marshal(v), s.RecoveryKey)
		if e != nil {
			return nil, e
		}
		return marshal(struct {
			Data []byte `json:"data"`
		}{b}), nil
	case "restore":
		var q struct {
			Data        []byte `json:"data"`
			RecoveryKey string `json:"recovery_key"`
		}
		if decode(&q) != nil {
			return nil, ledger.ErrInvalid
		}
		b, e := decryptBackup(q.Data, q.RecoveryKey)
		if e != nil {
			return nil, e
		}
		var snapshot ledger.Snapshot
		if strictjson.Decode(b, &snapshot) != nil {
			return nil, ledger.ErrInvalid
		}
		if e = s.restoreLegacySnapshot(ctx, snapshot); e != nil {
			return nil, e
		}
		s.mu.Lock()
		s.sessions = map[string]session{}
		s.mu.Unlock()
		return marshal(struct {
			Status      string `json:"status"`
			Connections string `json:"connections"`
		}{"restored", "reauthorization_required"}), nil
	case "open_session":
		var q local.SessionRequest
		if decode(&q) != nil || !q.Valid() {
			return nil, ledger.ErrInvalid
		}
		c, e := s.Admin.Connection(ctx, q.ConnectionID)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.initMaps()
		now := time.Now()
		for k, v := range s.sessions {
			if now.After(v.Expires) {
				delete(s.sessions, k)
			}
		}
		if len(s.sessions) >= 64 {
			return nil, errors.New("too many active agent sessions")
		}
		token := secret()
		expiry := now.Add(8 * time.Hour)
		s.sessions[tokenKey(token)] = session{ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}, uid, expiry}
		return marshal(struct {
			Token     string    `json:"token"`
			Access    string    `json:"access"`
			BookID    string    `json:"book_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}{token, c.Access, c.BookID, expiry}), nil
	}
	return nil, ledger.ErrInvalid
}
func safeError(e error) string {
	for _, known := range []error{ledger.ErrInvalid, ledger.ErrDenied, ledger.ErrConflict, ledger.ErrNotFound, ledger.ErrUncertain, ledger.ErrLimit, ledger.ErrArchive, ledger.ErrArchiveCleanup, documents.ErrUnavailable, documents.ErrIntegrity} {
		if errors.Is(e, known) {
			return known.Error()
		}
	}
	return "operation failed; no automatic retry"
}
func (s *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-done:
		}
	}()
	slots := make(chan struct{}, 8)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		c, e := listener.AcceptUnix()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			uid, e := local.PeerUID(c)
			if e != nil {
				return
			}
			reader := bufio.NewReaderSize(c, 4096)
			b, e := reader.ReadSlice('\n')
			if e != nil || len(b) > 4096 {
				return
			}
			var h local.Header
			if strictjson.Decode(b[:len(b)-1], &h) != nil || !s.admit(uid, h) {
				json.NewEncoder(c).Encode(local.Response{Error: ledger.ErrDenied.Error()})
				return
			}
			s.servePayload(ctx, uid, h, reader, c)
		}()
	}
}
func (s *Server) servePayload(ctx context.Context, uid uint32, h local.Header, reader io.Reader, c net.Conn) {
	var e error
	duration := 90 * time.Second
	if h.Method == "archive_backup" || h.Method == "archive_restore" {
		duration = 15 * time.Minute
	}
	c.SetDeadline(time.Now().Add(duration + time.Second))
	callCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	stop := context.AfterFunc(callCtx, func() { c.SetDeadline(time.Now()) })
	defer stop()
	payload := make([]byte, h.Size)
	if _, e = io.ReadFull(reader, payload); e != nil {
		return
	}
	q := local.Request{Method: h.Method, Authorization: h.Authorization, Payload: payload}
	if h.Method == "archive_backup" || h.Method == "archive_restore" {
		s.archiveStream(callCtx, uid, q, h.StreamSize, reader, c)
		return
	}
	if h.Method == "document_upload" || h.Method == "document_download" {
		s.documentStream(callCtx, uid, q, h.StreamSize, reader, c)
		return
	}
	result, e := s.Handle(callCtx, uid, q)
	response := local.Response{Result: result}
	if e != nil {
		response.Result = nil
		response.Error = safeError(e)
	}
	json.NewEncoder(c).Encode(response)
}

func Listen(path string) (*net.UnixListener, error) {
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0666); e != nil {
		listener.Close()
		return nil, e
	}
	return listener, nil
}

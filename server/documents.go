package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

const transferPrefix = "/documents/transfers/"

type documentCapability struct {
	scope                               ledger.Scope
	token, method, documentID, uploadID string
	size                                int64
	sha256                              string
	expires                             time.Time
}

type documentTransfers struct {
	mu           sync.Mutex
	capabilities map[string]documentCapability
	origin       string
	db           *pgxpool.Pool
	sessions     *access.Service
	admission    Admission
	vault        *documents.Vault
	slots        chan struct{}
}

func newDocumentTransfers(origin string, db *pgxpool.Pool, sessions *access.Service, admission Admission, vault *documents.Vault) *documentTransfers {
	return &documentTransfers{capabilities: map[string]documentCapability{}, origin: origin, db: db, sessions: sessions, admission: admission, vault: vault, slots: make(chan struct{}, 4)}
}

func capabilityHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// issue returns only short-lived, one-use byte capabilities. The caller has
// already resolved identity, scope and current metadata through the ledger.
// A capability is not a document identity and is never persisted in a receipt.
func (t *documentTransfers) issue(scope ledger.Scope, token string, inspection ledger.DocumentInspection) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	owned := 0
	for key, c := range t.capabilities {
		if !c.expires.After(now) {
			delete(t.capabilities, key)
		} else if c.scope.ConnectionID == scope.ConnectionID {
			owned++
		}
	}
	wanted := 0
	if inspection.Document.State == "ready" {
		wanted++
	}
	if inspection.Upload != nil && inspection.Upload.Status == "pending" {
		wanted++
	}
	if len(t.capabilities)+wanted > 128 || owned+wanted > 8 {
		return nil, documents.ErrUnavailable
	}
	newURL := func(method, upload string) (string, error) {
		var bytes [32]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return "", documents.ErrUnavailable
		}
		secret := base64.RawURLEncoding.EncodeToString(bytes[:])
		expires := now.Add(2 * time.Minute)
		if upload != "" && inspection.Upload.ExpiresAt != nil && inspection.Upload.ExpiresAt.Before(expires) {
			expires = *inspection.Upload.ExpiresAt
		}
		t.capabilities[capabilityHash(secret)] = documentCapability{scope: scope, token: token, method: method, documentID: inspection.Document.ID, uploadID: upload, size: inspection.Document.Size, sha256: inspection.Document.SHA256, expires: expires}
		return t.origin + transferPrefix + secret, nil
	}
	var err error
	if inspection.Document.State == "ready" {
		inspection.DownloadURL, err = newURL(http.MethodGet, "")
		if err != nil {
			return nil, err
		}
	}
	if inspection.Upload != nil && inspection.Upload.Status == "pending" {
		inspection.UploadURL, err = newURL(http.MethodPut, inspection.Upload.ID)
		if err != nil {
			return nil, err
		}
	}
	result, err := json.Marshal(inspection)
	return result, err
}

func (t *documentTransfers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) > 0 || !strings.HasPrefix(r.URL.Path, transferPrefix) {
		failure(w, 403)
		return
	}
	secret := strings.TrimPrefix(r.URL.Path, transferPrefix)
	if len(secret) != 43 || strings.Contains(secret, "/") {
		failure(w, 403)
		return
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 429)
		return
	}
	t.mu.Lock()
	key := capabilityHash(secret)
	c, ok := t.capabilities[key]
	if ok && c.method == r.Method {
		delete(t.capabilities, key)
	}
	t.mu.Unlock()
	if !ok || !c.expires.After(time.Now()) || c.method != r.Method {
		failure(w, 403)
		return
	}
	// Cookies never authorize a byte transfer. The original agent credential is
	// rechecked here and again under the same database lease as the stream.
	scope, err := t.sessions.ResolveAgent(r.Context(), c.token)
	if err != nil || scope != c.scope {
		failure(w, 403)
		return
	}
	method := "get_document"
	if r.Method == http.MethodPut {
		method = "complete_document_upload"
	}
	if t.admission != nil && t.admission.Check(r.Context(), scope, method) != nil {
		failure(w, 403)
		return
	}
	store := remoteStore(t.db, t.sessions, c.token, t.admission, t.vault)
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(90 * time.Second))
	_ = controller.SetWriteDeadline(time.Now().Add(90 * time.Second))
	if r.Method == http.MethodPut {
		if r.ContentLength != c.size || r.Header.Get("Content-Type") != "application/octet-stream" {
			failure(w, 400)
			return
		}
		result, err := store.Upload(r.Context(), scope, c.uploadID, http.MaxBytesReader(w, r.Body, c.size))
		if err != nil {
			failure(w, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(result)
		return
	}
	if r.ContentLength > 0 || r.Header.Get("Range") != "" {
		failure(w, 400)
		return
	}
	dst := &documentDownload{response: w, size: c.size, sha: c.sha256}
	if err := store.Download(r.Context(), scope, c.documentID, dst); err != nil {
		if !dst.started {
			failure(w, 403)
		}
		return
	}
	_ = dst.finish()
}

type documentDownload struct {
	response http.ResponseWriter
	size     int64
	sha      string
	started  bool
	tail     []byte
}

// Withhold the final bytes until both authenticated EOF and the database read
// lease have completed. Otherwise a bad final encrypted frame could leave a
// length-complete HTTP download that appeared successful.
func (d *documentDownload) Write(p []byte) (int, error) {
	const retain = 64 << 10
	if d.tail == nil {
		d.tail = make([]byte, 0, retain)
	}
	flush := len(d.tail) + len(p) - retain
	if flush <= 0 {
		d.tail = append(d.tail, p...)
		return len(p), nil
	}
	fromTail := min(flush, len(d.tail))
	if _, err := d.write(d.tail[:fromTail]); err != nil {
		return 0, err
	}
	fromInput := flush - fromTail
	if _, err := d.write(p[:fromInput]); err != nil {
		return 0, err
	}
	remaining := copy(d.tail, d.tail[fromTail:])
	d.tail = append(d.tail[:remaining], p[fromInput:]...)
	return len(p), nil
}

func (d *documentDownload) finish() error {
	_, err := d.write(d.tail)
	d.tail = nil
	return err
}

func (d *documentDownload) write(bytes []byte) (int, error) {
	if len(bytes) == 0 {
		return 0, nil
	}
	if !d.started {
		d.response.Header().Set("Content-Type", "application/octet-stream")
		d.response.Header().Set("Content-Disposition", `attachment; filename="document"`)
		d.response.Header().Set("X-Content-Type-Options", "nosniff")
		d.response.Header().Set("Cache-Control", "no-store")
		d.response.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		d.response.Header().Set("Content-Length", strconv.FormatInt(d.size, 10))
		d.response.Header().Set("X-Document-SHA256", d.sha)
		d.response.WriteHeader(http.StatusOK)
		d.started = true
	}
	return d.response.Write(bytes)
}

var _ io.Writer = (*documentDownload)(nil)

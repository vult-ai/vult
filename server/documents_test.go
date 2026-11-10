package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type documentServerFixture struct {
	db              *testutil.Database
	sessions        *access.Service
	operator        access.Operator
	server          *http.Server
	dir             string
	vault           *documents.Vault
	auth, owners    *pgxpool.Pool
	tenant, session string
	book            ledger.Book
	identity        access.Identity
}

func documentServer(t *testing.T) *documentServerFixture {
	t.Helper()
	db := testutil.New(t)
	f := &documentServerFixture{db: db, operator: access.Operator{DB: db.Admin}}
	roles := []string{}
	pools := []*pgxpool.Pool{}
	t.Cleanup(func() {
		for _, p := range pools {
			p.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, role := range roles {
			if _, err := db.Admin.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Error(err)
			}
			if _, err := db.Admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Error(err)
			}
		}
	})
	for i := 0; i < 2; i++ {
		suffix := strings.ReplaceAll(ledger.ID(), "-", "")
		role := "document_role_" + suffix
		if _, err := db.Admin.Exec(db.Context, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+suffix+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
			t.Fatal(err)
		}
		roles = append(roles, role)
		config := db.Runtime.Config().Copy()
		config.ConnConfig.User, config.ConnConfig.Password = role, suffix
		pool, err := pgxpool.NewWithConfig(db.Context, config)
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, pool)
	}
	if err := f.operator.ConfigureRoles(db.Context, db.Role, roles[0], roles[1]); err != nil {
		t.Fatal(err)
	}
	f.auth, f.owners = pools[0], pools[1]
	var err error
	f.sessions, err = access.New(pools[0], pools[1], configuration().Origin)
	if err != nil {
		t.Fatal(err)
	}
	f.identity = access.Identity{Issuer: "https://identity.example.com", Subject: "example", AuthenticatedAt: time.Now().UTC()}
	f.tenant, err = f.operator.BindOwner(db.Context, "", f.identity)
	if err != nil {
		t.Fatal(err)
	}
	f.session, err = f.sessions.IssueSession(db.Context, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.sessions.WithOwner(db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		f.book, err = owner.CreateBook(db.Context, "Example Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.dir = t.TempDir()
	if err = os.Chmod(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	keyJSON, err := documents.NewKeyFile(f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	human, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	f.vault = &documents.Vault{Storage: disk, Keys: keys}
	f.server, err = New(configuration(), Options{Database: db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: human}, Documents: f.vault})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *documentServerFixture) token(t *testing.T, permission string) string {
	t.Helper()
	ctx := f.db.Context
	c, err := f.sessions.RegisterClient(ctx, "Example agent", []string{"http://127.0.0.1:53921/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := access.Random()
	req, err := f.sessions.StartAuthorization(ctx, access.Authorization{ClientID: c.ID, RedirectURI: c.Redirects[0], Resource: f.sessions.Resource(), Challenge: access.Challenge(verifier), State: "example-state", Access: permission})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := f.sessions.ConsentAuthorization(ctx, f.session, req, f.tenant, f.book.ID, permission, "", false)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(destination)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := f.sessions.Exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ID}, "resource": {f.sessions.Resource()}, "redirect_uri": {c.Redirects[0]}, "code": {u.Query().Get("code")}, "code_verifier": {verifier}})
	if err != nil {
		t.Fatal(err)
	}
	return tokens.Access
}

type documentRoundTripper func(*http.Request) (*http.Response, error)

func (fn documentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func (f *documentServerFixture) client(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := &http.Client{Transport: documentRoundTripper(func(input *http.Request) (*http.Response, error) {
		r := input.Clone(input.Context())
		path := *r.URL
		path.Scheme, path.Host = "", ""
		r.URL = &path
		r.Host, r.RemoteAddr = "ledger.example.com", "127.0.0.1:12345"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", r.Host)
		r.Header.Set("X-Forwarded-For", "192.0.2.10")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.server.Handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "example-client", Version: "1.0.0"}, nil).Connect(f.db.Context, &mcp.StreamableClientTransport{Endpoint: f.sessions.Resource(), HTTPClient: client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func documentTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, name string, input any, output any) {
	t.Helper()
	r, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil || r.IsError || len(r.Content) != 1 {
		t.Fatal("document tool failed", name, err, r)
	}
	if inspection, ok := output.(*ledger.DocumentInspection); ok {
		*inspection = ledger.DocumentInspection{}
	}
	text, ok := r.Content[0].(*mcp.TextContent)
	if !ok || json.Unmarshal([]byte(text.Text), output) != nil {
		t.Fatal("invalid document tool result")
	}
}

func (f *documentServerFixture) bytes(method, target string, data []byte, change func(*http.Request)) *httptest.ResponseRecorder {
	u, _ := url.Parse(target)
	r := request()
	r.Method, r.URL.Path = method, u.Path
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Type", "application/octet-stream")
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	f.server.Handler.ServeHTTP(w, r)
	return w
}

func TestDocumentMCPStreamsVerifiedOriginalAndRecoversLostReply(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	client := f.client(t, f.token(t, "write"))
	catalog, err := client.ListTools(ctx, nil)
	if err != nil || len(catalog.Tools) != 49 {
		t.Fatal("document tool catalog missing", err)
	}
	data := bytes.Repeat([]byte("synthetic original\n"), 300000)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	documentTool(t, ctx, client, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, &intake)
	if intake.Document.State != "pending" || intake.UploadID == "" {
		t.Fatal("preparation reported completion")
	}
	var inspection ledger.DocumentInspection
	q := ledger.DocumentQuery{DocumentID: intake.Document.ID, UploadID: intake.UploadID}
	documentTool(t, ctx, client, "get_document", q, &inspection)
	if inspection.UploadURL == "" || inspection.DownloadURL != "" || inspection.Upload.Status != "pending" {
		t.Fatal("wrong byte authority")
	}
	if w := f.bytes("PUT", inspection.UploadURL, data[:len(data)-1], nil); w.Code != 400 {
		t.Fatal("wrong length accepted", w.Code)
	}
	if w := f.bytes("PUT", inspection.UploadURL, data, nil); w.Code != 403 {
		t.Fatal("single-use capability replayed", w.Code)
	}
	documentTool(t, ctx, client, "get_document", q, &inspection)
	completed := f.bytes("PUT", inspection.UploadURL, data, nil)
	if completed.Code != 201 {
		t.Fatal("verified upload failed", completed.Code, completed.Body.String())
	}
	// The client discards that reply and resolves the same logical intake.
	documentTool(t, ctx, client, "get_document", q, &inspection)
	if inspection.Upload.Status != "recorded" || !bytes.Equal(inspection.Upload.Result, completed.Body.Bytes()) || inspection.UploadURL != "" || inspection.DownloadURL == "" {
		t.Fatal("lost completion not recoverable")
	}
	w := f.bytes("GET", inspection.DownloadURL, nil, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("download changed original", w.Code)
	}
	for key, want := range map[string]string{"Content-Type": "application/octet-stream", "Content-Disposition": `attachment; filename="document"`, "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Document-SHA256": intake.Document.SHA256} {
		if w.Header().Get(key) != want {
			t.Fatal("unsafe download header", key)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("missing document sandbox policy")
	}
	if w := f.bytes("GET", inspection.DownloadURL, nil, nil); w.Code != 403 {
		t.Fatal("download capability replayed")
	}
	reader := f.client(t, f.token(t, "read"))
	readCatalog, err := reader.ListTools(ctx, nil)
	if err != nil || len(readCatalog.Tools) != 25 {
		t.Fatal("wrong read catalog", err)
	}
	for _, tool := range readCatalog.Tools {
		if ledger.Writing(tool.Name) {
			t.Fatal("reader exposed document mutation", tool.Name)
		}
	}
	documentTool(t, ctx, reader, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID}, &inspection)
	if w := f.bytes("GET", inspection.DownloadURL, nil, nil); w.Code != 200 {
		t.Fatal("reader could not download")
	}
	if result, err := reader.CallTool(ctx, &mcp.CallToolParams{Name: "add_document", Arguments: map[string]any{}}); err == nil && !result.IsError {
		t.Fatal("reader could prepare upload")
	}
}

func TestDocumentCapabilitiesRecheckRevocationAndDenyForeignBook(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	token := f.token(t, "write")
	client := f.client(t, token)
	data := []byte("synthetic original")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	documentTool(t, ctx, client, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, &intake)
	var inspection ledger.DocumentInspection
	documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID, UploadID: intake.UploadID}, &inspection)
	if w := f.bytes("GET", inspection.UploadURL, nil, nil); w.Code != 403 {
		t.Fatal("upload capability authorized read")
	}
	if err := f.operator.RevokeIdentity(ctx, f.identity); err != nil {
		t.Fatal(err)
	}
	if w := f.bytes("PUT", inspection.UploadURL, data, nil); w.Code != 403 || strings.Contains(w.Body.String(), "example.csv") {
		t.Fatal("revoked upload was accepted or leaked filename")
	}
	var count int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.documents WHERE state='ready'").Scan(&count); err != nil || count != 0 {
		t.Fatal("denied stream completed a document", err)
	}
	if files, err := os.ReadDir(f.dir); err != nil || len(files) != 0 {
		t.Fatal("denied stream stored bytes", err)
	}
	// A new tenant with a real credential cannot even mint a byte capability.
	id := access.Identity{Issuer: f.identity.Issuer, Subject: "other", AuthenticatedAt: time.Now().UTC()}
	f.tenant, _ = f.operator.BindOwner(ctx, "", id)
	f.session, _ = f.sessions.IssueSession(ctx, id)
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		f.book, err = owner.CreateBook(ctx, "Other Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreign := f.client(t, f.token(t, "write"))
	if result, err := foreign.CallTool(ctx, &mcp.CallToolParams{Name: "get_document", Arguments: ledger.DocumentQuery{DocumentID: intake.Document.ID}}); err == nil && !result.IsError {
		t.Fatal("foreign Book minted a capability")
	}
}

func TestDocumentCapabilityExpiryCapacityAndIngress(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	token := f.token(t, "write")
	scope, err := f.sessions.ResolveAgent(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	transfers := newDocumentTransfers(configuration().Origin, f.db.Runtime, f.sessions, nil, nil)
	inspection := ledger.DocumentInspection{Document: ledger.Document{ID: ledger.ID(), State: "ready", Size: 1}}
	var target string
	for i := 0; i < 8; i++ {
		body, err := transfers.issue(scope, token, inspection)
		if err != nil {
			t.Fatal(err)
		}
		var result ledger.DocumentInspection
		if json.Unmarshal(body, &result) != nil {
			t.Fatal("invalid capability result")
		}
		target = result.DownloadURL
	}
	if _, err := transfers.issue(scope, token, inspection); err != documents.ErrUnavailable {
		t.Fatal("unbounded outstanding capabilities")
	}
	for key, c := range transfers.capabilities {
		c.expires = time.Now().Add(-time.Second)
		transfers.capabilities[key] = c
	}
	u, _ := url.Parse(target)
	r := request()
	r.URL.Path = u.Path
	w := httptest.NewRecorder()
	transfers.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("expired capability accepted")
	}
	if _, err := transfers.issue(scope, token, inspection); err != nil || len(transfers.capabilities) != 1 {
		t.Fatal("expired capabilities not reclaimed")
	}
	// Exercise ingress with real capabilities from the configured server,
	// rather than invalid secrets that would fail independently of the guard.
	client := f.client(t, token)
	data := []byte("synthetic original")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	documentTool(t, ctx, client, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, &intake)
	documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID, UploadID: intake.UploadID}, &inspection)
	if w := f.bytes("PUT", inspection.UploadURL, data, nil); w.Code != 201 {
		t.Fatal("positive upload control failed")
	}
	documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID}, &inspection)
	if w := f.bytes("GET", inspection.DownloadURL, nil, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("positive download control failed")
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Host = "foreign.example.com" }, func(r *http.Request) { r.Header.Set("Origin", "https://foreign.example.com") }, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, func(r *http.Request) { r.URL.RawQuery = "extra=true" }} {
		documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID}, &inspection)
		if w := f.bytes("GET", inspection.DownloadURL, nil, change); w.Code != 403 {
			t.Fatal("byte transfer bypassed ingress")
		}
		if w := f.bytes("GET", inspection.DownloadURL, nil, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
			t.Fatal("guard consumed or invalidated its otherwise usable capability")
		}
	}
}

func TestDocumentDownloadNeverCompletesBeforeAuthenticatedEOF(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	client := f.client(t, f.token(t, "write"))
	data := bytes.Repeat([]byte("synthetic document\n"), 10000)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	documentTool(t, ctx, client, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}, &intake)
	var inspection ledger.DocumentInspection
	documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID, UploadID: intake.UploadID}, &inspection)
	if w := f.bytes("PUT", inspection.UploadURL, data, nil); w.Code != 201 {
		t.Fatal("upload failed")
	}
	files, err := os.ReadDir(f.dir)
	if err != nil || len(files) != 1 {
		t.Fatal("missing encrypted object", err)
	}
	path := filepath.Join(f.dir, files[0].Name())
	cipher, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cipher[len(cipher)-1] ^= 1
	if err = os.WriteFile(path, cipher, 0600); err != nil {
		t.Fatal(err)
	}
	documentTool(t, ctx, client, "get_document", ledger.DocumentQuery{DocumentID: intake.Document.ID}, &inspection)
	w := f.bytes("GET", inspection.DownloadURL, nil, nil)
	if w.Body.Len() >= len(data) || bytes.Contains(w.Body.Bytes(), []byte(`"error"`)) {
		t.Fatal("corrupt final frame appeared complete or appended an error to document bytes")
	}
	if w.Body.Len() > 0 && (w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename="document"`) {
		t.Fatal("partial download lost safe headers")
	}
}

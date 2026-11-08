package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func ownerUploadHeaders(r *http.Request, origin, session, key, name, media string, data []byte) {
	sum := sha256.Sum256(data)
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+session))
	r.Header.Set("Content-Type", media)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("X-Vult-Document-Name", url.PathEscape(name))
	r.Header.Set("X-Vult-Document-SHA256", hex.EncodeToString(sum[:]))
}

func ownerUploadRequest(session, book, key, name, media string, data []byte) *http.Request {
	r := browserRequest(http.MethodPost, "/api/v1/documents?book_id="+book, session)
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	ownerUploadHeaders(r, configuration().Origin, session, key, name, media, data)
	return r
}

func runOwnerUpload(s *http.Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	return w
}

func decodeOwnerUpload(t *testing.T, w *httptest.ResponseRecorder, status int) ledger.OwnerDocumentUpload {
	t.Helper()
	var got struct {
		SchemaVersion int `json:"schema_version"`
		ledger.OwnerDocumentUpload
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.SchemaVersion != 1 ||
		w.Header().Get("Cache-Control") != "no-store" || !ledger.ValidID(got.Document.ID) ||
		got.Document.State != "ready" || got.Document.ExpiresAt != nil {
		t.Fatalf("invalid owner upload response: %d %s", w.Code, w.Body.String())
	}
	return got.OwnerDocumentUpload
}

func separateDocumentBook(t *testing.T, f *documentServerFixture) string {
	t.Helper()
	tenant, book := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Separate Book','USD',2)", tenant, book); err != nil {
		t.Fatal(err)
	}
	return book
}

func TestBrowserOwnerDocumentUploadValidatesAndReuses(t *testing.T) {
	f, _ := browserDocuments(t)
	foreign := separateDocumentBook(t, f)
	var second ledger.Book
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		second, err = owner.CreateBook(f.db.Context, "Another Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7\nSynthetic original\n")
	name := strings.Repeat("é", 135) + ".pdf"
	newRequest := func() *http.Request {
		return ownerUploadRequest(f.session, f.book.ID, "upload-1", name, "application/pdf", data)
	}
	cases := []struct {
		name string
		edit func(*http.Request)
		want int
	}{
		{"no session", func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, 403},
		{"no CSRF", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403},
		{"foreign Book", func(r *http.Request) { r.URL.RawQuery = "book_id=" + foreign }, 404},
		{"no Book", func(r *http.Request) { r.URL.RawQuery = "" }, 400},
		{"duplicate Book", func(r *http.Request) { r.URL.RawQuery += "&book_id=" + f.book.ID }, 400},
		{"foreign parameter", func(r *http.Request) { r.URL.RawQuery += "&actor_id=" + ledger.ID() }, 400},
		{"duplicate header", func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }, 400},
		{"invalid header encoding", func(r *http.Request) { r.Header.Set("X-Vult-Document-Name", "%QZ") }, 400},
		{"path filename", func(r *http.Request) { r.Header.Set("X-Vult-Document-Name", "directory%2Fexample.pdf") }, 400},
		{"unknown media", func(r *http.Request) { r.Header.Set("Content-Type", "text/html") }, 415},
		{"wrong signature", func(r *http.Request) { r.Header.Set("Content-Type", "image/png") }, 415},
		{"unknown length", func(r *http.Request) { r.ContentLength = -1 }, 400},
		{"empty", func(r *http.Request) { r.ContentLength = 0 }, 400},
		{"oversize", func(r *http.Request) { r.ContentLength = documents.MaxSize + 1 }, 413},
		{"truncated", func(r *http.Request) { r.ContentLength++ }, 400},
		{"excess bytes", func(r *http.Request) { r.ContentLength-- }, 400},
		{"wrong checksum", func(r *http.Request) { r.Header.Set("X-Vult-Document-SHA256", strings.Repeat("0", 64)) }, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRequest()
			c.edit(r)
			if got := runOwnerUpload(f.server, r); got.Code != c.want {
				t.Fatalf("expected %d, got %d %s", c.want, got.Code, got.Body.String())
			}
		})
	}
	var count int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.documents").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected uploads changed the document population", count, err)
	}

	// A Sources upload is authorized by the ordinary live owner session.
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '20 minutes' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	firstResponse := runOwnerUpload(f.server, newRequest())
	first := decodeOwnerUpload(t, firstResponse, http.StatusCreated)
	if first.Reused || first.Document.Name != name {
		t.Fatal("a new upload lost its original name")
	}
	retry := runOwnerUpload(f.server, newRequest())
	if retry.Code != firstResponse.Code || retry.Body.String() != firstResponse.Body.String() {
		t.Fatal("an identical retry changed its retained outcome")
	}
	badRetry := newRequest()
	badRetry.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), len(data))))
	if got := runOwnerUpload(f.server, badRetry); got.Code != http.StatusBadRequest {
		t.Fatal("a saved request bypassed checksum verification", got.Code)
	}
	changed := ownerUploadRequest(f.session, f.book.ID, "upload-1", "changed.pdf", "application/pdf", data)
	if got := runOwnerUpload(f.server, changed); got.Code != http.StatusConflict {
		t.Fatal("a changed request reused an occupied key", got.Code)
	}
	duplicate := decodeOwnerUpload(t, runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, "upload-2", "copy.pdf", "application/pdf", data)), http.StatusOK)
	if !duplicate.Reused || duplicate.Document.ID != first.Document.ID || duplicate.Document.Name != name {
		t.Fatal("deduplication changed original identity or name")
	}
	other := decodeOwnerUpload(t, runOwnerUpload(f.server, ownerUploadRequest(f.session, second.ID, "upload-1", "other.pdf", "application/pdf", data)), http.StatusCreated)
	if other.Document.ID == first.Document.ID {
		t.Fatal("deduplication crossed Books")
	}
	list := browserDocumentGet(f, "/api/v1/documents?book_id="+f.book.ID, nil)
	var page ledger.DocumentPage
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || len(page.Documents) != 1 || page.Documents[0].ID != first.Document.ID {
		t.Fatal("the uploaded original did not appear in Sources")
	}
	saved := browserDocumentGet(f, "/api/v1/documents/"+first.Document.ID+"/content?book_id="+f.book.ID, nil)
	if saved.Code != http.StatusOK || !bytes.Equal(saved.Body.Bytes(), data) {
		t.Fatal("the retrieved original differed from the upload", saved.Code)
	}

	var actor, owner string
	err := f.db.Admin.QueryRow(f.db.Context, `SELECT d.actor_id::text,s.identity_id::text FROM public.documents d
        CROSS JOIN access.sessions s WHERE d.id=$1 AND s.token_hash=$2`, first.Document.ID, access.Hash(f.session)).Scan(&actor, &owner)
	if err != nil || actor != owner {
		t.Fatal("a browser upload lost the owner actor", err)
	}
	err = f.db.Admin.QueryRow(f.db.Context, `SELECT count(*) FROM public.document_uploads u JOIN public.requests r
        ON r.tenant_id=u.tenant_id AND r.book_id=u.book_id AND r.request_key='upload:'||u.id::text AND r.actor_id=u.actor_id
        WHERE u.document_id=$1 AND u.actor_id=$2 AND u.connection_id IS NULL`, first.Document.ID, owner).Scan(&count)
	if err != nil || count != 2 {
		t.Fatal("owner upload receipts were absent or duplicated by a retry", count, err)
	}

	largeCSV := bytes.Repeat([]byte("1,2\n"), int(documents.MaxSize)/4)
	large := decodeOwnerUpload(t, runOwnerUpload(f.server,
		ownerUploadRequest(f.session, f.book.ID, "large", "large.csv", "text/csv", largeCSV)), http.StatusCreated)
	if large.Document.Size != documents.MaxSize {
		t.Fatal("the browser upload lost bytes at its size limit")
	}

	// Two requests racing on one hash share one Book original.
	csv := []byte("category,amount\nExample,125\n")
	replies := make(chan *httptest.ResponseRecorder, 2)
	for _, key := range []string{"race-1", "race-2"} {
		go func() {
			replies <- runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, key, "example.csv", "text/csv", csv))
		}()
	}
	results := map[int]ledger.OwnerDocumentUpload{}
	for range 2 {
		reply := <-replies
		results[reply.Code] = decodeOwnerUpload(t, reply, reply.Code)
	}
	if results[201].Document.ID == "" || results[200].Document.ID != results[201].Document.ID || !results[200].Reused {
		t.Fatal("racing uploads did not share one verified original")
	}
}

func TestOwnerDocumentUploadSurvivesExpiryAndArchive(t *testing.T) {
	f, scope := browserDocuments(t)
	data := []byte("%PDF-1.7\nExample agent original\n")
	agent := originalForBrowser(t, f, scope, "agent-original", data, true)
	expiring := originalForBrowser(t, f, scope, "agent-unused", []byte("%PDF-1.7\nAnother synthetic original\n"), true)
	if agent.ExpiresAt == nil || expiring.ExpiresAt == nil {
		t.Fatal("an agent-only original lost its deadline")
	}
	var ownerID string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", f.tenant).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	runtime, err := f.db.Runtime.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Rollback(context.Background())
	if _, err := runtime.Exec(f.db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
		set_config('vult.owner_session',$3,true),set_config('vult.owner_origin',$4,true),set_config('vult.actor_id',$5,true)`,
		f.tenant, f.book.ID, access.Hash(f.session), configuration().Origin, ownerID); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	if err := runtime.QueryRow(f.db.Context, "SELECT access.owner_document_allowed($1,$2)", f.tenant, f.book.ID).Scan(&allowed); err != nil || allowed {
		t.Fatal("the agent role acquired owner intake authority", err)
	}
	if err := runtime.Rollback(f.db.Context); err != nil {
		t.Fatal(err)
	}
	direct, err := f.owners.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Rollback(context.Background())
	if _, err = direct.Exec(f.db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
        set_config('vult.owner_session',$3,true),set_config('vult.owner_origin',$4,true)`,
		f.tenant, f.book.ID, access.Hash(f.session), configuration().Origin); err != nil {
		t.Fatal(err)
	}
	if _, err = direct.Exec(f.db.Context, "SELECT set_config('vult.actor_id',access.require_owner($1,false)::text,true)", f.tenant); err != nil {
		t.Fatal(err)
	}
	if tag, err := direct.Exec(f.db.Context, "UPDATE public.documents SET expires_at=NULL WHERE id=$1", agent.ID); err != nil || tag.RowsAffected() != 1 {
		t.Fatal("could not exercise the deferred owner retention guard", err)
	}
	if err = direct.Commit(f.db.Context); err == nil {
		t.Fatal("owner role retained a document without a verified upload receipt")
	}
	retained := decodeOwnerUpload(t, runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, "keep", "owner.pdf", "application/pdf", data)), http.StatusOK)
	if !retained.Reused || retained.Document.ID != agent.ID || retained.Document.UsedAt != nil {
		t.Fatal("retaining an unused original invented a new original or source link")
	}
	ageUploadOriginal(t, f, expiring.ID)
	books, err := f.db.Owner.ExpiredDocumentBooks(f.db.Context, 20)
	if err != nil || len(books) != 1 || books[0] != f.book.ID {
		t.Fatal("maintenance did not select the agent-only expired original", books, err)
	}
	if count, err := f.db.Owner.ExpireBookDocuments(f.db.Context, f.book.ID, f.vault.Storage, 100); err != nil || count != 1 {
		t.Fatal("maintenance did not remove exactly the unretained original", count, err)
	}
	var archive bytes.Buffer
	if err = f.db.Owner.WriteArchive(f.db.Context, f.vault, &archive, f.book.ID); err != nil {
		t.Fatal(err)
	}
	destination := testutil.New(t)
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	key, err := documents.NewKeyFile(f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	restored := &documents.Vault{Storage: disk, Keys: keys}
	if err = destination.Owner.RestoreArchive(destination.Context, restored, bytes.NewReader(legacyArchiveClaimingOwnerRetention(t, archive.Bytes())), nil); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an older archive claimed an owner-retained original", err)
	}
	if err = destination.Owner.RestoreArchive(destination.Context, restored, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("retained original did not restore", err)
	}
	var obj documents.Object
	var noExpiry bool
	err = destination.Admin.QueryRow(destination.Context, `SELECT tenant_id::text,book_id::text,id::text,storage_id,key_id,sha256,size_bytes,expires_at IS NULL
        FROM public.documents WHERE id=$1 AND state='ready' AND used_at IS NULL`, agent.ID).Scan(
		&obj.TenantID, &obj.BookID, &obj.DocumentID, &obj.StorageID, &obj.KeyID, &obj.SHA256, &obj.Size, &noExpiry)
	var decoded bytes.Buffer
	if err != nil || !noExpiry || restored.Read(destination.Context, obj, &decoded) != nil || !bytes.Equal(decoded.Bytes(), data) {
		t.Fatal("restore lost the unused original, retention or exact bytes", err)
	}

	// A ready original with no deadline is still valid evidence for an agent.
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{
		RequestKey: "use-owner-original", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example entry",
		Lines:   []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "100"}, {AccountID: b.ID, Side: "credit", AmountMinor: "100"}},
		Sources: []ledger.SourceInput{{DocumentID: agent.ID}},
	})
	brokenData := []byte("%PDF-1.7\nSynthetic missing original\n")
	broken := originalForBrowser(t, f, scope, "broken", brokenData, true)
	var storage string
	if err = f.db.Admin.QueryRow(f.db.Context, "SELECT storage_id FROM public.documents WHERE id=$1", broken.ID).Scan(&storage); err != nil {
		t.Fatal(err)
	}
	if err = f.vault.Storage.Remove(f.db.Context, storage); err != nil {
		t.Fatal(err)
	}
	w := runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, "unavailable", "example.pdf", "application/pdf", brokenData))
	var retainedByOwner bool
	if err = f.db.Admin.QueryRow(f.db.Context, "SELECT expires_at IS NULL FROM public.documents WHERE id=$1", broken.ID).Scan(&retainedByOwner); err != nil ||
		w.Code != http.StatusServiceUnavailable || retainedByOwner {
		t.Fatal("a missing object was reported as an owner-retained original", w.Code, err)
	}
}

func ageUploadOriginal(t *testing.T, f *documentServerFixture, id string) {
	t.Helper()
	tx, err := f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.db.Context, "ALTER TABLE public.documents DISABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.db.Context, "UPDATE public.documents SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.db.Context, "SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE public.documents ENABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.db.Context); err != nil {
		t.Fatal(err)
	}
}

func legacyArchiveClaimingOwnerRetention(t *testing.T, data []byte) []byte {
	t.Helper()
	var rewritten bytes.Buffer
	reader, writer := tar.NewReader(bytes.NewReader(data)), tar.NewWriter(&rewritten)
	var manifestHash [32]byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		switch header.Name {
		case "manifest.json":
			var manifest map[string]json.RawMessage
			var receipts map[string]string
			var snapshot ledger.Snapshot
			if json.Unmarshal(body, &manifest) != nil || json.Unmarshal(manifest["migrations"], &receipts) != nil ||
				json.Unmarshal(manifest["snapshot"], &snapshot) != nil {
				t.Fatal("invalid archive fixture")
			}
			// Keep a valid released prefix and table set around the retained original.
			delete(receipts, "0004_recorded_book.sql")
			manifest["format"] = json.RawMessage(`"vult-archive-v1"`)
			snapshot.Format = "vult-documents-backup-v1"
			for _, table := range []string{"book_revisions", "operation_proposals", "operation_decisions", "operation_consumptions", "book_addresses", "book_identity_revisions"} {
				delete(snapshot.Tables, table)
			}
			manifest["snapshot"], _ = json.Marshal(snapshot)
			manifest["migrations"], _ = json.Marshal(receipts)
			body, _ = json.Marshal(manifest)
			manifestHash = sha256.Sum256(body)
		case "complete.json":
			body, _ = json.Marshal(map[string]string{"format": "vult-archive-complete-v1", "manifest_sha256": hex.EncodeToString(manifestHash[:])})
		}
		header.Size = int64(len(body))
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return rewritten.Bytes()
}

func TestOwnerDocumentUploadExpiredReservationStartsAgainWithNewKey(t *testing.T) {
	f, _ := browserDocuments(t)
	data := []byte("date,amount\n2026-01-01,125\n")
	sum := sha256.Sum256(data)
	q := ledger.AddDocument{RequestKey: "interrupted", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	var actor, lease string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT identity_id::text FROM access.sessions WHERE token_hash=$1", access.Hash(f.session)).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		lease, err = owner.PrepareOwnerDocument(f.db.Context, f.book.ID, actor, q, ledger.Store{Documents: f.vault})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var document string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT document_id::text FROM public.document_uploads WHERE id=$1", lease).Scan(&document); err != nil {
		t.Fatal(err)
	}
	ageUploadOriginal(t, f, document)
	send := func(key string) *httptest.ResponseRecorder {
		return runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, key, q.Name, q.MediaType, data))
	}
	if w := send(q.RequestKey); w.Code != http.StatusGone {
		t.Fatal("expired incomplete upload was not distinguished from a missing Book", w.Code)
	}
	if out := decodeOwnerUpload(t, send("new-attempt"), http.StatusCreated); out.Document.ID != document || out.Reused {
		t.Fatal("a new request failed to revive the reserved original")
	}
}

func TestLocalBrowserOwnerDocumentUploadUsesProtectedBridge(t *testing.T) {
	f, _ := browserDocuments(t)
	config, _ := localBrowserConfig(t, "127.0.0.1:51999")
	sessions, err := access.New(f.auth, f.owners, config.Origin)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.IssueSession(f.db.Context, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	login, err := access.NewHTTP(sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	protected, err := access.NewOwnerDocumentReader(f.db.Context, f.db.Admin, config.Origin)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s, err := New(config, Options{
		Database: f.db.Runtime, Access: &Access{Sessions: sessions, HTTP: login}, Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
		UploadDocument: func(ctx context.Context, token, book string, q ledger.AddDocument, source io.Reader) (ledger.OwnerDocumentUpload, error) {
			calls++
			if token != session || book != f.book.ID {
				t.Fatal("the bridge received substituted authorization")
			}
			return protected.Upload(ctx, token, book, q, f.vault, source)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := separateDocumentBook(t, f)
	for i, book := range []string{foreign, f.book.ID, f.book.ID} {
		key := ledger.ID()
		data := []byte("record,value\n" + key + ",100\n")
		r := httptest.NewRequest(http.MethodPost, config.Origin+"/api/v1/documents?book_id="+book, bytes.NewReader(data))
		r.URL.Scheme, r.URL.Host = "", ""
		r.TLS.HandshakeComplete = true
		r.TLS.ServerName = strings.Split(r.Host, ":")[0]
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
		ownerUploadHeaders(r, config.Origin, session, key, "records.csv", "text/csv", data)
		w := runOwnerUpload(s, r)
		if i == 0 {
			if w.Code != http.StatusNotFound || calls != 0 {
				t.Fatal("foreign file reached protected service", w.Code)
			}
			continue
		}
		decodeOwnerUpload(t, w, http.StatusCreated)
	}
	if calls != 2 {
		t.Fatal("local browser bypassed its upload bridge")
	}
}

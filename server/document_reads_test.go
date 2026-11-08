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
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

func browserDocuments(t *testing.T) (*documentServerFixture, ledger.Scope) {
	t.Helper()
	f := documentServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.db.Store.Documents = f.vault
	h, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: h}, Documents: f.vault,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Example</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
}

func TestLocalBrowserDocumentReadKeepsKeysBehindTheReader(t *testing.T) {
	f, scope := browserDocuments(t)
	data := bytes.Repeat([]byte("example original\n"), 20000)
	doc := originalForBrowser(t, f, scope, "example", data, true)
	c, _ := localBrowserConfig(t, "127.0.0.1:51999")
	sessions, err := access.New(f.auth, f.owners, c.Origin)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.IssueSession(f.db.Context, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(f.db.Context, f.db.Admin, c.Origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		s, err := New(c, Options{Database: f.db.Runtime, Access: &Access{Sessions: sessions, HTTP: h}, Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
			ReadDocument: func(ctx context.Context, raw, book, id string, dst io.Writer) error {
				if raw != session || book != f.book.ID || id != doc.ID {
					t.Fatal("reader received substituted authority or identity")
				}
				if fail {
					_, err := dst.Write(data)
					if err != nil {
						return err
					}
					return documents.ErrIntegrity
				}
				return reader.Download(ctx, raw, book, id, f.vault, dst)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, c.Origin+"/api/v1/documents/"+doc.ID+"/content?book_id="+f.book.ID, nil)
		r.URL.Scheme, r.URL.Host = "", ""
		r.TLS.HandshakeComplete = true
		r.TLS.ServerName = strings.Split(r.Host, ":")[0]
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if !fail && (w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data)) {
			t.Fatal("local reader could not supply verified original", w.Code)
		}
		if fail && (w.Body.Len() >= len(data) || bytes.Contains(w.Body.Bytes(), []byte(`"error"`))) {
			t.Fatal("local unconfirmed read appeared complete")
		}
	}
}

func originalForBrowser(t *testing.T, f *documentServerFixture, scope ledger.Scope, key string, data []byte, ready bool) ledger.Document {
	t.Helper()
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(f.db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: key, Name: key + ".pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid document fixture")
	}
	if ready {
		result, err := f.db.Store.Upload(f.db.Context, scope, intake.UploadID, bytes.NewReader(data))
		if err != nil || json.Unmarshal(result, &intake.Document) != nil {
			t.Fatal("original fixture did not complete", err)
		}
	}
	return intake.Document
}

func browserDocumentGet(f *documentServerFixture, path string, change func(*http.Request)) *httptest.ResponseRecorder {
	r := browserRequest(http.MethodGet, path, f.session)
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	f.server.Handler.ServeHTTP(w, r)
	return w
}

func documentReadState(t *testing.T, f *documentServerFixture) []byte {
	t.Helper()
	var state []byte
	if err := f.db.Admin.QueryRow(f.db.Context, `SELECT jsonb_build_array(
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.books t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.accounts t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.entries t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.lines t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.requests t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.documents t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.source_citations t),
        (SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM public.evidence_links t))`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestBrowserDocumentsReadReadyPagesAndSafeOriginal(t *testing.T) {
	f, scope := browserDocuments(t)
	path := "/api/v1/documents?book_id=" + f.book.ID
	var page struct {
		SchemaVersion int `json:"schema_version"`
		ledger.DocumentPage
	}
	w := browserDocumentGet(f, path, nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.SchemaVersion != 1 || page.Documents == nil || len(page.Documents) != 0 {
		t.Fatal("empty document read unavailable", w.Code)
	}
	originalForBrowser(t, f, scope, "pending", []byte("pending bytes"), false)
	data := []byte("<!doctype html><script>untrusted original</script>")
	doc := originalForBrowser(t, f, scope, "example", data, true)
	second := originalForBrowser(t, f, scope, "second", []byte("another original"), true)
	before := documentReadState(t, f)
	w = browserDocumentGet(f, path+"&limit=1", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Documents) != 1 || page.Documents[0].ID != doc.ID || !page.HasMore {
		t.Fatal("ready-only first page differs", w.Code)
	}
	w = browserDocumentGet(f, path+"&limit=1&after="+strconv.FormatInt(page.NextAfter, 10), nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Documents) != 1 || page.Documents[0].ID != second.ID || page.HasMore {
		t.Fatal("document cursor lost or duplicated a row", w.Code)
	}
	base := "/api/v1/documents/" + doc.ID
	w = browserDocumentGet(f, base+"?book_id="+f.book.ID, nil)
	var inspection struct {
		SchemaVersion int `json:"schema_version"`
		ledger.DocumentInspection
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inspection) != nil || inspection.SchemaVersion != 1 || inspection.Document.ID != doc.ID || inspection.Document.SHA256 != doc.SHA256 || inspection.Upload != nil || inspection.DownloadURL != "" || strings.Contains(w.Body.String(), "storage_id") || strings.Contains(w.Body.String(), "key_id") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("owner metadata exposed byte authority or storage details", w.Code)
	}
	w = browserDocumentGet(f, base+"/content?book_id="+f.book.ID, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("download changed original", w.Code)
	}
	for key, want := range map[string]string{"Content-Type": "application/octet-stream", "Content-Disposition": `attachment; filename="document"`, "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Document-SHA256": doc.SHA256} {
		if w.Header().Get(key) != want {
			t.Fatal("unsafe original header", key)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("original could render active content")
	}
	if !bytes.Equal(before, documentReadState(t, f)) {
		t.Fatal("document reads changed accepted rows or counters")
	}
}

func TestBrowserDocumentReadsRejectForeignScopeAndAuthority(t *testing.T) {
	f, scope := browserDocuments(t)
	doc := originalForBrowser(t, f, scope, "example", []byte("synthetic original"), true)
	var other ledger.Book
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		other, err = owner.CreateBook(f.db.Context, "Another Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreign := f.identity
	foreign.Subject = "another-owner"
	if _, err := f.operator.BindOwner(f.db.Context, "", foreign); err != nil {
		t.Fatal(err)
	}
	foreignSession, err := f.sessions.IssueSession(f.db.Context, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/content", "/entries"} {
		path := "/api/v1/documents/" + doc.ID + suffix + "?book_id=" + f.book.ID
		for _, tc := range []struct {
			name   string
			change func(*http.Request)
			status int
		}{
			{"missing-session", func(r *http.Request) { r.Header.Del("Cookie") }, 401},
			{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, 403},
			{"foreign-session", func(r *http.Request) { r.Header.Set("Cookie", access.SessionCookie+"="+foreignSession) }, 404},
			{"wrong-book", func(r *http.Request) { r.URL.RawQuery = "book_id=" + other.ID }, 404},
			{"duplicate-book", func(r *http.Request) { r.URL.RawQuery += "&book_id=" + f.book.ID }, 400},
			{"upload-authority", func(r *http.Request) { r.URL.RawQuery += "&upload_id=" + ledger.ID() }, 400},
		} {
			t.Run(suffix+"/"+tc.name, func(t *testing.T) {
				w := browserDocumentGet(f, path, tc.change)
				if w.Code != tc.status || strings.Contains(w.Body.String(), doc.Name) {
					t.Fatal("document denial differs", w.Code, tc.status)
				}
			})
		}
	}
	for _, query := range []string{"&limit=0", "&limit=101", "&after=-1", "&unexpected=1"} {
		if w := browserDocumentGet(f, "/api/v1/documents?book_id="+f.book.ID+query, nil); w.Code != 400 {
			t.Fatal("invalid page accepted", w.Code)
		}
	}
	if w := browserDocumentGet(f, "/api/v1/documents/"+doc.ID+"/content?book_id="+f.book.ID, func(r *http.Request) { r.Header.Set("Range", "bytes=0-1") }); w.Code != 400 {
		t.Fatal("unverified range download accepted")
	}
	if err := f.sessions.EndSession(f.db.Context, f.session); err != nil {
		t.Fatal(err)
	}
	if w := browserDocumentGet(f, "/api/v1/documents?book_id="+f.book.ID, nil); w.Code != 401 {
		t.Fatal("revoked session read documents", w.Code)
	}
}

func TestBrowserDocumentUsesPreserveCurrentRelationshipsAndReversalOrder(t *testing.T) {
	f, scope := browserDocuments(t)
	doc := originalForBrowser(t, f, scope, "statement", []byte("example statement"), true)
	other := originalForBrowser(t, f, scope, "other", []byte("another original"), true)
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	one, two := 1, 2
	post := func(key string, sources []ledger.SourceInput) ledger.Entry {
		t.Helper()
		var e ledger.Entry
		if json.Unmarshal(f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example journal", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"}}, Sources: sources}), &e) != nil {
			t.Fatal("invalid entry")
		}
		return e
	}
	original := post("original", []ledger.SourceInput{{DocumentID: doc.ID, Location: ledger.Location{Row: &one}}, {DocumentID: doc.ID, Location: ledger.Location{Row: &two}}})
	var links struct {
		Sources []ledger.SourceCitation `json:"sources"`
	}
	if json.Unmarshal(f.db.Call(t, scope, "get_entry_documents", ledger.EntryDocumentsQuery{EntryID: original.ID}), &links) != nil || len(links.Sources) != 2 {
		t.Fatal("invalid citations")
	}
	f.db.Call(t, scope, "correct_source_citation", ledger.CorrectSourceCitation{RequestKey: "correct", SourceID: links.Sources[0].SourceID, ExpectedRevision: 1, DocumentID: other.ID, Location: ledger.Location{Row: &one}, Reason: "Example correction"})
	for _, role := range []string{"statement", "receipt"} {
		f.db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "attach-" + role, EntryID: original.ID, DocumentID: doc.ID, Role: role, Reason: "Example evidence"})
	}
	var evidence struct {
		Evidence []ledger.EvidenceLink `json:"evidence"`
	}
	if json.Unmarshal(f.db.Call(t, scope, "get_entry_documents", ledger.EntryDocumentsQuery{EntryID: original.ID}), &evidence) != nil {
		t.Fatal("invalid evidence")
	}
	for _, link := range evidence.Evidence {
		if link.Role == "receipt" {
			f.db.Call(t, scope, "detach_evidence", ledger.DetachEvidence{RequestKey: "detach", LinkID: link.LinkID, Reason: "Example removal"})
		}
	}
	active := post("active", []ledger.SourceInput{{DocumentID: doc.ID, Location: ledger.Location{Row: &one}}})
	f.db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: original.ID, AccountingDate: "2026-01-03", Reason: "Example correction"})
	path := "/api/v1/documents/" + doc.ID + "/entries?book_id=" + f.book.ID + "&limit=1"
	after := int64(0)
	revision := ""
	uses := []ledger.DocumentUse{}
	for pageNumber := 0; pageNumber < 5; pageNumber++ {
		url := path + "&after=" + strconv.FormatInt(after, 10)
		if revision != "" {
			url += "&revision=" + revision
		}
		w := browserDocumentGet(f, url, nil)
		var page ledger.DocumentUsePage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.SchemaVersion != 1 || page.Uses == nil {
			t.Fatal("document use page failed", w.Code)
		}
		uses = append(uses, page.Uses...)
		after, revision = page.NextAfter, page.Revision
		if !page.HasMore {
			break
		}
	}
	if len(uses) != 3 || uses[0].EntryID != active.ID || uses[0].Reversed || uses[0].Citation == nil {
		t.Fatal("active use missing or reversed entries sorted first", uses)
	}
	seenCitation, seenEvidence := false, false
	for _, use := range uses[1:] {
		if use.EntryID != original.ID || !use.Reversed {
			t.Fatal("reversed use hidden or mislabelled")
		}
		if use.Citation != nil {
			seenCitation = use.Citation.Revision == 1 && use.Citation.Location.Row != nil && *use.Citation.Location.Row == 2
		}
		if use.Evidence != nil {
			seenEvidence = use.Evidence.Role == "statement"
		}
	}
	if !seenCitation || !seenEvidence {
		t.Fatal("correction, multiplicity or detachment lost")
	}
	w := browserDocumentGet(f, "/api/v1/documents/"+other.ID+"/entries?book_id="+f.book.ID, nil)
	var corrected ledger.DocumentUsePage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &corrected) != nil || len(corrected.Uses) != 1 || corrected.Uses[0].Citation.Revision != 2 {
		t.Fatal("latest corrected source did not move documents")
	}
	f.db.Account(t, scope, "2000", "liability")
	if w := browserDocumentGet(f, path+"&after="+strconv.FormatInt(after, 10)+"&revision="+revision, nil); w.Code != 409 {
		t.Fatal("stale population accepted a bound continuation", w.Code)
	}
}

func TestBrowserOriginalCorruptionCannotCompleteDownload(t *testing.T) {
	for _, size := range []int{100, 200000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			f, scope := browserDocuments(t)
			data := bytes.Repeat([]byte("x"), size)
			doc := originalForBrowser(t, f, scope, "example", data, true)
			files, err := os.ReadDir(f.dir)
			if err != nil || len(files) != 1 {
				t.Fatal("missing fixture", err)
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
			w := browserDocumentGet(f, "/api/v1/documents/"+doc.ID+"/content?book_id="+f.book.ID, nil)
			if w.Code == 200 && (w.Body.Len() >= len(data) || bytes.Contains(w.Body.Bytes(), []byte(`"error"`))) {
				t.Fatal("corrupt original appeared complete or included error text")
			}
		})
	}
}

func TestBrowserDocumentSearchSharesFinderPopulationAndCursor(t *testing.T) {
	f, scope := browserDocuments(t)
	expired := originalForBrowser(t, f, scope, "audit-auditt-expired", []byte("expired synthetic original"), true)
	ageUploadOriginal(t, f, expired.ID)
	first := originalForBrowser(t, f, scope, "audit-first", []byte("first synthetic original"), true)
	originalForBrowser(t, f, scope, "audit-pending", []byte("pending synthetic original"), false)
	originalForBrowser(t, f, scope, "audimal-other", []byte("nonmatching synthetic original"), true)
	permanent := decodeOwnerUpload(t, runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, "keep", "audit-kept.pdf", "application/pdf", []byte(`%PDF-1.7
Synthetic retained original
`))), 201).Document
	// Seed unrelated metadata in bulk so an exact search must work beyond
	// the first ten thousand available originals. No original is opened here.
	tx, err := f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.db.Context, `ALTER TABLE public.documents DISABLE TRIGGER document_guard;
		ALTER TABLE public.documents DISABLE TRIGGER document_completion_receipt`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, `INSERT INTO public.documents
		(tenant_id,book_id,id,sequence,sha256,size_bytes,media_type,original_name,storage_id,key_id,state,actor_id,ready_at,expires_at)
		SELECT $1,$2,gen_random_uuid(),100000+n,encode(sha256(convert_to('noise-'||n,'UTF8')),'hex'),
			80,'application/pdf','Other '||n||'.pdf',encode(sha256(convert_to('storage-'||n,'UTF8')),'hex'),
			'example','ready',$3,clock_timestamp(),clock_timestamp()+interval '1 day'
		FROM generate_series(1,10001) n`, f.tenant, f.book.ID, scope.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, `SET CONSTRAINTS ALL IMMEDIATE;
		ALTER TABLE public.documents ENABLE TRIGGER document_guard;
		ALTER TABLE public.documents ENABLE TRIGGER document_completion_receipt`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.db.Context); err != nil {
		t.Fatal(err)
	}
	list := "/api/v1/documents?book_id=" + f.book.ID
	finder := "/api/v1/search?types=sources&book_id=" + f.book.ID
	read := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := browserDocumentGet(f, path, nil)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d, want %d", w.Code, want)
		}
		return w
	}
	for _, q := range []string{"audit", "auditt"} {
		suffix := "&q=" + url.QueryEscape(q)
		var found ledger.SearchResult
		if err := json.Unmarshal(read(finder+suffix, 200).Body.Bytes(), &found); err != nil {
			t.Fatal(err)
		}
		var seen []string
		cursor := ""
		for i, expected := range []ledger.Document{first, permanent} {
			var page struct {
				SchemaVersion int `json:"schema_version"`
				ledger.DocumentSearchPage
			}
			w := read(list+suffix+"&limit=1"+cursor, 200)
			if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.SchemaVersion != 1 || page.StateRevision == "" || page.Revision == "" ||
				page.Total != found.Sources.Total || page.Total != 2 || page.HasMore != (i == 0) ||
				len(page.Documents) != 1 || page.Documents[0].ID != expected.ID || page.NextAfter != expected.Sequence {
				t.Fatalf("filtered page %d for %q diverged from the finder: %s", i, q, w.Body.String())
			}
			seen = append(seen, page.Documents[0].ID)
			cursor = "&after=" + strconv.FormatInt(page.NextAfter, 10) + "&revision=" + page.Revision
		}
		for _, hit := range found.Sources.Items {
			if hit.ID != seen[0] && hit.ID != seen[1] {
				t.Fatal("finder included a document outside the filtered list")
			}
		}
	}
	var firstPage ledger.DocumentSearchPage
	if err := json.Unmarshal(read(list+"&q=audit&limit=1", 200).Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	continuePage := "&after=" + strconv.FormatInt(firstPage.NextAfter, 10) + "&revision=" + firstPage.Revision
	read(list+"&q=auditt"+continuePage, 409)
	read(list+"&q=audit&after=1", 400)
	ageUploadOriginal(t, f, first.ID)
	read(list+"&q=audit"+continuePage, 409)
	var current ledger.DocumentSearchPage
	if err := json.Unmarshal(read(list+"&q=audit", 200).Body.Bytes(), &current); err != nil ||
		current.Total != 1 || len(current.Documents) != 1 || current.Documents[0].ID != permanent.ID {
		t.Fatal("fresh search did not remove the expired original", err)
	}
	var none ledger.DocumentSearchPage
	if err := json.Unmarshal(read(list+"&q=no-match-zzzz", 200).Body.Bytes(), &none); err != nil ||
		none.Documents == nil || len(none.Documents) != 0 || none.Total != 0 || none.HasMore {
		t.Fatal("the empty result did not have an exact zero total", err)
	}
	for _, path := range []string{list + "&q=audit&q=other", list + "&q=audit&after=-1", list + "&q=%00", list + "&q=" + strings.Repeat("a", 201),
		"/api/v1/documents/" + first.ID + "?book_id=" + f.book.ID + "&q=audit"} {
		read(path, 400)
	}
	read("/api/v1/documents?book_id="+ledger.ID()+"&q=audit", 404)
	for _, tc := range []struct {
		change func(*http.Request)
		status int
	}{
		{func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		{func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, 403},
	} {
		if w := browserDocumentGet(f, list+"&q=audit", tc.change); w.Code != tc.status || strings.Contains(w.Body.String(), first.ID) {
			t.Fatal("unauthorized search returned a document", w.Code)
		}
	}
}

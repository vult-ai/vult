package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestDocumentChecksKeepEveryUploadWhileChecksWait(t *testing.T) {
	f, _ := browserDocuments(t)
	blocked, started := make(chan struct{}), make(chan struct{}, documentCheckCapacity+20)
	var once sync.Once
	release := func() { once.Do(func() { close(blocked) }) }
	t.Cleanup(release)
	h, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: h}, Documents: f.vault,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Example</title>")}},
		DocumentAdded: func(ctx context.Context, _ ledger.AgentTarget, _ OwnerAct, _ AddedDocument) {
			started <- struct{}{}
			select {
			case <-blocked:
			case <-ctx.Done():
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	upload := func(i int) *httptest.ResponseRecorder {
		key := "queue-" + strconv.Itoa(i)
		r := ownerUploadRequest(f.session, f.book.ID, key, key+".pdf", "application/pdf", []byte("%PDF-1.7 "+key))
		return runOwnerUpload(s, r)
	}
	const files = documentCheckCapacity + 20
	for i := 0; i < files; i++ {
		if w := upload(i); w.Code != http.StatusCreated {
			t.Fatal("an added file was refused while checks were busy", i, w.Code, w.Body.String())
		}
	}
	for range documentCheckWorkers {
		waitDocumentCheck(t, started)
	}
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), "SELECT count(*) FROM public.documents WHERE book_id=$1", f.book.ID).Scan(&count); err != nil || count != files {
		t.Fatal("every added file is kept", count, err)
	}
	release()
	for range files - documentCheckWorkers {
		waitDocumentCheck(t, started)
	}
}

// The owner's screen reads the checks Vult ran on each file it added, and
// only what's new since it last looked.
func TestOwnerReadsDocumentChecks(t *testing.T) {
	f, writer := browserDocuments(t)
	book := f.book.ID
	upload := func(key, name string) ledger.Document {
		r := ownerUploadRequest(f.session, book, key, name, "application/pdf", []byte("%PDF-1.7 example "+key))
		return decodeOwnerUpload(t, runOwnerUpload(f.server, r), http.StatusCreated).Document
	}
	read := func(query string) (int, ledger.DocumentCheckPage) {
		w := httptest.NewRecorder()
		f.server.Handler.ServeHTTP(w, browserRequest(http.MethodGet, documentChecksPath+"?book_id="+book+query, f.session))
		var page ledger.DocumentCheckPage
		if w.Code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatal("invalid checks", w.Body.String())
		}
		return w.Code, page
	}
	if code, page := read(""); code != http.StatusOK || len(page.Checks) != 0 || page.Next != 0 {
		t.Fatal("no checks yet", code, page)
	}

	// Vult records them as the owner's Vult; an agent can't.
	vult := func(call func(o ledger.TenantAdmin, actor string) error) {
		t.Helper()
		if err := f.sessions.OwnerAct(f.db.Context, f.session, f.tenant, call); err != nil {
			t.Fatal(err)
		}
	}
	receipt := upload("receipt", "receipt.pdf")
	vult(func(o ledger.TenantAdmin, actor string) error {
		_, err := o.RecordReceiptAsVult(f.db.Context, book, actor, ledger.RecordReceiptDetails{DocumentID: receipt.ID, AmountMinor: "1250", Date: "2026-10-12", Vendor: "Blue Bottle"})
		return err
	})
	menu := upload("menu", "menu.pdf")
	vult(func(o ledger.TenantAdmin, actor string) error {
		return o.WriteDocumentCheck(f.db.Context, book, actor, ledger.DocumentCheck{DocumentID: menu.ID, Check: ledger.CheckDone, Result: ledger.ResultFailed, Reason: "It's a menu, not a receipt."})
	})
	if _, err := f.db.Store.Call(f.db.Context, writer, "record_receipt_details", mustJSON(ledger.RecordReceiptDetails{DocumentID: menu.ID, AmountMinor: "1"})); err == nil {
		t.Fatal("an agent recorded a check")
	}

	code, page := read("")
	if code != http.StatusOK || len(page.Checks) != 5 {
		t.Fatal("the latest of each check per document", code, page)
	}
	last := page.Checks[len(page.Checks)-1]
	if last.DocumentID != menu.ID || last.Check != ledger.CheckDone || last.OK || last.Reason != "It's a menu, not a receipt." || page.Next != last.Seq {
		t.Fatal("the unusable file ends with its reason", last, page.Next)
	}
	if _, mine := read("&document_id=" + receipt.ID); len(mine.Checks) != 4 {
		t.Fatal("one document's checks", mine)
	}
	if _, fresh := read("&after=" + strconv.FormatInt(last.Seq-1, 10)); len(fresh.Checks) != 1 || fresh.Checks[0].DocumentID != menu.ID {
		t.Fatal("only documents with news", fresh)
	}
	for _, bad := range []string{"&after=-1", "&document_id=nope", "&other=1"} {
		if code, _ := read(bad); code == http.StatusOK {
			t.Fatal("a bad query is refused", bad)
		}
	}
	// Another person's session sees nothing of this Book.
	w := httptest.NewRecorder()
	f.server.Handler.ServeHTTP(w, browserRequest(http.MethodGet, documentChecksPath+"?book_id="+book, "not-a-session"))
	if w.Code == http.StatusOK {
		t.Fatal("checks need the owner's session")
	}
}

// A statement or scan the owner adds goes to the checker with its bytes,
// after the upload answers, and the checker records as the owner's Vult.
func TestAddedFilesGoToTheChecker(t *testing.T) {
	f, _ := browserDocuments(t)
	book := f.book.ID
	type handed struct {
		doc AddedDocument
		err error
	}
	got := make(chan handed, 4)
	h, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: h}, Documents: f.vault,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Example</title>")}},
		DocumentAdded: func(ctx context.Context, target ledger.AgentTarget, act OwnerAct, doc AddedDocument) {
			err := act(func(o ledger.TenantAdmin, actor string) error {
				if target.BookID != book {
					return ledger.ErrDenied
				}
				return o.WriteDocumentCheck(ctx, book, actor, ledger.DocumentCheck{DocumentID: doc.ID, Check: ledger.CheckDone, Result: ledger.ResultReview,
					Reason: "No AI is set up to read this file."})
			})
			got <- handed{doc, err}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	upload := func(key, name, media, group string, data []byte) ledger.Document {
		r := ownerUploadRequest(f.session, book, key, name, media, data)
		if group != "" {
			r.Header.Set("X-Vult-Document-Group", group)
		}
		return decodeOwnerUpload(t, runOwnerUpload(f.server, r), http.StatusCreated).Document
	}
	data := []byte("%PDF-1.7 example statement")
	statement := upload("statement", "statement.pdf", "application/pdf", "statement", data)
	select {
	case h := <-got:
		if h.err != nil || h.doc.ID != statement.ID || h.doc.MediaType != "application/pdf" || string(h.doc.Data) != string(data) {
			t.Fatal("the checker gets the file", h.err, h.doc.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the statement never reached the checker")
	}
	w := httptest.NewRecorder()
	f.server.Handler.ServeHTTP(w, browserRequest(http.MethodGet, documentChecksPath+"?book_id="+book+"&document_id="+statement.ID, f.session))
	var page ledger.DocumentCheckPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Checks) != 1 || page.Checks[0].Result != ledger.ResultReview {
		t.Fatal("the checker's result is read back", w.Code, w.Body.String())
	}
	// A bank's CSV export is checked too; company and tax records aren't.
	csv := upload("csv", "statement.csv", "text/csv", "statement", []byte("date,amount\n2026-09-01,1.00\n"))
	select {
	case h := <-got:
		if h.doc.ID != csv.ID || h.doc.Confirmed != nil {
			t.Fatal("the export reaches the checker", h.doc.Name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the export never reached the checker")
	}
	upload("articles", "articles.pdf", "application/pdf", "company", []byte("%PDF-1.7 example articles"))
	select {
	case h := <-got:
		t.Fatal("handed over a file it shouldn't", h.doc.Name)
	case <-time.After(500 * time.Millisecond):
	}

	// The owner confirms what Vult couldn't read, beside the document.
	body, _ := json.Marshal(map[string]any{"book_id": book, "document_id": statement.ID, "fields": map[string]any{"period_start": map[string]any{"value": "2026-09-01"}}})
	w = bankCall(t, f.server, f.session, http.MethodPost, documentReadingPath, string(body))
	if w.Code != http.StatusAccepted {
		t.Fatal("confirmation", w.Code, w.Body.String())
	}
	select {
	case h := <-got:
		if h.doc.ID != statement.ID || !strings.Contains(string(h.doc.Confirmed), "2026-09-01") || string(h.doc.Data) != string(data) {
			t.Fatal("the confirmed fields reach the checker with the document", string(h.doc.Confirmed))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the confirmation never reached the checker")
	}
	body, _ = json.Marshal(map[string]any{"book_id": book, "document_id": ledger.ID(), "fields": map[string]any{}})
	if w := bankCall(t, f.server, f.session, http.MethodPost, documentReadingPath, string(body)); w.Code == http.StatusAccepted {
		t.Fatal("a document that isn't in the Book can't be confirmed")
	}
}

// An agent's proposed reading over MCP goes to Vult's verifier, with a
// store that can read the document; a read-only agent has no such tool.
func TestAgentProposalsGoToVult(t *testing.T) {
	f, _ := browserDocuments(t)
	type proposed struct {
		scope ledger.Scope
		input json.RawMessage
		data  []byte
		err   error
	}
	got := make(chan proposed, 2)
	h, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: h}, Documents: f.vault,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Example</title>")}},
		ProposeReading: func(ctx context.Context, store ledger.Store, scope ledger.Scope, _ string, input json.RawMessage) (json.RawMessage, error) {
			var q struct {
				DocumentID string `json:"document_id"`
			}
			_ = json.Unmarshal(input, &q)
			var data bytes.Buffer
			err := store.Download(ctx, scope, q.DocumentID, &data)
			got <- proposed{scope, input, data.Bytes(), err}
			return json.RawMessage(`{"status":"rejected","reason":"The date 2026-08-01 doesn't appear in the document."}`), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("%PDF-1.7 example statement")
	doc := decodeOwnerUpload(t, runOwnerUpload(f.server, ownerUploadRequest(f.session, f.book.ID, "statement", "statement.pdf", "application/pdf", data)), http.StatusCreated).Document
	writer := f.client(t, f.token(t, "write"))
	result, err := writer.CallTool(f.db.Context, &mcp.CallToolParams{Name: "propose_document_reading", Arguments: map[string]any{
		"document_id": doc.ID, "reading": map[string]any{"period_start": map[string]any{"value": "2026-08-01", "page": 1}}}})
	if err != nil || result.IsError {
		t.Fatal("the proposal reaches Vult", err, result)
	}
	select {
	case p := <-got:
		if p.err != nil || string(p.data) != string(data) || p.scope.Access != "write" || p.scope.BookID != f.book.ID {
			t.Fatal("Vult reads the document in the agent's request", p.err, p.scope)
		}
	default:
		t.Fatal("the proposal never reached Vult")
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "rejected") {
		t.Fatal("the agent hears the verdict", text)
	}
	reader := f.client(t, f.token(t, "read"))
	catalog, err := reader.ListTools(f.db.Context, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "propose_document_reading" {
			t.Fatal("a read-only agent can't propose readings")
		}
	}
}

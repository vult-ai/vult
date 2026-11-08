package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestBrowserJournalReadsKeepCompleteFactsAndScope(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	var c ledger.Connection
	var other ledger.Book
	var otherConnection ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(ctx, f.book.ID, "Example writer", "write")
		if err != nil {
			return err
		}
		other, err = owner.CreateBook(ctx, "Example separate Book", "USD", 2)
		if err != nil {
			return err
		}
		otherConnection, err = owner.CreateConnection(ctx, other.ID, "Example separate writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	var entry ledger.Entry
	q := ledger.PostJournal{RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example journal", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: b.ID, Side: "credit", AmountMinor: "9007199254740993"}}}
	if json.Unmarshal(f.db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid fixture")
	}
	path := "/api/v1/transactions/" + entry.ID
	query := "?" + url.Values{"book_id": {f.book.ID}}.Encode()
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path+query, f.session))
	var got ledger.JournalRead
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Version != 1 || got.SchemaVersion != 1 || got.BookID != f.book.ID || got.Currency != "USD" || got.Entry.ID != entry.ID || len(got.Entry.Lines) != 2 || got.Entry.Lines[0].AmountMinor != "9007199254740993" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("complete journal read differs", w.Code)
	}
	agent := ledger.JournalRecorder{Kind: "agent", Name: c.Name, ConnectionID: c.ID}
	if got.RecordedBy == nil || *got.RecordedBy != agent {
		t.Fatal("journal actor did not resolve to its Book connection")
	}
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		return owner.Revoke(ctx, c.ID)
	}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path+query, f.session))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.RecordedBy == nil || *got.RecordedBy != agent {
		t.Fatal("revocation erased historical actor attribution", w.Code)
	}

	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path+"/documents"+query, f.session))
	var documents ledger.JournalDocumentsRead
	var facts struct {
		Sources  []ledger.SourceCitation `json:"sources"`
		Evidence []ledger.EvidenceLink   `json:"evidence"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &documents) != nil || documents.EntryID != entry.ID || json.Unmarshal(documents.Documents, &facts) != nil || facts.Sources == nil || facts.Evidence == nil {
		t.Fatal("typed empty document relationships unavailable", w.Code)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/transactions/invalid" + query, http.StatusNotFound},
		{"/api/v1/transactions/" + ledger.ID() + query, http.StatusNotFound},
		{path + "/extra" + query, http.StatusNotFound},
		{path + "?book_id=" + other.ID, http.StatusNotFound},
		{path + "/documents?book_id=" + other.ID, http.StatusNotFound},
		{path + query + "&book_id=" + f.book.ID, http.StatusBadRequest},
		{path + query + "&history=true", http.StatusBadRequest},
		{path + "/documents" + query + "&history=%zz", http.StatusBadRequest},
		{path + "/documents" + query + "&history=unknown", http.StatusBadRequest},
		{path + "/documents" + query + "&history=true&history=false", http.StatusBadRequest},
		{path + "/documents" + query + "&source_after=-1", http.StatusBadRequest},
		{path + "/documents" + query + "&limit=101", http.StatusBadRequest},
		{path + "/documents" + query + "&limit=0", http.StatusBadRequest},
		{path + "/documents" + query + "&tenant_id=1", http.StatusBadRequest},
	} {
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, tc.path, f.session))
		if w.Code != tc.status || strings.Contains(w.Body.String(), entry.ID) || strings.Contains(w.Body.String(), "9007199254740993") || strings.Contains(w.Body.String(), c.Name) {
			t.Fatal("rejected journal read leaked facts", w.Code, tc.status)
		}
	}
	for _, suffix := range []string{"", "/documents"} {
		r := browserRequest(http.MethodGet, path+suffix+query, f.session)
		r.Header.Set("Authorization", "Bearer example")
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal("agent bearer reached owner read", w.Code)
		}
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path+suffix+query, ""))
		if w.Code != http.StatusUnauthorized {
			t.Fatal("anonymous read reached journal", w.Code)
		}
	}

	// The accepted history can contain an actor without a local connection.
	// The display name is visible only for this exact authenticated identity.
	var ownerID string
	if err := f.db.Admin.QueryRow(ctx, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", f.tenant).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	own := seedJournalActor(t, f, entry, ownerID, "example-owner")
	unknown := seedJournalActor(t, f, entry, otherConnection.ActorID, "example-unmapped")
	read := func(id string) (ledger.JournalRead, string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/transactions/"+id+query, f.session))
		var data ledger.JournalRead
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Entry.ID != id {
			t.Fatal("recorded history could not be read", w.Code)
		}
		return data, w.Body.String()
	}
	if data, _ := read(own); data.RecordedBy == nil || *data.RecordedBy != (ledger.JournalRecorder{Kind: "owner", Name: "You"}) {
		t.Fatal("the signed-in actor did not get the owner fallback")
	}
	displayName := "Example owner"
	if _, err := f.sessions.SaveUserSettings(ctx, f.session, access.UserSettingsPatch{
		ExpectedRevision: "0", Profile: &access.UserProfilePatch{DisplayName: &displayName},
	}); err != nil {
		t.Fatal(err)
	}
	if data, body := read(own); data.RecordedBy == nil ||
		*data.RecordedBy != (ledger.JournalRecorder{Kind: "owner", Name: displayName}) ||
		strings.Contains(body, "photo_asset") || strings.Contains(body, "avatar_bytes") {
		t.Fatal("the journal did not return only the signed-in owner's display name")
	}
	if data, body := read(unknown); data.RecordedBy != nil || strings.Contains(body, otherConnection.Name) ||
		strings.Contains(body, "recorded_by") || strings.Contains(body, "import_provenance") {
		t.Fatal("an actor without a matching Book connection gained attribution")
	}
}

// Seed synthetic retained history through the trusted fixture administrator.
// Every entry and line still has to agree with its receipt at commit.
func seedJournalActor(t *testing.T, f *documentServerFixture, from ledger.Entry, actor, key string) string {
	t.Helper()
	ctx := f.db.Context
	tx, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)`, f.tenant, f.book.ID); err != nil {
		t.Fatal(err)
	}
	entry := from
	entry.ID, entry.ReceiptID, entry.ActorID = ledger.ID(), ledger.ID(), actor
	entry.Lines = append([]ledger.Line(nil), from.Lines...)
	if err := tx.QueryRow(ctx, `INSERT INTO public.entries
		(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description,measurement_policy)
		SELECT tenant_id,book_id,$4,0,$5,$6,economic_date,accounting_date,description,measurement_policy
		FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=$3
		RETURNING sequence,recorded_at`, f.tenant, f.book.ID, from.ID, entry.ID, entry.ReceiptID, actor).Scan(&entry.Sequence, &entry.RecordedAt); err != nil {
		t.Fatal(err)
	}
	for i, line := range from.Lines {
		entry.Lines[i].ID = ledger.ID()
		if _, err := tx.Exec(ctx, `INSERT INTO public.lines
			(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo,
			native_currency,native_scale,native_amount_minor,native_account_revision,contexts,valuation)
			SELECT tenant_id,book_id,$4,$5,position,account_id,side,amount_minor,memo,
			native_currency,native_scale,native_amount_minor,native_account_revision,contexts,valuation
			FROM public.lines WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
			f.tenant, f.book.ID, line.ID, entry.Lines[i].ID, entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	result, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.requests
		(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
		SELECT tenant_id,book_id,$4,$5,$6,operation,
			convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('request_key',$6::text))::text,'UTF8'),$7
		FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
		f.tenant, f.book.ID, from.ReceiptID, entry.ReceiptID, actor, key, result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return entry.ID
}

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestBrowserActivityCatalogLimitReturnsBoundedResponse(t *testing.T) {
	f, s := browserServer(t)
	tx, err := f.db.Admin.Begin(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)", f.tenant, f.book.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.db.Context, `INSERT INTO public.accounts(tenant_id,book_id,id,class,name)
 SELECT $1,$2,gen_random_uuid(),'asset','Example account' FROM generate_series(1,10001)`, f.tenant, f.book.ID); err != nil {
		t.Fatal("could not prepare bounded synthetic catalog", err)
	}
	if _, err := tx.Exec(f.db.Context, `INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id)
 SELECT tenant_id,book_id,id,1,'',name,$3 FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND revision=0`, f.tenant, f.book.ID, ledger.ID()); err != nil {
		t.Fatal("could not complete synthetic account revisions", err)
	}
	if err := tx.Commit(f.db.Context); err != nil {
		t.Fatal("synthetic catalog did not satisfy account invariants", err)
	}
	for _, path := range []string{
		"/api/v1/state?view=transactions&book_id=" + f.book.ID,
		"/api/v1/state?view=accounts&dashboard_from=2026-01-01&dashboard_to=2026-01-31&book_id=" + f.book.ID,
		"/api/v1/account-activity?as_of=2026-01-31&book_id=" + f.book.ID,
	} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusRequestEntityTooLarge || strings.Contains(w.Body.String(), "Example account") {
			t.Fatal("catalog bound became a server failure or partial catalog", path, w.Code)
		}
	}
}

func TestBrowserActivityListAndSummaryShareFiltersAndRevision(t *testing.T) {
	f, s := browserServer(t)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	list := "/api/v1/state?" + url.Values{"book_id": {f.book.ID}, "view": {"transactions"}, "limit": {"25"}, "offset": {"0"}, "from": {"2026-01-01"}, "to": {"2026-12-31"}, "search": {""}, "kind": {""}, "account": {""}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-12-31"}}.Encode()
	w := get(list)
	var empty ledger.ActivityPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &empty) != nil || empty.Version != 1 || empty.Book.ID != f.book.ID || empty.Transactions == nil || empty.Accounts == nil || empty.Coverage.Count != 0 || empty.Summary.Income != "0" {
		t.Fatal("valid empty Activity state unavailable", w.Code)
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	cash, income := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "4000", "income")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "income", EconomicDate: "2025-12-31", AccountingDate: "2026-01-02", Description: "Example income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: income.ID, Side: "credit", AmountMinor: "9007199254740993"}}})
	w = get(list)
	var page ledger.ActivityPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Coverage.Count != 1 || page.Summary.Income != "9007199254740993" || len(page.Transactions) != 1 || page.Transactions[0].EconomicDate != "2025-12-31" || page.Transactions[0].AccountingDate != "2026-01-02" || page.Transactions[0].AmountRole != "book_debit_total" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Activity state changed exact values or date meanings", w.Code)
	}
	summaryURL := "/api/v1/transactions/summary?" + url.Values{"book_id": {f.book.ID}, "from": {"2026-01-01"}, "to": {"2026-12-31"}, "revision": {page.Revision}}.Encode()
	w = get(summaryURL)
	var sum ledger.ActivitySummary
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &sum) != nil || sum.Revision != page.Revision || sum.Count != 1 || sum.Income != page.Summary.Income || len(sum.Series) != 12 || sum.SourceCoverage != "unknown" {
		t.Fatal("table and summary read different scopes", w.Code)
	}
	for _, path := range []string{
		list + "&tenant_id=" + f.tenant,
		list + "&limit=0",
		list + "&offset=-1",
		list + "&view=accounts",
		summaryURL + "&to=2026-02-31",
		summaryURL + "&account=1000",
		summaryURL + "&unknown=%zz",
		"/api/v1/transactions/summary?book_id=" + f.book.ID + "&revision=short",
	} {
		w = get(path)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), cash.ID) || strings.Contains(w.Body.String(), "9007199254740993") {
			t.Fatal("malformed Activity request returned facts", w.Code)
		}
	}
	for _, path := range []string{list, summaryURL} {
		for _, token := range []bool{false, true} {
			r := browserRequest(http.MethodGet, path, "")
			want := http.StatusUnauthorized
			if token {
				r = browserRequest(http.MethodGet, path, f.session)
				r.Header.Set("Authorization", "Bearer example")
				want = http.StatusForbidden
			}
			w = httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), cash.ID) {
				t.Fatal("non-session principal reached owner Activity", w.Code)
			}
		}
	}
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "second", EconomicDate: "2026-02-01", AccountingDate: "2026-02-01", Description: "Example later income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "1"}, {AccountID: income.ID, Side: "credit", AmountMinor: "1"}}})
	w = get(summaryURL)
	if w.Code != http.StatusConflict {
		t.Fatal("stale summary did not require a matching refresh", w.Code)
	}
	foreignTenant, foreignBook := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example separate Book','USD',2)", foreignTenant, foreignBook); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/state?view=transactions&book_id=" + foreignBook, "/api/v1/transactions/summary?book_id=" + foreignBook} {
		w = get(path)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook) {
			t.Fatal("Activity exposed a Book outside the session", w.Code)
		}
	}
}

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

func TestBrowserSearchAnswersTheOwnersBookLikeActivity(t *testing.T) {
	f, s := browserServer(t)
	get := func(path string, session string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, session))
		return w
	}
	search := func(v url.Values) string { return "/api/v1/search?" + v.Encode() }
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	office, card := f.db.Account(t, scope, "6000", "expense"), f.db.Account(t, scope, "2000", "liability")
	for i, d := range []string{"Example supplies", "Example supplies", "Example software"} {
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "entry-" + string(rune('a'+i)), EconomicDate: "2026-02-0" + string(rune('1'+i)), AccountingDate: "2026-02-0" + string(rune('1'+i)), Description: d,
			Lines: []ledger.PostLine{{AccountID: office.ID, Side: "debit", AmountMinor: "1012", Memo: "Card 0140"}, {AccountID: card.ID, Side: "credit", AmountMinor: "1012"}}})
	}

	w := get(search(url.Values{"book_id": {f.book.ID}, "q": {"supplies"}}), f.session)
	var out ledger.SearchResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Transactions.Total != 2 || out.StateRevision == "" || out.SchemaVersion != 1 || out.BookID != f.book.ID || out.Currency != "USD" || out.Scale != 2 ||
		out.Counterparties.Status != "not_recorded" || out.Transactions.Items[0].AmountRole != "book_debit_total" || out.Transactions.Items[0].AmountMinor != "1012" ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("owner search", w.Code, w.Body.String())
	}
	for _, group := range []string{"transactions", "accounts", "counterparties", "sources"} {
		if !strings.Contains(w.Body.String(), `"`+group+`":{"items":[`) {
			t.Fatal("group missing", group)
		}
	}

	unused := f.db.Account(t, scope, "5000", "expense")
	matches := search(url.Values{"book_id": {f.book.ID}, "q": {"examplq"}, "types": {"accounts"}, "account_ids": {"1"}})
	var ids ledger.SearchResult
	w = get(matches, f.session)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ids) != nil || ids.Accounts.Total != 3 ||
		len(ids.AccountIDs) != 3 || len(ids.Accounts.Items) != 0 || ids.StateRevision == "" {
		t.Fatal("complete account selection lost a typo match", w.Code)
	}
	for _, account := range []ledger.Account{office, card, unused} {
		found := false
		for _, id := range ids.AccountIDs {
			found = found || id == account.ID
		}
		if !found {
			t.Fatal("account selection omitted a matching account")
		}
	}

	// Activity's list and summary filter with the same rules: q, or its older name search.
	for _, key := range []string{"q", "search"} {
		list := get("/api/v1/state?"+url.Values{"book_id": {f.book.ID}, "view": {"transactions"}, key: {"supplies example"}}.Encode(), f.session)
		var page ledger.ActivityPage
		if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Coverage.Count != 2 {
			t.Fatal("Activity list does not share the search population", key, list.Code)
		}
		sum := get("/api/v1/transactions/summary?"+url.Values{"book_id": {f.book.ID}, key: {"supplies example"}, "revision": {page.Revision}}.Encode(), f.session)
		var summary ledger.ActivitySummary
		if sum.Code != http.StatusOK || json.Unmarshal(sum.Body.Bytes(), &summary) != nil || summary.Count != 2 {
			t.Fatal("Activity summary does not share the search population", key, sum.Code)
		}
	}
	if w := get("/api/v1/state?"+url.Values{"book_id": {f.book.ID}, "view": {"transactions"}, "q": {"a"}, "search": {"b"}}.Encode(), f.session); w.Code != http.StatusBadRequest {
		t.Fatal("q and a different search were both accepted", w.Code)
	}

	w = get(search(url.Values{"book_id": {f.book.ID}, "q": {strings.Repeat("x", 201)}}), f.session)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"QUERY_TOO_LONG"`) || !strings.Contains(w.Body.String(), `"characters":200`) {
		t.Fatal("too long", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		search(url.Values{"book_id": {f.book.ID}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "limit": {"0"}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {strings.Repeat("x", 65)}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "limit": {"21"}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "types": {"people"}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "types": {"sources"}, "account_ids": {"1"}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "types": {"accounts"}, "account_ids": {"0"}}),
		search(url.Values{"book_id": {f.book.ID}, "q": {"x"}, "view": {"transactions"}}),
		search(url.Values{"book_id": {"not-a-book"}, "q": {"x"}}),
		"/api/v1/search?book_id=" + f.book.ID + "&q=x&q=y",
	} {
		if w := get(bad, f.session); w.Code != http.StatusBadRequest {
			t.Fatal("accepted", bad, w.Code)
		}
	}
	if w := get(search(url.Values{"book_id": {ledger.ID()}, "q": {"supplies"}}), f.session); w.Code != http.StatusNotFound {
		t.Fatal("a Book the owner doesn't hold", w.Code)
	}
	if w := get(search(url.Values{"book_id": {f.book.ID}, "q": {"supplies"}}), ""); w.Code != http.StatusUnauthorized {
		t.Fatal("no session", w.Code)
	}
	r := browserRequest(http.MethodGet, search(url.Values{"book_id": {f.book.ID}, "q": {"supplies"}}), f.session)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 43))
	if w := httptest.NewRecorder(); func() int { s.Handler.ServeHTTP(w, r); return w.Code }() != http.StatusForbidden {
		t.Fatal("a bearer token searched")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, search(url.Values{"book_id": {f.book.ID}, "q": {"supplies"}}), f.session).WithContext(cancelled))
	if w.Code == http.StatusOK {
		t.Fatal("a cancelled request still searched")
	}
}

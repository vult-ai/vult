package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestBrowserAccountReadsEmptyFullMalformedAndDenied(t *testing.T) {
	f, s := browserServer(t)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	base := "/api/v1/account-activity?" + url.Values{"book_id": {f.book.ID}, "as_of": {"2026-01-31"}}.Encode()
	w := get(base)
	var activity ledger.AccountActivityRead
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &activity) != nil || activity.Version != 1 || activity.Accounts == nil || len(activity.Accounts) != 0 || len(activity.EmptyWeekly) != 52 || activity.SourceCoverage != "unknown" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("valid empty account activity unavailable", w.Code)
	}
	var connection ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	cash, equity := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	codes, _ := json.Marshal([]string{cash.ID})
	detailURL := "/api/v1/account-detail?" + url.Values{"book_id": {f.book.ID}, "codes": {string(codes)}, "from": {"2026-01-01"}, "to": {"2026-01-31"}, "view": {"ledger"}, "limit": {"100"}, "offset": {"0"}}.Encode()
	w = get(detailURL)
	var detail ledger.AccountDetailRead
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Count != 0 || detail.Ledger == nil || detail.Total != "0" || detail.Activity == nil {
		t.Fatal("valid empty account ledger unavailable", w.Code)
	}
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "example", EconomicDate: "2025-12-31", AccountingDate: "2026-01-02", Description: "Example accepted entry", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "9007199254740993"}}})
	hierarchyURL := "/api/v1/account-hierarchy-activity?" + url.Values{"book_id": {f.book.ID}, "as_of": {"2026-01-31"}, "codes": {string(codes)}}.Encode()
	for _, path := range []string{base, hierarchyURL} {
		w = get(path)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &activity) != nil || activity.Accounts[cash.ID] == nil || activity.Accounts[cash.ID].Total != 1 || activity.NativeStatus[cash.ID] != "not_recorded" || activity.HierarchyBasis != "flat_recorded_accounts" {
			t.Fatal("full account activity changed identity or unknown native history", w.Code)
		}
	}
	w = get(detailURL)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Total != "9007199254740993" || detail.Ledger.Closing != detail.Total || len(detail.Ledger.Lines) != 1 || detail.Ledger.Lines[0].EconomicDate != "2025-12-31" || detail.Ledger.Lines[0].Date != "2026-01-02" {
		t.Fatal("exact ledger amount or date roles changed", w.Code)
	}
	for _, path := range []string{base + "&as_of=2026-02-31", base + "&codes=[]", hierarchyURL + "&tenant_id=" + f.tenant, hierarchyURL + "&codes=[]", detailURL + "&offset=-1", detailURL + "&view=unknown", strings.Replace(detailURL, "from=2026-01-01", "from=2026-02-31", 1), "/api/v1/account-detail?book_id=" + f.book.ID + "&codes=null"} {
		w = get(path)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), cash.ID) {
			t.Fatal("malformed account scope returned records", w.Code)
		}
	}
	for _, path := range []string{base, hierarchyURL, detailURL} {
		for _, bearer := range []bool{false, true} {
			r := browserRequest(http.MethodGet, path, "")
			want := http.StatusUnauthorized
			if bearer {
				r = browserRequest(http.MethodGet, path, f.session)
				r.Header.Set("Authorization", "Bearer example")
				want = http.StatusForbidden
			}
			w = httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), cash.ID) {
				t.Fatal("agent or absent session reached owner account read", w.Code)
			}
		}
	}
	foreignTenant, foreignBook := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example separate Book','USD',2)", foreignTenant, foreignBook); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, hierarchyURL, detailURL} {
		w = get(strings.Replace(path, f.book.ID, foreignBook, 1))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook) {
			t.Fatal("foreign Book disclosed in account read", w.Code)
		}
	}
}

func TestBrowserLargeAccountSelectionReadsExactGroups(t *testing.T) {
	f, scope := browserDocuments(t)
	assets := make([]string, 211)
	for i := range assets {
		assets[i] = f.db.Account(t, scope, fmt.Sprintf("1%03d", i), "asset").ID
	}
	equity := f.db.Account(t, scope, "3000", "equity")
	post := func(key string, ids ...string) {
		lines := []ledger.PostLine{{AccountID: equity.ID, Side: "credit", AmountMinor: fmt.Sprint(100 * len(ids))}}
		for _, id := range ids {
			lines = append(lines, ledger.PostLine{AccountID: id, Side: "debit", AmountMinor: "100"})
		}
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-05",
			AccountingDate: "2026-01-05", Description: "Example recorded entry", Lines: lines})
	}
	post("split", assets[0], assets[1])
	post("outside", assets[210])
	body := func(ids []string) string {
		data, _ := json.Marshal(map[string][]string{"codes": ids})
		return string(data)
	}
	read := func(method, path, data string, change func(*http.Request), want int) *httptest.ResponseRecorder {
		t.Helper()
		r := browserRequest(method, path, f.session)
		if method == http.MethodPost {
			r.Body = io.NopCloser(strings.NewReader(data))
			r.Header.Set("Origin", configuration().Origin)
			r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+f.session))
			r.Header.Set("Content-Type", "application/json")
		}
		if change != nil {
			change(r)
		}
		w := runOwnerUpload(f.server, r)
		if w.Code != want {
			t.Fatalf("%s %s: status %d, want %d", method, r.URL.Path, w.Code, want)
		}
		return w
	}
	book := "?book_id=" + f.book.ID
	activity := "/api/v1/account-hierarchy-activity" + book + "&as_of=2026-01-31"
	detail := "/api/v1/account-detail" + book + "&from=2026-01-01&to=2026-01-31&view=group&limit=1"
	state := read(http.MethodGet, "/api/v1/state"+book+"&view=accounts&dashboard_from=2026-01-01&dashboard_to=2026-01-31", "", nil, 200)
	var captured ledger.BrowserState
	if err := json.Unmarshal(state.Body.Bytes(), &captured); err != nil || captured.AccountActivity == nil ||
		len(captured.AccountBalances) != 212 || len(captured.AccountActivity.Accounts) != 212 ||
		captured.AccountActivity.StateRevision != captured.StateRevision || captured.StateRevision == "" {
		t.Fatal("positions and activity do not share the Accounts snapshot", err)
	}
	selection := assets[:200]
	raw, _ := json.Marshal(selection)
	read(http.MethodGet, activity+"&codes="+url.QueryEscape(string(raw)), "", nil, 400)
	w := read(http.MethodPost, activity, body(selection), nil, 200)
	var subset ledger.AccountActivityRead
	if err := json.Unmarshal(w.Body.Bytes(), &subset); err != nil || subset.SchemaVersion != 1 ||
		subset.StateRevision != captured.StateRevision || len(subset.Accounts) != len(selection) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("large exact activity scope did not return confirmed accounts", err)
	}
	groupFound := false
	for _, group := range subset.Groups {
		if len(group.Codes) == len(selection) {
			groupFound = group.Count == 1
		}
	}
	if !groupFound {
		t.Fatal("group included an outside entry or counted a split twice")
	}
	w = read(http.MethodPost, detail, body(assets), nil, 200)
	var page ledger.AccountDetailRead
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.SchemaVersion != 1 || page.Count != 2 ||
		page.Total != "300" || len(page.Entries) != 1 || page.Revision == "" {
		t.Fatal("large group detail lost its exact total, count or paging revision", err)
	}
	more := detail + "&offset=1&revision=" + page.Revision
	read(http.MethodPost, more, body(assets), nil, 200)
	post("later", assets[0])
	read(http.MethodPost, more, body(assets), nil, 409)

	for _, path := range []string{activity, detail} {
		for _, malformed := range []string{`null`, `{}`, `{"codes":null}`, `{"codes":[]}`,
			`{"codes":[null]}`, `{"codes":["bad-id"]}`, body([]string{assets[0], assets[0]}),
			`{"codes":["` + assets[0] + `"],"book_id":"` + f.book.ID + `"}`} {
			read(http.MethodPost, path, malformed, nil, 400)
		}
		read(http.MethodPost, path, strings.Repeat(" ", 400*1024+1), nil, 413)
		read(http.MethodPost, path+"&codes="+url.QueryEscape(string(raw)), body(selection), nil, 400)
		read(http.MethodPost, path, body(selection), func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403)
		read(http.MethodPost, path, body(selection), func(r *http.Request) { r.Header.Set("Origin", "https://other.example.com") }, 403)
		read(http.MethodPost, path, body(selection), func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, 403)
		read(http.MethodPost, path, body(selection), func(r *http.Request) { r.Header.Del("Cookie") }, 401)
		read(http.MethodPost, strings.Replace(path, f.book.ID, ledger.ID(), 1), body(selection), nil, 404)
		read(http.MethodPost, path, body([]string{assets[0], ledger.ID()}), nil, 404)
	}
}

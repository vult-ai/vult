package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestBrowserStateViewsEmptyFullAndScoped(t *testing.T) {
	f, s := browserServer(t)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	for _, view := range []string{"dashboard", "accounts", "settings", "documents", "statements"} {
		q := url.Values{"book_id": {f.book.ID}, "view": {view}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-01-31"}, "account": {""}, "search": {""}, "kind": {""}, "from": {""}, "to": {""}}
		metadata := view != "dashboard" && view != "accounts"
		if metadata {
			q.Set("bootstrap", "1")
		}
		path := "/api/v1/state?" + q.Encode()
		w := get(path)
		var out ledger.BrowserState
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Version != 1 || out.Book.ID != f.book.ID || out.ReadView != view || out.Accounts == nil || out.Transactions == nil || out.SourceCoverage != "unknown" || out.Bootstrap != metadata || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("valid empty scoped state unavailable", view, w.Code)
		}
		if metadata && (out.Position != nil || out.Summary != nil || out.Dashboard != nil || out.AccountBalances != nil) {
			t.Fatal("metadata-only state invented financial measurements", view)
		}
		if !metadata && (out.AccountBalances == nil || out.Position == nil || out.CashStatus != "unavailable" || out.Summary.Cash != nil) {
			t.Fatal("empty state became known zero cash", view)
		}
		for _, suffix := range []string{"&tenant_id=" + f.tenant, "&view=transactions", "&dashboard_to=2026-02-31", "&kind=expense"} {
			w = get(path + suffix)
			if w.Code != http.StatusBadRequest {
				t.Fatal("malformed state request accepted", view, w.Code)
			}
		}
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
			if w.Code != want {
				t.Fatal("non-session principal reached owner state", view, w.Code)
			}
		}
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
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "example", EconomicDate: "2025-12-31", AccountingDate: "2026-01-02", Description: "Example income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: income.ID, Side: "credit", AmountMinor: "9007199254740993"}}})
	for _, view := range []string{"dashboard", "accounts"} {
		w := get("/api/v1/state?" + url.Values{"book_id": {f.book.ID}, "view": {view}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-01-31"}}.Encode())
		var state ledger.BrowserState
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Position.Assets != "9007199254740993" || state.Summary.Income != "9007199254740993" || len(state.AccountBalances) != 2 || state.AccountBalances[0].Code != state.AccountBalances[0].ID || state.HierarchyBasis != "flat_recorded_accounts" {
			t.Fatal("state lost exact values or stable identities", view, w.Code)
		}
	}
	foreignTenant, foreignBook := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example separate Book','USD',2)", foreignTenant, foreignBook); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"accounts", "settings"} {
		w := get("/api/v1/state?" + url.Values{"book_id": {foreignBook}, "view": {view}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-01-31"}}.Encode())
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook) {
			t.Fatal("state disclosed an unauthorized Book", view, w.Code)
		}
	}
	q := url.Values{"book_id": {foreignBook}, "view": {"dashboard"}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-01-31"}, "dashboard_unit": {"month"}}
	if w := get("/api/v1/state?" + q.Encode()); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook) {
		t.Fatal("Dashboard snapshot disclosed an unauthorized Book", w.Code)
	}
}

func TestDashboardSnapshotUsesOwnerScopeAndOnePeriod(t *testing.T) {
	f, s := browserServer(t)
	zone := "UTC"
	if _, err := f.sessions.ReviseBookSettings(f.db.Context, f.session, f.tenant, ledger.ReviseBookSettings{
		BookID: f.book.ID, RequestKey: "calendar", Revision: 0, Name: f.book.Name, AccountingTimezone: &zone,
	}, nil); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"book_id": {f.book.ID}, "view": {"dashboard"}, "dashboard_from": {"2026-01-01"}, "dashboard_to": {"2026-01-31"}, "dashboard_unit": {"month"}}
	base := "/api/v1/state?" + q.Encode()
	get := func(path, session, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := browserRequest(http.MethodGet, path, session)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		s.Handler.ServeHTTP(w, r)
		return w
	}
	w := get(base, f.session, "")
	var empty ledger.BrowserState
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &empty) != nil ||
		empty.SchemaVersion != 1 || empty.DashboardSnapshot == nil || empty.DashboardSnapshot.Cash != nil ||
		empty.DashboardSnapshot.CashStatus != "unavailable" || empty.Dashboard != nil || empty.AccountBalances != nil {
		t.Fatal("empty Dashboard snapshot lost its status or carried duplicate figures", w.Code)
	}
	for _, check := range []struct {
		path, session, bearer string
		status                int
	}{
		{base, "", "", http.StatusUnauthorized},
		{base, f.session, "example", http.StatusForbidden},
		{strings.Replace(base, f.book.ID, ledger.ID(), 1), f.session, "", http.StatusNotFound},
		{strings.Replace(base, "view=dashboard", "view=accounts", 1), f.session, "", http.StatusBadRequest},
		{strings.Replace(base, "dashboard_unit=month", "dashboard_unit=", 1), f.session, "", http.StatusBadRequest},
		{strings.Replace(base, "dashboard_unit=month", "dashboard_unit=fortnight", 1), f.session, "", http.StatusBadRequest},
		{base + "&dashboard_unit=week", f.session, "", http.StatusBadRequest},
		{strings.Replace(strings.Replace(base, "dashboard_from=2026-01-01", "dashboard_from=2020-01-01", 1), "dashboard_unit=month", "dashboard_unit=day", 1), f.session, "", http.StatusRequestEntityTooLarge},
	} {
		if got := get(check.path, check.session, check.bearer); got.Code != check.status {
			t.Fatalf("unexpected Dashboard request status: got %d want %d", got.Code, check.status)
		}
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
	var cash ledger.Account
	if err := json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Role: "cash", Name: "Example cash"}), &cash); err != nil {
		t.Fatal(err)
	}
	income := f.db.Account(t, scope, "4000", "income")
	for _, row := range []struct{ key, date, amount string }{
		{"prior", "2025-01-22", "50"},
		{"selected", "2026-01-23", "9007199254740993"},
		{"later", "2026-02-01", "100"},
	} {
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: row.key, EconomicDate: row.date, AccountingDate: row.date, Description: "Example income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: row.amount}, {AccountID: income.ID, Side: "credit", AmountMinor: row.amount}}})
	}
	w = get(base, f.session, "")
	var state ledger.BrowserState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || len(state.StateRevision) != 64 ||
		state.StateRevision == empty.StateRevision || state.StateKnowledgeCutoff == "" || state.DashboardSnapshot == nil {
		t.Fatal("Dashboard did not capture the updated recorded state", w.Code)
	}
	dashboard := state.DashboardSnapshot
	if dashboard.SchemaVersion != 1 || dashboard.BookID != f.book.ID || dashboard.Period.To != "2026-01-31" ||
		dashboard.Financial.Result != "9007199254740993" || dashboard.PriorYear == nil || dashboard.PriorYear.Result != "50" ||
		dashboard.Cash == nil || *dashboard.Cash != "9007199254741043" || len(dashboard.CashAccounts) != 1 ||
		dashboard.CashAccounts[0].Carrying != *dashboard.Cash || dashboard.Trends.Current[len(dashboard.Trends.Current)-1].Date != dashboard.Period.To {
		t.Fatal("Dashboard lost precision, used another period, or mixed account scope")
	}
}

func TestDashboardUsesTheRecordedReceivableRole(t *testing.T) {
	f, scope := browserDocuments(t)
	account := func(key, name, class, role string) ledger.Account {
		t.Helper()
		var a ledger.Account
		if json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{
			RequestKey: key, Name: name, Class: class, Role: role,
		}), &a) != nil {
			t.Fatal("could not create example account")
		}
		return a
	}
	receivable := account("example-receivable", "Example invoice balance", "asset", "receivable")
	named := account("example-name-only", "Accounts Receivable", "asset", "")
	equity := account("example-counterpart", "Example counterpart", "equity", "")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "example-balance", EconomicDate: "2026-01-02",
		AccountingDate: "2026-01-02", Description: "Example carrying amounts", Lines: []ledger.PostLine{
			{AccountID: receivable.ID, Side: "debit", AmountMinor: "12500"},
			{AccountID: named.ID, Side: "debit", AmountMinor: "500"},
			{AccountID: equity.ID, Side: "credit", AmountMinor: "13000"},
		}})
	read := func(path string) []byte {
		t.Helper()
		w := ownerAPICall(f.server, f.session, http.MethodGet, path, "", false)
		if w.Code != 200 {
			t.Fatal("could not read Dashboard", w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	check := func() {
		t.Helper()
		var projection struct {
			Financial json.RawMessage
		}
		var snapshot ledger.BrowserState
		if json.Unmarshal(read("/api/v1/dashboard/projection?book_id="+f.book.ID+"&from=2026-01-01&to=2026-01-31&view=recognition&unit=day"), &projection) != nil ||
			json.Unmarshal(read("/api/v1/state?book_id="+f.book.ID+"&view=dashboard&dashboard_from=2026-01-01&dashboard_to=2026-01-31&dashboard_unit=day"), &snapshot) != nil ||
			snapshot.DashboardSnapshot == nil {
			t.Fatal("could not parse Dashboard role reads")
		}
		snapshotJSON, _ := json.Marshal(snapshot.DashboardSnapshot.Financial)
		for _, data := range []json.RawMessage{projection.Financial, snapshotJSON} {
			var financial struct {
				Rows []struct {
					Code, Amount string
					Role         *string
				}
			}
			if json.Unmarshal(data, &financial) != nil {
				t.Fatal("could not parse position rows")
			}
			found := map[string]bool{}
			for _, row := range financial.Rows {
				switch row.Code {
				case receivable.ID:
					found[row.Code] = row.Role != nil && *row.Role == "receivable" && row.Amount == "12500"
				case named.ID:
					found[row.Code] = row.Role == nil && row.Amount == "500"
				}
			}
			if !found[receivable.ID] || !found[named.ID] {
				t.Fatal("position did not reflect exact recorded roles", string(data))
			}
		}
	}
	check()
	role := "receivable"
	f.db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "example-label", AccountID: receivable.ID,
		ExpectedRevision: receivable.Revision, Name: "Example renamed invoice balance", Role: &role})
	check()
}

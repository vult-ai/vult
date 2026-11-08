package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/ledger"
)

func TestBrowserCalendarProjectionUsesStoredZoneAndSignedAccountAmounts(t *testing.T) {
	f, s := browserServer(t)
	var connection ledger.Connection
	zone := "America/New_York"
	if _, err := f.sessions.ReviseBookSettings(f.db.Context, f.session, f.tenant, ledger.ReviseBookSettings{BookID: f.book.ID, RequestKey: "calendar", Revision: 0, Name: f.book.Name, AccountingTimezone: &zone}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	cash := f.db.Account(t, scope, "1000", "asset")
	income := f.db.Account(t, scope, "4000", "income")
	for _, row := range []struct{ key, date, debit, credit, amount string }{
		{"sale", "2026-03-08", cash.ID, income.ID, "20"},
		{"refund", "2026-03-08", income.ID, cash.ID, "70"},
		{"following-week", "2026-03-09", cash.ID, income.ID, "100"},
	} {
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: row.key, EconomicDate: row.date, AccountingDate: row.date, Description: "Example recorded activity", Lines: []ledger.PostLine{{AccountID: row.debit, Side: "debit", AmountMinor: row.amount}, {AccountID: row.credit, Side: "credit", AmountMinor: row.amount}}})
	}
	base := "/api/v1/dashboard/projection?" + url.Values{"book_id": {f.book.ID}, "from": {"2026-03-08"}, "to": {"2026-03-09"}, "view": {"recognition"}}.Encode()
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	w := get(base + "&unit=week")
	var out reporting.DashboardProjection
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Contract.Version != 2 || out.Contract.ReportingZone != zone || out.Performance.Unit != "week" || out.Performance.Income != "50" || len(out.Performance.Buckets) != 2 {
		t.Fatal("browser projection did not use the accepted calendar", w.Code)
	}
	first, second := out.Performance.Buckets[0], out.Performance.Buckets[1]
	if first.From != "2026-03-02" || first.To != "2026-03-09" || first.CoveredFrom != "2026-03-08" || first.CoveredTo != "2026-03-09" || first.Income.Total != "-50" || len(first.Income.Accounts) != 1 || first.Income.Accounts[0].AccountID != income.ID || first.Income.Accounts[0].Negative != "-70" || second.Income.Total != "100" {
		t.Fatal("calendar boundary or per-account refund changed")
	}
	for _, suffix := range []string{"&unit=", "&unit=fortnight", "&unit=fiscal_year", "&unit=week&unit=month"} {
		if w := get(base + suffix); w.Code != http.StatusBadRequest {
			t.Fatal("malformed unit accepted", w.Code)
		}
	}
	for _, unit := range []string{"day", "month", "quarter", "year"} {
		w := get(base + "&unit=" + unit)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Performance.Income != "50" {
			t.Fatal("valid unit changed the accepted contribution total", unit, w.Code)
		}
	}
	if w := get(strings.Replace(base, "from=2026-03-08", "from=2020-01-01", 1) + "&unit=day"); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized explicit unit was silently repartitioned", w.Code)
	}
}

func TestBrowserDashboardReadsEmptyFullMalformedAndForeignScopes(t *testing.T) {
	f, s := browserServer(t)
	query := url.Values{"book_id": {f.book.ID}, "from": {"2026-01-01"}, "to": {"2026-01-31"}}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	base := "/api/v1/dashboard"
	for _, mode := range []string{"", "/projection", "/cash-summary"} {
		params := query.Encode()
		if mode == "/projection" {
			params += "&view=recognition"
		}
		w := get(base + mode + "?" + params)
		var result map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["book_id"] != f.book.ID || result["schema_version"] != float64(1) || result["cash_status"] != "unavailable" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("valid empty dashboard became failure or invented cash", mode, w.Code)
		}
		if _, exists := result["cash_minor"]; exists {
			t.Fatal("empty scope asserted a cash balance")
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
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	var cash ledger.Account
	if json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Name: "Example cash", Role: "cash"}), &cash) != nil {
		t.Fatal("invalid cash account fixture")
	}
	income := f.db.Account(t, scope, "4000", "income")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "income", EconomicDate: "2025-12-31", AccountingDate: "2026-01-02", Description: "Example income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "250"}, {AccountID: income.ID, Side: "credit", AmountMinor: "250"}}})
	for _, mode := range []string{"", "/projection", "/cash-summary"} {
		params := query.Encode()
		if mode == "/projection" {
			params += "&view=recognition"
		}
		w := get(base + mode + "?" + params)
		var result map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["currency"] != "USD" || result["cash_minor"] != "250" || result["money_in_minor"] != "250" || result["cash_status"] != "partial" {
			t.Fatal("full dashboard changed recorded values or cash scope", mode, w.Code)
		}
		if mode == "/cash-summary" {
			var cashResult ledger.CashSummaryRead
			if json.Unmarshal(w.Body.Bytes(), &cashResult) != nil || len(cashResult.Accounts) != 1 || cashResult.Accounts[0].Code != cash.ID || cashResult.Accounts[0].NativeStatus != "not_recorded" || cashResult.Accounts[0].NativeMinor != nil {
				t.Fatal("native cash measurement was fabricated")
			}
		}
		for _, suffix := range []string{"&tenant_id=" + f.tenant, "&from=2026-02-31", "&unknown=%zz", "&book_id=" + f.book.ID} {
			w = get(base + mode + "?" + params + suffix)
			if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), cash.ID) {
				t.Fatal("malformed dashboard scope returned records", mode, w.Code)
			}
		}
		for _, token := range []bool{false, true} {
			r := browserRequest(http.MethodGet, base+mode+"?"+params, "")
			want := http.StatusUnauthorized
			if token {
				r = browserRequest(http.MethodGet, base+mode+"?"+params, f.session)
				r.Header.Set("Authorization", "Bearer example")
				want = http.StatusForbidden
			}
			w = httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), cash.ID) {
				t.Fatal("non-session principal reached dashboard", mode, w.Code)
			}
		}
	}
	foreignTenant, foreignBook := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example private Book','USD',2)", foreignTenant, foreignBook); err != nil {
		t.Fatal(err)
	}
	query.Set("book_id", foreignBook)
	for _, mode := range []string{"", "/projection", "/cash-summary"} {
		params := query.Encode()
		if mode == "/projection" {
			params += "&view=recognition"
		}
		w := get(base + mode + "?" + params)
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook) {
			t.Fatal("dashboard exposed a Book outside the session", mode, w.Code)
		}
	}
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/ledger"
)

func TestBrowserFigureRoutesExposeOneBookStateAndDetectMixedCards(t *testing.T) {
	f, s := browserServer(t)
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
	if json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Role: "cash", Name: "Example cash"}), &cash) != nil {
		t.Fatal("invalid cash fixture")
	}
	income := f.db.Account(t, scope, "4000", "income")
	post := func(key, date, amount string) ledger.Entry {
		t.Helper()
		var entry ledger.Entry
		if json.Unmarshal(f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: "Example recorded activity", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: amount}, {AccountID: income.ID, Side: "credit", AmountMinor: amount}}}), &entry) != nil {
			t.Fatal("invalid journal fixture")
		}
		return entry
	}
	entry := post("income", "2026-01-20", "100")
	paths := []string{
		"dashboard?from=2026-01-01&to=2026-01-31",
		"dashboard/projection?view=recognition&unit=month&from=2026-01-01&to=2026-01-31",
		"dashboard/cash-summary?from=2026-01-01&to=2026-01-31",
		"financial-reports?report=profit_loss&from=2026-01-01&to=2026-01-31",
		"financial-reports/months?report=profit_loss&from=2026-01-01&to=2026-01-31&months_from=2026-01-01&months_to=2026-01-31",
		"state?view=dashboard&dashboard_from=2026-01-01&dashboard_to=2026-01-31",
		"state?view=dashboard&dashboard_from=2026-01-01&dashboard_to=2026-01-31&dashboard_unit=month",
		"state?view=accounts&dashboard_from=2026-01-01&dashboard_to=2026-01-31",
		"state?view=transactions&from=2026-01-01&to=2026-01-31",
		"transactions/summary?from=2025-01-01&to=2025-12-31&search=not-matched",
		"account-activity?as_of=2026-01-31",
		"account-hierarchy-activity?as_of=2026-01-31",
		"account-detail?from=2026-01-01&to=2026-01-31&codes=" + url.QueryEscape(`["`+cash.ID+`"]`),
		"transaction-postings?ids=" + entry.ID,
		"transactions/" + entry.ID,
	}
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/"+path+separator+"book_id="+f.book.ID, f.session))
		return w
	}
	var expected string
	for _, path := range paths {
		t.Run(strings.Split(path, "?")[0], func(t *testing.T) {
			w := get(path)
			var state ledger.RecordedState
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || len(state.StateRevision) != 64 {
				t.Fatal("figure route omitted its state", path, w.Code)
			}
			if expected == "" {
				expected = state.StateRevision
			}
			if state.StateRevision != expected {
				t.Fatal("period, filter or route changed the shared Book identity")
			}
			if _, err := time.Parse(time.RFC3339Nano, state.StateCapturedAt); err != nil {
				t.Fatal("observation time was not recorded", err)
			}
		})
	}
	old := get(paths[0])
	post("backdated", "2026-01-01", "25")
	current := get(paths[2])
	var before, after map[string]json.RawMessage
	if json.Unmarshal(old.Body.Bytes(), &before) != nil || json.Unmarshal(current.Body.Bytes(), &after) != nil || string(before["state_revision"]) == string(after["state_revision"]) || string(after["cash_minor"]) != `"125"` {
		t.Fatal("different-state cards were indistinguishable")
	}
	r := browserRequest(http.MethodGet, "/api/v1/"+paths[0]+"&book_id="+f.book.ID, f.session)
	r.Header.Set("Authorization", "Bearer example")
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "state_revision") {
		t.Fatal("bearer principal learned owner state")
	}
}

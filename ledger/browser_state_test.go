package ledger_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestOwnerBrowserStateKeepsRecordedPositionsAndNativeUnknowns(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	role := "cash"
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "cash-role", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: "", Name: "Income: Not a parent", Role: &role})
	q := post(cash.ID, equity.ID, "prior")
	q.EconomicDate, q.AccountingDate = "2025-12-31", "2025-12-31"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "1000", "1000"
	db.Call(t, scope, "post_journal", q)
	income, expense := db.Account(t, scope, "4000", "income"), db.Account(t, scope, "5000", "expense")
	q = post(cash.ID, income.ID, "income")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "100", "100"
	db.Call(t, scope, "post_journal", q)
	q = post(expense.ID, cash.ID, "expense")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "25", "25"
	db.Call(t, scope, "post_journal", q)
	foreign := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q = measuredPost(foreign, expense.ID, "rounded", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"accounts", "dashboard"} {
		out, err := owner.BrowserState(db.Context, book.ID, view, "2026-01-01", "2026-01-31")
		if err != nil || out.Version != 1 || out.AsOf != "2026-01-31" || out.ReadView != view || out.HierarchyBasis != "flat_recorded_accounts" || out.SourceCoverage != "unknown" || out.Position == nil || out.Position.Assets != "1075" || out.Position.NetAssets != "1075" || out.Position.Equity != "1000" || out.Position.Accumulated != "75" || out.Summary == nil || out.Summary.Income != "100" || out.Summary.Expenses != "25" || out.Summary.Net != "75" || out.Summary.Cash == nil || *out.Summary.Cash != "1075" || out.Book.FiscalYearStartMonth != nil {
			t.Fatal("state reinterpreted stored class, period, position or calendar", view, err)
		}
		if view == "dashboard" && (out.Dashboard == nil || out.Dashboard.Profit != out.Summary.Net || *out.Dashboard.Cash != *out.Summary.Cash) {
			t.Fatal("Dashboard and state snapshots disagree")
		}
		if view == "accounts" && out.Dashboard != nil {
			t.Fatal("account state calculated an unrequested Dashboard")
		}
		seen := map[string]bool{}
		for _, position := range out.AccountBalances {
			seen[position.ID] = true
			if position.Code != position.ID {
				t.Fatal("permanent identity became an optional GL code")
			}
			if position.ID == cash.ID && (position.Kind != "asset" || position.GLCode != "" || position.NativeStatus != "not_recorded" || position.NativeMinor != nil || position.Currency != "" || position.Scale != nil) {
				t.Fatal("account name became classification or unknown native became zero")
			}
			if position.ID == foreign.ID && (position.NativeStatus != "complete" || position.NativeMinor == nil || *position.NativeMinor != "1" || position.BalanceMinor != "0" || position.Scale == nil || *position.Scale != 0) {
				t.Fatal("zero Book value hid the known native position")
			}
		}
		if len(seen) != 5 {
			t.Fatal("state dropped an unused or zero-Book account")
		}
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("read-only state mutated accepted records", err)
	}
}

func TestOwnerBrowserStateMetadataSkipsFinancialHistory(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "example"))
	pool, counts := traceQueries(t, db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"settings", "documents", "statements"} {
		counts.reads.Store(0)
		counts.stateReads.Store(0)
		out, err := owner.BrowserState(db.Context, book.ID, view, "", "")
		if err != nil || !out.Bootstrap || out.Book.ID != book.ID || len(out.Accounts) != 2 || out.Summary != nil || out.Dashboard != nil || out.Position != nil || out.AccountBalances != nil || out.CashStatus != "not_requested" || counts.reads.Load() != 0 || counts.stateReads.Load() != 1 || counts.locks.Load() != 0 {
			t.Fatal("metadata read loaded, locked or fabricated financial data", view, err)
		}
	}
}

func TestOwnerBrowserStateEmptyForeignAndMalformedScopes(t *testing.T) {
	db, book, _, _, _ := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := owner.BrowserState(db.Context, book.ID, "accounts", "2026-01-01", "2026-01-31")
	if err != nil || out.Position == nil || out.Position.Assets != "0" || out.Summary == nil || out.Summary.Cash != nil || out.CashStatus != "unavailable" || out.SourceCoverage != "unknown" || out.AccountBalances == nil {
		t.Fatal("empty recorded state became confirmed cash or complete history", err)
	}
	_, other, _ := tenantOwner(t, db)
	if value, err := owner.BrowserState(db.Context, other.ID, "accounts", "2026-01-01", "2026-01-31"); !errors.Is(err, ledger.ErrNotFound) || value.Book.ID != "" {
		t.Fatal("state crossed tenant ownership", err)
	}
	for _, fixture := range []struct{ view, from, to string }{{"unsupported", "", ""}, {"accounts", "", ""}, {"accounts", "2026-02-31", "2026-03-01"}, {"accounts", "2026-03-01", "2026-02-01"}, {"settings", "2026-02-31", ""}} {
		if value, err := owner.BrowserState(db.Context, book.ID, fixture.view, fixture.from, fixture.to); !errors.Is(err, ledger.ErrInvalid) || value.Book.ID != "" {
			t.Fatal("malformed state scope returned records", err)
		}
	}
}

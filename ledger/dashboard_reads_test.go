package ledger_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vult-ai/vult/ledger"
)

func TestOwnerDashboardCashUsesTheCapturedKnowledgeCutoff(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "recorded", "1", "1")
	quoteOn(&q, "0.01")
	db.Call(t, scope, "post_journal", q)
	// A disposable fixture models a retained future recording time without
	// changing the system clock or rewriting accepted journal records.
	if _, err := db.Admin.Exec(db.Context, `CREATE FUNCTION public.future_recording() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN NEW.recorded_at=clock_timestamp()+interval '1 day'; RETURN NEW; END $$;
 CREATE TRIGGER future_recording BEFORE INSERT ON public.entries FOR EACH ROW EXECUTE FUNCTION public.future_recording()`); err != nil {
		t.Fatal(err)
	}
	q = measuredPost(cash, equity.ID, "future-recorded", "2", "2")
	quoteOn(&q, "0.01")
	var future ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &future) != nil || !future.RecordedAt.After(time.Now()) {
		t.Fatal("fixture did not retain a future recording time")
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || out.Cash == nil || *out.Cash != "1" || len(out.Accounts) != 1 || out.Accounts[0].CarryingMinor != "1" || out.Accounts[0].NativeMinor == nil || *out.Accounts[0].NativeMinor != "1" || out.MovementCount == nil || *out.MovementCount != 1 {
		t.Fatal("cash subtotal and account rows used different knowledge populations", err)
	}
}

func TestOwnerDashboardReadsShareRecordedDatesAndPermanentIdentities(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	role := "cash"
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "cash-role", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: "", Name: "Example account", Role: &role})
	income := db.Account(t, scope, "4000", "income")
	q := post(cash.ID, equity.ID, "prior")
	q.EconomicDate, q.AccountingDate = "2025-12-31", "2025-12-31"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "10000", "10000"
	db.Call(t, scope, "post_journal", q)
	q = post(cash.ID, income.ID, "recorded-sale")
	q.EconomicDate, q.AccountingDate = "2025-12-31", "2026-01-02"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "9007199254740993", "9007199254740993"
	db.Call(t, scope, "post_journal", q)
	pool, counts := traceQueries(t, db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	view, err := owner.DashboardProjection(db.Context, book.ID, "2026-01-01", "2026-01-31", "day")
	if err != nil || view.Profit != "9007199254740993" || view.Cash == nil || *view.Cash != "9007199254750993" || *view.MoneyIn != "9007199254740993" || *view.MoneyOut != "0" || view.Transactions != 1 || view.Contract.ReportingZone != "" || counts.reads.Load() != 1 || counts.stateReads.Load() != 1 || counts.locks.Load() != 0 {
		t.Fatal("projection changed accounting basis, precision or query scope", err)
	}
	if view.Financial.IncomeRows[0].Code != income.ID {
		t.Fatal("account identity became an editable code")
	}
	cashView, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || cashView.Cash == nil || *cashView.Cash != *view.Cash || len(cashView.Accounts) != 1 || cashView.Accounts[0].ID != cash.ID || cashView.Accounts[0].Code != cash.ID || cashView.Accounts[0].NativeStatus != "not_recorded" || cashView.Accounts[0].NativeMinor != nil || cashView.Accounts[0].Currency != "" || cashView.Accounts[0].Scale != nil {
		t.Fatal("missing native measurement became an asserted zero or Book denomination", err)
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("dashboard reads mutated journal state", err)
	}
	_, other, _ := tenantOwner(t, db)
	if out, err := owner.Dashboard(db.Context, other.ID, "2026-01-01", "2026-01-31"); !errors.Is(err, ledger.ErrNotFound) || out.BookID != "" {
		t.Fatal("dashboard crossed tenant scope", err)
	}
	if out, err := owner.CashSummary(db.Context, other.ID, "2026-01-01", "2026-01-31"); !errors.Is(err, ledger.ErrNotFound) || out.BookID != "" {
		t.Fatal("cash read crossed tenant scope", err)
	}
}

func TestOwnerDashboardCashKeepsNativeZeroAndIncompleteHistoryDistinct(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "rounded-position", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	known, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || known.Cash == nil || *known.Cash != "0" || known.MovementCount == nil || *known.MovementCount != 1 || len(known.Accounts) != 1 || known.Accounts[0].NativeStatus != "complete" || known.Accounts[0].NativeMinor == nil || *known.Accounts[0].NativeMinor != "1" {
		t.Fatal("nonzero native movement disappeared behind a zero Book value", err)
	}
	legacy := measuredAccount(t, db, scope, "1300", "EUR", 2, false)
	db.Call(t, scope, "post_journal", measuredPost(legacy, equity.ID, "incomplete-position", "100", "125"))
	incomplete, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || incomplete.Cash == nil || *incomplete.Cash != "125" || incomplete.Revision == known.Revision {
		t.Fatal("known Book carrying subtotal changed or revision stayed stale", err)
	}
	found := false
	for _, account := range incomplete.Accounts {
		if account.ID == legacy.ID {
			found = true
			if account.NativeStatus != "incomplete" || account.NativeMinor != nil || account.Currency != "EUR" || account.CarryingMinor != "125" {
				t.Fatal("unestablished native opening became a complete native balance")
			}
		}
	}
	if !found {
		t.Fatal("incomplete native account disappeared")
	}
	var changed ledger.Account
	euro := "EUR"
	if json.Unmarshal(db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "new-unit", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: cash.Code, Name: cash.Name, Currency: &euro, Scale: currencyScale(2)}), &changed) != nil {
		t.Fatal("invalid unit revision fixture")
	}
	updated, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || updated.Revision == incomplete.Revision {
		t.Fatal("native unit change was not bound into the snapshot revision", err)
	}
}

func TestOwnerDashboardEmptyCashScopeHasExplicitUnavailableStates(t *testing.T) {
	db, book, _, _, _ := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := owner.DashboardProjection(db.Context, book.ID, "2026-01-01", "2026-01-31", "day")
	if err != nil || view.Transactions != 0 || view.Financial.Income != "0" || view.Cash != nil || view.CashFlow != nil || view.CashStatus != "unavailable" || len(view.Comparison.Current) != 0 {
		t.Fatal("valid empty ledger became a failure or invented cash", err)
	}
	cash, err := owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil || cash.Cash != nil || cash.MovementCount != nil || cash.PositionError == "" || cash.MovementError == "" {
		t.Fatal("missing cash scope became a zero position", err)
	}
	for _, dates := range [][2]string{{"2026-02-31", "2026-03-01"}, {"2026-02-02", "2026-02-01"}, {"", "2026-02-01"}} {
		if got, err := owner.Dashboard(db.Context, book.ID, dates[0], dates[1]); !errors.Is(err, ledger.ErrInvalid) || got.BookID != "" {
			t.Fatal("malformed scope returned financial data", err)
		}
	}
}

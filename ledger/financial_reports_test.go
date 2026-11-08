package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/ledger"
)

type reportQueryKey struct{}
type reportRowTrace struct{ rows atomic.Int64 }

func (trace *reportRowTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, reportQueryKey{}, strings.Contains(q.SQL, "FROM access.report_postings"))
}

func (trace *reportRowTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if selected, _ := ctx.Value(reportQueryKey{}).(bool); selected {
		trace.rows.Add(q.CommandTag.RowsAffected())
	}
}

func TestOwnerFinancialActivityReadsLoadOnlyDisplayedWindows(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	for key, date := range map[string]string{"old": "2020-01-01", "comparison": "2025-02-01", "gap": "2025-08-01", "current": "2026-02-01", "future": "2027-01-01"} {
		q := post(debit.ID, credit.ID, key)
		q.EconomicDate, q.AccountingDate = date, date
		db.Call(t, scope, "post_journal", q)
	}
	config := db.Admin.Config()
	trace := &reportRowTrace{}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []reporting.Kind{reporting.ProfitLoss, reporting.Journal} {
		trace.rows.Store(0)
		req := ledger.FinancialReportRequest{BookID: book.ID, Report: kind, From: "2026-02-01", To: "2026-02-01"}
		if _, err := owner.FinancialReport(db.Context, req, false); err != nil || trace.rows.Load() != 2 {
			t.Fatal("unrelated history consumed the activity population budget", kind, err, trace.rows.Load())
		}
	}
	trace.rows.Store(0)
	req := ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-02-01", To: "2026-02-01", Comparison: "previous_year"}
	if _, err := owner.FinancialReport(db.Context, req, false); err != nil || trace.rows.Load() != 4 {
		t.Fatal("comparison read loaded intervening history or truncated an entry", err, trace.rows.Load())
	}
	trace.rows.Store(0)
	req = ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.TrialBalance, AsOf: "2026-02-01"}
	if _, err := owner.FinancialReport(db.Context, req, false); err != nil || trace.rows.Load() != 8 {
		t.Fatal("position report lost preceding balances", err, trace.rows.Load())
	}
}

func TestOwnerFinancialMonthlyDrilldownsReproduceDisjointAndLaterComparisons(t *testing.T) {
	db, book, scope, cash, _ := setup(t)
	income := db.Account(t, scope, "4000", "income")
	for _, fixture := range []struct{ key, date, amount string }{
		{"earlier-comparison", "2025-01-15", "100"},
		{"outside-both", "2025-06-01", "200"},
		{"current-january", "2026-01-15", "300"},
		{"current-february", "2026-02-15", "400"},
		{"later-gap", "2027-06-01", "500"},
		{"later-comparison", "2028-01-15", "600"},
	} {
		q := post(cash.ID, income.ID, fixture.key)
		q.EconomicDate, q.AccountingDate = fixture.date, fixture.date
		q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = fixture.amount, fixture.amount
		db.Call(t, scope, "post_journal", q)
	}
	pool, counts := traceQueries(t, db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ from, to, anchorIncome string }{
		{"2025-01-01", "2025-01-31", "700"},
		{"2028-01-01", "2028-01-31", "1800"},
	} {
		counts.reads.Store(0)
		counts.stateReads.Store(0)
		req := ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31", Comparison: "explicit", CompareFrom: fixture.from, CompareTo: fixture.to}
		set, err := owner.FinancialReportMonthSet(db.Context, req, "2026-01-01", "2026-12-31")
		if err != nil || len(set.Months) != 12 || counts.reads.Load() != 1 || counts.stateReads.Load() != 1 || set.Base.Reconciliation["income"] != "700" {
			t.Fatal("monthly capture changed base totals or rescanned history", err, counts.reads.Load())
		}
		for i, month := range set.Months {
			if month.Reconciliation["income"] != fixture.anchorIncome {
				t.Fatal("later comparison anchor omitted intervening records")
			}
			parts, err := owner.FinancialContributions(db.Context, ledger.FinancialContributionRequest{Request: month.Request, Digest: month.Digest, RowID: "total:result", ColumnID: "prior", KnowledgeCutoff: month.Scope.KnowledgeCutoff, GeneratedAt: month.Scope.GeneratedAt})
			want := "0"
			if i == 0 {
				want = "300"
			} else if i == 1 {
				want = "400"
			}
			if err != nil || parts.Total != want {
				t.Fatal("unchanged monthly result cannot reproduce its contributions", i, err)
			}
		}
	}
}

func TestOwnerFinancialReportsUseRecordedClassesAmountsAndAccountingDates(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	expense := db.Account(t, scope, "5000", "expense")
	q := post(cash.ID, equity.ID, "position")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "10000", "10000"
	db.Call(t, scope, "post_journal", q)
	q = post(expense.ID, equity.ID, "expense")
	q.EconomicDate, q.AccountingDate = "2025-12-31", "2026-02-01"
	q.Description = "Opening balance transfer"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "125", "125"
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid fixture")
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	req := ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31"}
	out, err := owner.FinancialReport(db.Context, req, true)
	if err != nil || out.SchemaVersion != 1 || out.Book.Currency != "USD" || out.Book.Scale != 2 || out.Reconciliation["expenses"] != "125" || out.Reconciliation["result"] != "-125" || out.Status != "partial" || out.Scope.OpeningBalances != "unknown" || out.Scope.Coverage != "unknown" || out.Scope.FiscalStart != "" || out.ExportCSV == "" {
		t.Fatal("recorded expense or unknown facts changed", err)
	}
	if len(out.Findings) != 1 || !reflect.DeepEqual(out.Findings[0].EntryIDs, []string{entry.ID}) || out.Findings[0].AmountMinor != "125" {
		t.Fatal("unreviewed equity counterpart lacks an attributable finding")
	}
	parts, err := owner.FinancialContributions(db.Context, ledger.FinancialContributionRequest{Request: out.Request, Digest: out.Digest, RowID: "total:result", ColumnID: "current", KnowledgeCutoff: out.Scope.KnowledgeCutoff, GeneratedAt: out.Scope.GeneratedAt})
	if err != nil || parts.Total != "-125" || len(parts.Contributions) != 1 || parts.Contributions[0].AccountID != expense.ID || parts.Contributions[0].Date != "2026-02-01" || parts.Contributions[0].Treatment != "unknown" {
		t.Fatal("recorded contribution scope differs", err)
	}
	req.To = "2026-01-31"
	jan, err := owner.FinancialReport(db.Context, req, false)
	if err != nil || jan.Reconciliation["expenses"] != "0" {
		t.Fatal("economic date silently replaced accounting date", err)
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("reporting mutated ledger state", err)
	}
}

func TestOwnerFinancialReportsKeepLargeAndZeroBookEffects(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	expense := db.Account(t, scope, "5000", "expense")
	q := post(expense.ID, equity.ID, "exact-large")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "9007199254740993", "9007199254740993"
	db.Call(t, scope, "post_journal", q)
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q = measuredPost(cash, expense.ID, "zero-book", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	req := ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31"}
	out, err := owner.FinancialReport(db.Context, req, false)
	if err != nil || out.Reconciliation["expenses"] != "9007199254740993" || out.ExportCSV != "" || !out.ExportAvailable {
		t.Fatal("integer precision or recorded zero changed", err)
	}
	req.Report = reporting.Journal
	journal, err := owner.FinancialReport(db.Context, req, false)
	if err != nil || len(journal.Rows) != 2 {
		t.Fatal("zero Book entry disappeared from journal", err)
	}
	pool, counts := traceQueries(t, db.Admin)
	traced, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	req.Report = reporting.ProfitLoss
	months, err := traced.FinancialReportMonthSet(db.Context, req, "2026-01-01", "2026-12-31")
	if err != nil || len(months.Months) != 12 || counts.reads.Load() != 1 || counts.locks.Load() != 0 {
		t.Fatal("month set rescanned or locked journal history", err, counts.reads.Load(), counts.locks.Load())
	}
}

func TestOwnerFinancialReportReadsDenyForeignAndStaleScopes(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "example"))
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	_, foreign, _ := tenantOwner(t, db)
	req := ledger.FinancialReportRequest{BookID: foreign.ID, Report: reporting.TrialBalance, AsOf: "2026-12-31"}
	if out, err := owner.FinancialReport(db.Context, req, false); !errors.Is(err, ledger.ErrNotFound) || out.Book.ID != "" {
		t.Fatal("foreign report returned data", err)
	}
	req.BookID = book.ID
	out, err := owner.FinancialReport(db.Context, req, false)
	if err != nil {
		t.Fatal(err)
	}
	c := ledger.FinancialContributionRequest{Request: out.Request, Digest: out.Digest, RowID: "total:trial_balance", ColumnID: "debit", KnowledgeCutoff: out.Scope.KnowledgeCutoff, GeneratedAt: out.Scope.GeneratedAt}
	if _, err := owner.FinancialContributions(db.Context, c); err != nil {
		t.Fatal("original population cannot be inspected", err)
	}
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: debit.ID, ExpectedRevision: debit.Revision, Code: "", Name: "Example renamed account"})
	if got, err := owner.FinancialContributions(db.Context, c); !errors.Is(err, ledger.ErrConflict) || got.Contributions != nil {
		t.Fatal("stale catalog inspection silently changed", err)
	}
	for _, change := range []func(*ledger.FinancialReportRequest){
		func(q *ledger.FinancialReportRequest) { q.AsOf = "2026-02-31" },
		func(q *ledger.FinancialReportRequest) { q.Currency = "EUR" },
		func(q *ledger.FinancialReportRequest) { q.Report = "unknown" },
		func(q *ledger.FinancialReportRequest) { q.AccountIDs = []string{debit.ID} },
	} {
		bad := req
		change(&bad)
		if got, err := owner.FinancialReport(db.Context, bad, false); !errors.Is(err, ledger.ErrInvalid) || got.Book.ID != "" {
			t.Fatal("malformed report scope returned facts", err)
		}
	}
}

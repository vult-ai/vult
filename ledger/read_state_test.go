package ledger_test

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/ledger"
)

func figureState(t *testing.T, result any, want string) ledger.RecordedState {
	t.Helper()
	data, err := json.Marshal(result)
	var state ledger.RecordedState
	if err != nil || json.Unmarshal(data, &state) != nil {
		t.Fatal("invalid figure envelope", err)
	}
	decoded, err := hex.DecodeString(state.StateRevision)
	if err != nil || len(decoded) != 32 || want != "" && state.StateRevision != want {
		t.Fatal("figure state is missing or differs from the captured Book", state.StateRevision)
	}
	if _, err := time.Parse(time.RFC3339Nano, state.StateCapturedAt); err != nil {
		t.Fatal("figure observation time missing", err)
	}
	if state.StateKnowledgeCutoff != "" {
		cutoff, err := time.Parse(time.RFC3339Nano, state.StateKnowledgeCutoff)
		captured, _ := time.Parse(time.RFC3339Nano, state.StateCapturedAt)
		if err != nil || cutoff.After(captured) {
			t.Fatal("figure cutoff exceeds its observation", err)
		}
	}
	return state
}

func TestRecordedFigureStateAgreesAcrossPeriodsRoutesAndAgentReads(t *testing.T) {
	db, book, scope, cash, _ := setup(t)
	income := db.Account(t, scope, "4000", "income")
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", post(cash.ID, income.ID, "sale")), &entry) != nil {
		t.Fatal("invalid fixture")
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	first, err := owner.Dashboard(db.Context, book.ID, "2026-01-01", "2026-01-31")
	if err != nil {
		t.Fatal(err)
	}
	want := figureState(t, first, "").StateRevision
	check := func(result any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		figureState(t, result, want)
	}
	check(owner.Dashboard(db.Context, book.ID, "2025-01-01", "2025-12-31"))
	check(owner.DashboardProjection(db.Context, book.ID, "2026-01-01", "2026-01-31", "week"))
	check(owner.CashSummary(db.Context, book.ID, "2026-01-01", "2026-01-31"))
	for _, view := range []string{"dashboard", "accounts", "settings"} {
		state, err := owner.BrowserState(db.Context, book.ID, view, "2026-01-01", "2026-01-31")
		check(state, err)
		if view == "dashboard" {
			figureState(t, state.Dashboard, want)
		}
	}
	check(owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{From: "2026-01-01", To: "2026-01-31"}, Limit: 10}))
	check(owner.ActivitySummary(db.Context, book.ID, ledger.ActivityFilters{Search: "not matched", From: "2025-01-01", To: "2025-12-31"}, ""))
	check(owner.AccountActivity(db.Context, book.ID, "2026-01-31", nil))
	check(owner.AccountDetail(db.Context, book.ID, ledger.AccountDetailQuery{AccountIDs: []string{cash.ID}, From: "2026-01-01", To: "2026-01-31", Limit: 10}))
	check(owner.Journal(db.Context, book.ID, entry.ID))
	check(owner.JournalPostings(db.Context, book.ID, []string{entry.ID}))
	report, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-01-31"}, true)
	check(report, err)
	if !reporting.CheckResultDigest(report.Result) || !strings.Contains(report.ExportCSV, "State revision,"+want) {
		t.Fatal("state metadata changed the financial digest or was absent from export")
	}
	parts, err := owner.FinancialContributions(db.Context, ledger.FinancialContributionRequest{Request: report.Request, Digest: report.Digest, RowID: "total:result", ColumnID: "current", KnowledgeCutoff: report.Scope.KnowledgeCutoff, GeneratedAt: report.Scope.GeneratedAt})
	check(parts, err)
	months, err := owner.FinancialReportMonthSet(db.Context, report.Request, "2026-01-01", "2026-01-31")
	check(months, err)
	figureState(t, months.Base, want)
	for _, month := range months.Months {
		figureState(t, month, want)
	}
	for _, read := range []struct {
		method string
		input  any
	}{
		{"get_balance", ledger.AccountQuery{AccountID: cash.ID}},
		{"get_trial_balance", ledger.TrialQuery{}},
		{"list_journals", ledger.ListQuery{Limit: 10}},
	} {
		figureState(t, json.RawMessage(db.Call(t, scope, read.method, read.input)), want)
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("state observations changed accepted records", err)
	}
}

func TestRecordedFigureStateChangesForBackdatingAndDefinitionsOnlyInItsBook(t *testing.T) {
	db, book, scope, cash, _ := setup(t)
	income := db.Account(t, scope, "4000", "income")
	q := post(cash.ID, income.ID, "later")
	q.EconomicDate, q.AccountingDate = "2026-01-20", "2026-01-20"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "100", "100"
	db.Call(t, scope, "post_journal", q)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	read := func() ledger.DashboardRead {
		t.Helper()
		out, err := owner.Dashboard(db.Context, book.ID, "2026-01-01", "2026-01-31")
		if err != nil {
			t.Fatal(err)
		}
		figureState(t, out, "")
		return out
	}
	first := read()
	second := read()
	if first.StateRevision != second.StateRevision || first.StateCapturedAt == second.StateCapturedAt {
		t.Fatal("refresh time became the Book state")
	}
	q = post(cash.ID, income.ID, "backdated")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "100", "100"
	db.Call(t, scope, "post_journal", q)
	backdated := read()
	if backdated.LastDate != first.LastDate || backdated.StateRevision == first.StateRevision || backdated.Profit != "200" {
		t.Fatal("latest entry date concealed changed earlier figures")
	}
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: income.ID, ExpectedRevision: income.Revision, Code: income.Code, Name: "Example revised account"})
	renamed := read()
	if renamed.StateRevision == backdated.StateRevision || renamed.Profit != backdated.Profit {
		t.Fatal("accepted definitions were absent from the state identity")
	}
	_, foreign, foreignScope := tenantOwner(t, db)
	debit, credit := db.Account(t, foreignScope, "1000", "asset"), db.Account(t, foreignScope, "3000", "equity")
	db.Call(t, foreignScope, "post_journal", post(debit.ID, credit.ID, "foreign"))
	if unchanged := read(); unchanged.StateRevision != renamed.StateRevision {
		t.Fatal("another Book changed this Book's state identity")
	}
	if out, err := owner.Dashboard(db.Context, foreign.ID, "2026-01-01", "2026-01-31"); err == nil || out.StateRevision != "" {
		t.Fatal("foreign Book exposed a state marker")
	}
}

func TestRecordedFigureStateTracksTheWholeBookTemporalFrontier(t *testing.T) {
	db, book, scope, cash, _ := setup(t)
	income := db.Account(t, scope, "4000", "income")
	// A synthetic retained recording crosses the cutoff without another write.
	// Its accounting date is deliberately outside the displayed report period.
	if _, err := db.Admin.Exec(db.Context, `CREATE FUNCTION public.future_recording() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN NEW.recorded_at=clock_timestamp()+interval '5 seconds'; RETURN NEW; END $$;
 CREATE TRIGGER future_recording BEFORE INSERT ON public.entries FOR EACH ROW EXECUTE FUNCTION public.future_recording()`); err != nil {
		t.Fatal(err)
	}
	q := post(cash.ID, income.ID, "future-recording")
	q.EconomicDate, q.AccountingDate = "2025-12-31", "2025-12-31"
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid fixture")
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	request := ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-01-31"}
	before, err := owner.FinancialReport(db.Context, request, false)
	if err != nil {
		t.Fatal(err)
	}
	cutoff, _ := time.Parse(time.RFC3339Nano, before.StateKnowledgeCutoff)
	if !cutoff.Before(entry.RecordedAt) {
		t.Fatal("fixture crossed its cutoff before the first observation")
	}
	var epoch int64
	if err := db.Admin.QueryRow(db.Context, "SELECT write_epoch FROM public.books WHERE tenant_id=$1 AND id=$2", book.TenantID, book.ID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(10 * time.Second)
	t.Cleanup(func() { deadline.Stop() })
	tick := time.NewTicker(25 * time.Millisecond)
	t.Cleanup(tick.Stop)
	for {
		var visible bool
		if err := db.Admin.QueryRow(db.Context, "SELECT clock_timestamp()>$1", entry.RecordedAt).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("retained recording did not cross the real cutoff")
		case <-tick.C:
		case <-db.Context.Done():
			t.Fatal(db.Context.Err())
		}
	}
	after, err := owner.FinancialReport(db.Context, request, false)
	if err != nil || before.StateRevision == after.StateRevision || before.Reconciliation["result"] != "0" || after.Reconciliation["result"] != "0" {
		t.Fatal("route filter concealed changed whole-Book visibility", err)
	}
	var unchanged int64
	if err := db.Admin.QueryRow(db.Context, "SELECT write_epoch FROM public.books WHERE tenant_id=$1 AND id=$2", book.TenantID, book.ID).Scan(&unchanged); err != nil || unchanged != epoch {
		t.Fatal("fixture changed Book state instead of only the cutoff", err)
	}
	old, err := owner.FinancialContributions(db.Context, ledger.FinancialContributionRequest{Request: before.Request, Digest: before.Digest, RowID: "total:result", ColumnID: "current", KnowledgeCutoff: before.Scope.KnowledgeCutoff, GeneratedAt: before.Scope.GeneratedAt})
	if err != nil || old.StateRevision != before.StateRevision {
		t.Fatal("historical drilldown could not retain its original temporal state", err)
	}
}

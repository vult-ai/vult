package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func periodList(t *testing.T, db *testutil.Database, s ledger.Scope) []ledger.AccountingPeriod {
	t.Helper()
	var out struct {
		Periods []ledger.AccountingPeriod `json:"periods"`
	}
	if json.Unmarshal(db.Call(t, s, "list_closed_periods", struct{}{}), &out) != nil {
		t.Fatal("invalid period list")
	}
	return out.Periods
}

func refusedWith(t *testing.T, err error, text string) {
	t.Helper()
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(strings.ToLower(refused.Reason), strings.ToLower(text)) {
		t.Fatalf("expected refusal %q, got %v", text, err)
	}
}

// Close blocks posting; a reopening with a reason allows it; both appear in
// history and on the period's reports.
func TestClosingIsADecisionThatCanBeReopened(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, software := byCode["1110"].ID, byCode["6200"].ID
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	actor, _ := proposalOwner(t, db, s)
	ownerChange := func(method string, args any) (json.RawMessage, error) {
		return owner.AccountChange(db.Context, book.ID, actor, method, value(args))
	}
	// A write agent closes a period with nothing open.
	var feb ledger.AccountingPeriod
	if json.Unmarshal(db.Call(t, s, "close_period", ledger.ClosePeriod{RequestKey: "feb", From: "2026-02-01", Through: "2026-02-28"}), &feb) != nil ||
		feb.Status != "closed" || feb.Revision != 1 || len(feb.Events) != 1 || feb.Events[0].ActorID != s.ActorID {
		t.Fatal("a clean period closes", feb)
	}
	// January has an entry with no evidence or payee: the review lists it.
	entry := journal(t, db, s, "jan", "2026-01-12", software, checking, "2900")
	var review ledger.PeriodReview
	if json.Unmarshal(db.Call(t, s, "get_period_review", ledger.PeriodReviewQuery{From: "2026-01-01", Through: "2026-01-31"}), &review) != nil ||
		review.Clean || review.OpenItems["entries_without_evidence"] != 1 || review.OpenItems["entries_without_payee"] != 1 || len(review.EntriesWithoutPayee) != 1 || review.EntriesWithoutPayee[0].EntryID != entry.ID {
		t.Fatal("the review lists what is open", review)
	}
	// Only the owner closes with open items, and only with a reason.
	refusedWith(t, callError(db, s, "close_period", ledger.ClosePeriod{RequestKey: "jan-agent", From: "2026-01-01", Through: "2026-01-31", Reason: "Good enough"}), "only the owner")
	_, err = ownerChange("close_period", ledger.ClosePeriod{RequestKey: "jan-no-reason", From: "2026-01-01", Through: "2026-01-31"})
	refusedWith(t, err, "needs a reason")
	result, err := ownerChange("close_period", ledger.ClosePeriod{RequestKey: "jan", From: "2026-01-01", Through: "2026-01-31", Reason: "Receipts follow from the accountant"})
	succeeded(t, err)
	var jan ledger.AccountingPeriod
	if json.Unmarshal(result, &jan) != nil || jan.Status != "closed" || jan.Events[0].OpenItems["entries_without_payee"] != 1 || jan.Events[0].ReportSHA256 == "" {
		t.Fatal("the close keeps what was still open and the review it saw", jan)
	}
	// Posting into a closed period is refused.
	if err := tryJournal(db, s, "late", "2026-01-20", software, checking, "100"); err == nil {
		t.Fatal("a closed period accepted an entry")
	}
	// Overlapping periods are refused.
	if err := callError(db, s, "close_period", ledger.ClosePeriod{RequestKey: "overlap", From: "2026-01-15", Through: "2026-02-15"}); err == nil {
		t.Fatal("an overlapping period was closed")
	}
	// Agents never reopen; they propose it for the owner.
	refusedWith(t, callError(db, s, "reopen_period", ledger.ReopenPeriod{RequestKey: "reopen-agent", PeriodID: jan.ID, ExpectedRevision: 1, Reason: "Fix a date"}), "only the owner")
	proposer := connectionScope(t, db, book.ID, "propose")
	p := prepareOperation(t, db, proposer, "propose-reopen", "reopen_period", ledger.ReopenPeriod{RequestKey: "reopen", PeriodID: jan.ID, ExpectedRevision: 1, Reason: "A January receipt was posted to the wrong account"})
	if p.Preview.PeriodChange == nil || p.Preview.PeriodChange.Change != "reopen" || p.Preview.PeriodChange.Reason == "" {
		t.Fatal("a reopen proposal previews the decision", p.Preview)
	}
	if periodList(t, db, s)[0].Status != "closed" {
		t.Fatal("preparing a reopening recorded it")
	}
	// The owner reopens, with the reason shown on the period's reports.
	_, err = ownerChange("reopen_period", ledger.ReopenPeriod{RequestKey: "reopen", PeriodID: jan.ID, ExpectedRevision: 1, Reason: "A January receipt was posted to the wrong account"})
	succeeded(t, err)
	periods := periodList(t, db, s)
	if len(periods) != 2 || periods[0].ID != jan.ID || periods[0].Status != "reopened" || len(periods[0].Events) != 2 || periods[0].Events[1].Reason == "" {
		t.Fatal("the reopening shows in the history", periods)
	}
	journal(t, db, s, "late", "2026-01-20", software, checking, "100")
	// Reports of the period show the reopening and the close with open items.
	report, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-01-31"}, true)
	succeeded(t, err)
	kinds := map[string]bool{}
	for _, n := range report.PeriodNotes {
		kinds[n.Kind] = n.Reason != ""
	}
	if !kinds["reopened"] || !kinds["closed_with_open_items"] {
		t.Fatal("a period's reports show its reopening and its close with open items", report.PeriodNotes)
	}
	for _, want := range []string{"Period note,2026-01-01,2026-01-31,Reopened,A January receipt was posted to the wrong account",
		"Period note,2026-01-01,2026-01-31,Closed with open items,Receipts follow from the accountant"} {
		if !strings.Contains(report.ExportCSV, want+"\n") {
			t.Fatal("the exported report carries the period's notes", report.ExportCSV)
		}
	}
	// Closing it again appends to the same history.
	result, err = ownerChange("close_period", ledger.ClosePeriod{RequestKey: "jan-again", From: "2026-01-01", Through: "2026-01-31", Reason: "Corrected"})
	succeeded(t, err)
	if json.Unmarshal(result, &jan) != nil || jan.Revision != 3 || jan.Status != "closed" {
		t.Fatal("a reopened period closes again in its own history", jan)
	}
	// A year close follows the monthly closes: it contains them, records its
	// own period and posts nothing. A range that only partly overlaps a
	// closed period is refused.
	before := len(listJournals(t, db, s))
	refusedWith(t, callError(db, s, "close_period", ledger.ClosePeriod{RequestKey: "straddle", From: "2026-01-15", Through: "2026-03-31"}), "overlaps")
	result, err = ownerChange("close_period", ledger.ClosePeriod{RequestKey: "year", From: "2026-01-01", Through: "2026-12-31", Reason: "Year end; January's receipts follow"})
	succeeded(t, err)
	var year ledger.AccountingPeriod
	if json.Unmarshal(result, &year) != nil || year.Status != "closed" || year.From != "2026-01-01" || year.Through != "2026-12-31" {
		t.Fatal("a year closes after its monthly closes", year)
	}
	if len(listJournals(t, db, s)) != before {
		t.Fatal("a close posted entries")
	}
	refusedWith(t, callError(db, s, "close_period", ledger.ClosePeriod{RequestKey: "inside", From: "2026-06-01", Through: "2026-06-30"}), "overlaps")
	// Reopening a month inside a closed year doesn't open its dates.
	_, err = ownerChange("reopen_period", ledger.ReopenPeriod{RequestKey: "reopen-jan", PeriodID: jan.ID, ExpectedRevision: 3, Reason: "Example correction"})
	succeeded(t, err)
	if err := tryJournal(db, s, "inside-year", "2026-01-21", software, checking, "100"); err == nil {
		t.Fatal("a date inside a closed year accepted an entry")
	}
}

// Native-authorized closes keep the same accountable decision as app closes.
func TestNativeCloseKeepsOwnerReasonAndReview(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	actor, _ := proposalOwner(t, db, s)
	journal(t, db, s, "march", "2026-03-05", byCode["6200"].ID, byCode["1110"].ID, "100")
	refusedWith(t, db.Owner.ClosePeriod(db.Context, book.ID, "2026-03-01", "2026-03-31", ""), "needs a reason")
	succeeded(t, db.ClosePeriod(db.Context, book.ID, "2026-03-01", "2026-03-31"))
	periods := periodList(t, db, s)
	if len(periods) != 1 || periods[0].Status != "closed" || periods[0].Events[0].BeforeFoundations || periods[0].Events[0].ActorID != actor ||
		periods[0].Events[0].Reason == "" || periods[0].Events[0].OpenItems["entries_without_evidence"] != 1 || periods[0].Events[0].ReportSHA256 == "" {
		t.Fatal("a native close must retain its owner, reason and exact review", periods)
	}
	if err := tryJournal(db, s, "march-later", "2026-03-05", byCode["6200"].ID, byCode["1110"].ID, "100"); err == nil {
		t.Fatal("a native close did not block posting")
	}
}

func TestCloseKeepsEveryStatementReviewOrRefusesTheRange(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	bank := roleAccount(t, db, scope, "1000", "asset", "cash")
	document := statementDocument(t, db, scope, "monthly")
	actor, _ := proposalOwner(t, db, scope)
	fixture := []map[string]string{}
	jan := map[string]bool{}
	for i := range 257 {
		id, day := ledger.ID(), "2026-01-31"
		if i == 256 {
			day = "2026-02-01"
		} else {
			jan[id] = true
		}
		fixture = append(fixture, map[string]string{"id": id, "day": day, "import_receipt": ledger.ID(), "review_receipt": ledger.ID()})
	}
	// Batch ordinary synthetic records without disabling their guards, row
	// policies or deferred receipt checks.
	succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
		_, err := tx.Exec(db.Context, `WITH fixture AS MATERIALIZED (
 SELECT * FROM jsonb_to_recordset($1::jsonb) AS f(id uuid,day date,import_receipt uuid,review_receipt uuid)
), receipts AS (
 INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 SELECT $2,$3,r.id,$4,r.id::text,r.operation,convert_to(jsonb_build_object('request_key',r.id::text)::text,'UTF8'),'{}'
 FROM fixture f CROSS JOIN LATERAL (VALUES(f.import_receipt,'import_statement'),(f.review_receipt,'review_statement')) r(id,operation)
 RETURNING id
), saved AS (
 INSERT INTO public.statements(tenant_id,book_id,id,account_id,kind,currency,scale,document_id,period_start,period_end,
 opening_balance_minor,closing_balance_minor,evidence_state,actor_id,receipt_id)
 SELECT $2,$3,f.id,$5,'cash','USD',2,$6,f.day,f.day,0,0,'complete',$4,f.import_receipt
 FROM fixture f JOIN receipts r ON r.id=f.import_receipt RETURNING id
)
INSERT INTO public.statement_reviews(tenant_id,book_id,statement_id,revision,outcome,report_bytes,actor_id,receipt_id)
 SELECT $2,$3,f.id,1,'needs_work','{"report":"example"}',$4,f.review_receipt
 FROM fixture f JOIN saved s ON s.id=f.id JOIN receipts r ON r.id=f.review_receipt`, value(fixture), scope.TenantID, book.ID, scope.ActorID, bank.ID, document)
		return err
	}))
	_, err := db.Owner.ClosePeriodDecision(db.Context, book.ID, actor, ledger.ClosePeriod{
		RequestKey: "two-months", From: "2026-01-01", Through: "2026-02-28", Reason: "Supporting records remain under review",
	})
	if !errors.Is(err, ledger.ErrLimit) {
		t.Fatal("a close must refuse a range whose statement references cannot all be retained", err)
	}
	if periods := periodList(t, db, scope); len(periods) != 0 {
		t.Fatal("the refused close retained a period", periods)
	}
	period, err := db.Owner.ClosePeriodDecision(db.Context, book.ID, actor, ledger.ClosePeriod{
		RequestKey: "january", From: "2026-01-01", Through: "2026-01-31", Reason: "Supporting records remain under review",
	})
	succeeded(t, err)
	if len(period.Events) != 1 || len(period.Events[0].StatementReviews) != len(jan) {
		t.Fatal("the accepted close omitted statement references", period.Events)
	}
	for _, ref := range period.Events[0].StatementReviews {
		if !jan[ref.StatementID] || ref.Revision != 1 {
			t.Fatal("the close kept an unexpected statement reference", ref)
		}
		delete(jan, ref.StatementID)
	}
	if len(jan) != 0 {
		t.Fatal("the close omitted statement reviews", jan)
	}
}

func listJournals(t *testing.T, db *testutil.Database, s ledger.Scope) []ledger.Entry {
	t.Helper()
	var out struct {
		Journals []ledger.Entry `json:"entries"`
	}
	if json.Unmarshal(db.Call(t, s, "list_journals", map[string]int{"limit": 100}), &out) != nil {
		t.Fatal("invalid journal list")
	}
	return out.Journals
}

func TestCloseReviewAsksOnlyAboutIncomeAndExpense(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	checking, savings, opening, software := byCode["1110"].ID, byCode["1120"].ID, byCode["3900"].ID, byCode["6200"].ID
	journal(t, db, s, "opening", "2026-03-01", checking, opening, "500000")
	journal(t, db, s, "transfer", "2026-03-05", savings, checking, "100000")
	mistake := journal(t, db, s, "mistake", "2026-03-09", software, checking, "2900")
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: mistake.ID, AccountingDate: "2026-03-10", Reason: "Example duplicate charge"})
	var review ledger.PeriodReview
	if json.Unmarshal(db.Call(t, s, "get_period_review", ledger.PeriodReviewQuery{From: "2026-03-01", Through: "2026-03-31"}), &review) != nil ||
		!review.Clean || len(review.EntriesWithoutEvidence) != 0 || len(review.EntriesWithoutPayee) != 0 {
		t.Fatal("transfers, opening balances and reversals are not open items", review)
	}
	var march ledger.AccountingPeriod
	if json.Unmarshal(db.Call(t, s, "close_period", ledger.ClosePeriod{RequestKey: "march", From: "2026-03-01", Through: "2026-03-31"}), &march) != nil ||
		march.Status != "closed" {
		t.Fatal("a month with nothing open closes without a reason", march)
	}
}

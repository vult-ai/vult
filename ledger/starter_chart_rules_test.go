package ledger_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func chartBook(t *testing.T, db *testutil.Database, template string) (ledger.Book, ledger.Scope, map[string]ledger.Account) {
	t.Helper()
	book, s := db.Book(t, "Example Book")
	db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: template})
	byCode := map[string]ledger.Account{}
	for _, a := range listedAccounts(t, db, s) {
		byCode[a.Code] = a
	}
	return book, s, byCode
}

func report(t *testing.T, db *testutil.Database, book ledger.Book, req ledger.FinancialReportRequest) ledger.FinancialReport {
	t.Helper()
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	req.BookID = book.ID
	out, err := owner.FinancialReport(db.Context, req, false)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStarterChartsMatchTheirReviewedContents(t *testing.T) {
	for _, tc := range []struct {
		id       string
		accounts int
		headings []string
	}{
		{"business-v1", 32, []string{"1000", "1100", "2000", "3000", "4000", "6000"}},
		{"personal-v1", 30, []string{"1000", "2000", "3000", "4000", "5000", "5100"}},
	} {
		chart, ok := ledger.StarterChart(tc.id)
		if !ok || len(chart.Accounts) != tc.accounts {
			t.Fatal("unexpected chart size", tc.id, len(chart.Accounts))
		}
		headings := []string{}
		for _, a := range chart.Accounts {
			if a.Heading {
				headings = append(headings, a.Code)
			}
		}
		if value(headings) == nil || string(value(headings)) != string(value(tc.headings)) {
			t.Fatal("unexpected headings", tc.id, headings)
		}
	}
}

// Two agents starting the same empty Book end with one complete chart.
func TestStarterChartsApplyOnceWhenStartedConcurrently(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, key := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = callError(db, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: key, Template: "business-v1"})
		}()
	}
	wg.Wait()
	applied, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, ledger.ErrConflict):
			refused++
		default:
			t.Fatal("unexpected concurrent outcome", err)
		}
	}
	if applied != 1 || refused != 1 || len(listedAccounts(t, db, s)) != 32 {
		t.Fatal("concurrent starts must leave exactly one complete chart", applied, refused)
	}
}

// Headings take no postings; an account that became one keeps its history,
// and a reversal can still correct a line posted before.
func TestHeadingsTakeNoPostingsButKeepCorrections(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	checking, rent, operating := byCode["1110"], byCode["6100"], byCode["6000"]
	if err := tryJournal(db, s, "heading", "2026-01-05", operating.ID, checking.ID, "100"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a heading took a posting", err)
	}
	refused(t, rawJournal(db, s, "2026-01-05", operating.ID, checking.ID), "account is a heading and takes no postings")
	entry := journal(t, db, s, "rent", "2026-01-05", rent.ID, checking.ID, "1200")
	heading := true
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "make-heading", AccountID: rent.ID, ExpectedRevision: 1, Code: rent.Code, Name: rent.Name, Heading: &heading})
	if got := listedAccounts(t, db, s)[rent.ID]; !got.Heading || got.ParentAccountID != operating.ID {
		t.Fatal("the heading setting was not recorded", got)
	}
	if b := measuredBalance(t, db, s, rent.ID); b.DebitPositiveMinor != "1200" {
		t.Fatal("becoming a heading changed the account's history", b)
	}
	if err := tryJournal(db, s, "after", "2026-01-06", rent.ID, checking.ID, "100"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a heading took a new posting", err)
	}
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: entry.ID, AccountingDate: "2026-01-07", Reason: "Posted to the wrong account"})
	if b := measuredBalance(t, db, s, rent.ID); b.DebitPositiveMinor != "0" {
		t.Fatal("the reversal did not correct the heading's earlier line", b)
	}
	// A rename that doesn't mention the setting keeps it.
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: rent.ID, ExpectedRevision: 2, Code: rent.Code, Name: "Premises"})
	if got := listedAccounts(t, db, s)[rent.ID]; !got.Heading {
		t.Fatal("a rename cleared the heading setting")
	}
	posting := false
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "postable", AccountID: rent.ID, ExpectedRevision: 3, Code: rent.Code, Name: "Premises", Heading: &posting})
	journal(t, db, s, "again", "2026-01-08", rent.ID, checking.ID, "100")
	// Adding a child never changes a parent's setting.
	child := db.Account(t, s, "6110", "expense")
	parent := rent.ID
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "child", AccountID: child.ID, ExpectedRevision: 1, Code: child.Code, Name: child.Name, ParentAccountID: &parent})
	if got := listedAccounts(t, db, s)[rent.ID]; got.Heading {
		t.Fatal("gaining a child made the parent a heading")
	}
}

// Cost of sales comes from the role, survives renaming and renumbering, and
// includes the accounts beneath it.
func TestGrossProfitFollowsTheCostOfSalesRole(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, sales, costs, rent := byCode["1110"], byCode["4100"], byCode["5000"], byCode["6100"]
	journal(t, db, s, "sale", "2026-03-02", checking.ID, sales.ID, "1000")
	journal(t, db, s, "cost", "2026-03-03", costs.ID, checking.ID, "400")
	journal(t, db, s, "rent", "2026-03-04", rent.ID, checking.ID, "100")
	check := func(gross, result string) {
		t.Helper()
		r := report(t, db, book, ledger.FinancialReportRequest{Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31"})
		if r.Reconciliation["gross_profit"] != gross || r.Reconciliation["result"] != result {
			t.Fatal("unexpected gross profit or result", r.Reconciliation)
		}
	}
	check("600", "500")
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "renumber", AccountID: costs.ID, ExpectedRevision: 1, Code: "5500", Name: "Direct costs"})
	check("600", "500")
	materials := db.Account(t, s, "5510", "expense")
	parent := costs.ID
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "subdivide", AccountID: materials.ID, ExpectedRevision: 1, Code: materials.Code, Name: "Materials", ParentAccountID: &parent})
	journal(t, db, s, "materials", "2026-03-05", materials.ID, checking.ID, "50")
	check("550", "450")
	// A Book without cost of sales keeps its two-section statement.
	other, os, otherCodes := chartBook(t, db, "personal-v1")
	journal(t, db, os, "salary", "2026-03-02", otherCodes["1100"].ID, otherCodes["4100"].ID, "1000")
	if r := report(t, db, other, ledger.FinancialReportRequest{Report: reporting.ProfitLoss, From: "2026-01-01", To: "2026-12-31"}); r.Reconciliation["gross_profit"] != "" {
		t.Fatal("a Book without cost of sales gained a gross profit line")
	}
}

// Earnings brought in as a balance and earnings derived from later entries
// each count once.
func TestImportedEarningsAndLaterHistoryCountOnce(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, accumulated, sales := byCode["1110"], byCode["3800"], byCode["4100"]
	journal(t, db, s, "brought-in", "2023-12-31", checking.ID, accumulated.ID, "40000")
	journal(t, db, s, "sales-2024", "2024-06-30", checking.ID, sales.ID, "10000")
	journal(t, db, s, "sales-2025", "2025-06-30", checking.ID, sales.ID, "5000")
	r := report(t, db, book, ledger.FinancialReportRequest{Report: reporting.BalanceSheet, AsOf: "2026-01-01"})
	var equity, account string
	var walk func([]reporting.Row)
	walk = func(rows []reporting.Row) {
		for _, row := range rows {
			if row.ID == "equity" || row.ID == "section:equity" {
				equity = row.Values["current"]
			}
			if row.AccountID == accumulated.ID {
				account = row.Values["current"]
			}
			walk(row.Children)
		}
	}
	walk(r.Rows)
	if account != "40000" || equity != "55000" {
		t.Fatal("brought-in and derived earnings must each count once", account, equity, string(value(r.Rows)))
	}
}

// A line the agent can't classify is proposed with its question and source,
// survives retries, waits for the owner and links to the entry once decided.
func TestUnclassifiedStatementLinesStayVisibleAsProposals(t *testing.T) {
	db, s, _ := documentFixture(t)
	db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: "business-v1"})
	byCode := map[string]ledger.Account{}
	for _, a := range listedAccounts(t, db, s) {
		byCode[a.Code] = a
	}
	statement := readyOriginal(t, db, s, "march-statement")
	row := 7
	q := ledger.PostJournal{RequestKey: "unknown-payment", EconomicDate: "2026-03-12", AccountingDate: "2026-03-12", Description: "Payment to Example Holdings",
		Lines:   []ledger.PostLine{{AccountID: byCode["6300"].ID, Side: "debit", AmountMinor: "20000"}, {AccountID: byCode["1110"].ID, Side: "credit", AmountMinor: "20000"}},
		Sources: []ledger.SourceInput{{DocumentID: statement.ID, Location: ledger.Location{Row: &row}}}}
	prepare := ledger.PrepareOperation{RequestKey: "propose-unknown", Operation: "post_journal", Arguments: value(q),
		Assumptions: []string{"Professional fees, the closest match to the payee"}, OpenQuestions: []string{"Who is Example Holdings, and what was this payment for?"}}
	first := db.Call(t, s, "prepare_operation", prepare)
	if string(db.Call(t, s, "prepare_operation", prepare)) != string(first) {
		t.Fatal("a retried proposal must return the original")
	}
	var p ledger.OperationProposal
	if json.Unmarshal(first, &p) != nil || len(p.Preview.Sources) != 1 || p.Preview.Sources[0].Location.Row == nil || *p.Preview.Sources[0].Location.Row != 7 ||
		p.Preview.Lines[0].AmountMinor != "20000" || p.Preview.AccountingDate != "2026-03-12" || len(p.OpenQuestions) != 1 {
		t.Fatal("the proposal must keep its source line, amount, date and question", string(first))
	}
	var state ledger.ProposalState
	if json.Unmarshal(db.Call(t, s, "get_operation_proposal", ledger.ProposalQuery{RequestKey: "propose-unknown"}), &state) != nil || state.Status != "approval_required" {
		t.Fatal("the proposal must wait for the owner", state.Status)
	}
	if b := measuredBalance(t, db, s, byCode["1110"].ID); b.DebitPositiveMinor != "0" {
		t.Fatal("a proposal must not post", b)
	}
	approveOperation(t, db, s, p, s)
	var entry ledger.Entry
	if json.Unmarshal(executeOperation(t, db, s, p), &entry) != nil || len(entryCitations(t, db, s, entry.ID, false)) != 1 {
		t.Fatal("the decided entry must keep the statement line it came from")
	}
	if json.Unmarshal(db.Call(t, s, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Status != "committed" ||
		state.Outcome == nil || !json.Valid(state.Outcome.Result) || !containsID(state.Outcome.Result, entry.ID) {
		t.Fatal("the proposal must link to its entry", state.Status)
	}
}

func containsID(result json.RawMessage, id string) bool {
	var v struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(result, &v) == nil && v.ID == id
}

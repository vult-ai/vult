package reporting

import (
	"reflect"
	"testing"
)

func TestUnknownCalendarKeepsAccumulatedResultsWithoutFiscalClaims(t *testing.T) {
	in := coreFixture()
	in.Book.FiscalStartMonth, in.Book.FiscalStartDay = 0, 0
	for i := range in.Accounts {
		in.Accounts[i].Code = ""
	}
	out := requireCore(t, in, BalanceSheet)
	if out.Book.FiscalStartMonth != 0 || out.Book.FiscalStartDay != 0 || out.Scope.FiscalStart != "" || out.Reconciliation["unclosed_result"] != "1300" || out.Reconciliation["equity_total"] != "11300" {
		t.Fatal("unknown fiscal calendar changed recorded position or became January 1")
	}
	for _, key := range []string{"current_fiscal_profit", "prior_unclosed_result", "current_unclosed_result"} {
		if _, exists := out.Reconciliation[key]; exists {
			t.Fatal("unavailable fiscal amount was asserted", key)
		}
	}
	row := findRow(out.Rows, "equity:unclosed_result")
	if row == nil || len(row.Children) != 0 || row.Values["current"] != "1300" {
		t.Fatal("unknown fiscal split hid accumulated results")
	}
	parts, err := Contributions(in, out, row.ID, "current")
	if err != nil || parts.Total != "1300" {
		t.Fatal("accumulated result lost attributable postings", err)
	}
	for _, calendar := range [][2]int{{0, 1}, {1, 0}, {2, 30}, {13, 1}} {
		in.Book.FiscalStartMonth, in.Book.FiscalStartDay = calendar[0], calendar[1]
		if _, err := Build(in, coreRequest(BalanceSheet)); err == nil {
			t.Fatal("malformed calendar accepted", calendar)
		}
	}
}

func TestEconomicPurposeAndEquityLabelsCannotRemoveRecordedExpenses(t *testing.T) {
	in := Input{Book: Book{ID: "example", Currency: "USD", Scale: 2}, Accounts: []Account{
		{ID: "expense", Name: "Income and transfers", Kind: "expense"},
		{ID: "equity", Name: "Opening balance equity", Kind: "equity"},
		{ID: "cash", Name: "Expense", Kind: "asset"},
	}, Postings: []Posting{
		{ID: "position", EntryID: "mixed", BookID: "example", AccountID: "cash", Date: "2026-06-30", Side: "debit", Amount: "10000", Currency: "USD", Scale: 2},
		{ID: "cost", EntryID: "mixed", BookID: "example", AccountID: "expense", Date: "2026-06-30", Side: "debit", Amount: "125", Currency: "USD", Scale: 2},
		{ID: "offset", EntryID: "mixed", BookID: "example", AccountID: "equity", Date: "2026-06-30", Side: "credit", Amount: "10125", Currency: "USD", Scale: 2},
	}}
	q := Request{BookID: "example", Report: ProfitLoss, From: "2026-01-01", To: "2026-12-31"}
	before, err := Build(in, q)
	if err != nil || before.Reconciliation["expenses"] != "125" || before.Reconciliation["result"] != "-125" || len(before.Findings) != 1 || before.Findings[0].Code != "opening_treatment_unconfirmed" || before.Findings[0].AmountMinor != "125" || !reflect.DeepEqual(before.Findings[0].EntryIDs, []string{"mixed"}) {
		t.Fatal("equity pattern omitted recorded expense or hid its finding", err)
	}
	for i := range in.Postings {
		in.Postings[i].EconomicPurpose = "opening"
	}
	in.Accounts[0].Name, in.Accounts[1].Name = "Retained earnings", "Ordinary account"
	after, err := Build(in, q)
	if err != nil || !reflect.DeepEqual(before.Reconciliation, after.Reconciliation) || len(after.Findings) != 1 {
		t.Fatal("descriptive purpose or account name changed the accounting", err)
	}
	q.Report, q.From, q.To, q.AsOf = BalanceSheet, "", "", "2026-12-31"
	position, err := Build(in, q)
	if err != nil || position.Reconciliation["assets"] != "10000" || position.Reconciliation["equity_total"] != "10000" {
		t.Fatal("mixed position and expense components failed to reconcile", err)
	}
}

func TestUnknownCashScopeDoesNotBecomeZeroCashFlow(t *testing.T) {
	in := coreFixture()
	q := coreRequest(ProfitLoss)
	q.Report = CashFlows
	out, err := Build(in, q)
	if err != nil || out.Status != "unsupported" || len(out.Rows) != 0 || len(out.Reconciliation) != 1 {
		t.Fatal("missing cash policy became zero cash flows", err)
	}
}

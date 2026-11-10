package reporting

import (
	"fmt"
	"math/big"
	"testing"
)

// roleFixture is a synthetic Book whose accounts carry roles; names and codes
// deliberately say nothing about cash so classification can only use roles.
func roleFixture() Input {
	in := Input{Book: Book{ID: "book-example", Name: "Example Book", Currency: "USD", Scale: 2, StartDate: "2025-01-01", FiscalStartMonth: 1, FiscalStartDay: 1, RecognitionBasis: "accrual", ReportingFramework: "unknown"},
		Provenance: Provenance{LedgerRevision: "revision-1", KnowledgeCutoff: "2026-02-01T00:00:00Z", MappingVersion: "mapping-1", HierarchyBasis: "flat_recorded_accounts", GeneratedAt: "2026-02-01T00:00:00Z", Coverage: "complete", OpeningBalances: "confirmed"},
		Accounts: []Account{
			{ID: "checking", Code: "A", Name: "Account A", Kind: "asset", Role: "cash"},
			{ID: "savings", Code: "B", Name: "Account B", Kind: "asset", Role: "cash"},
			{ID: "receivable", Code: "C", Name: "Account C", Kind: "asset", Role: "receivable"},
			{ID: "equipment", Code: "D", Name: "Account D", Kind: "asset", Role: "fixed_asset"},
			{ID: "funds", Code: "E", Name: "Account E", Kind: "asset", Role: "investment"},
			{ID: "card", Code: "F", Name: "Account F", Kind: "liability", Role: "card"},
			{ID: "loan", Code: "G", Name: "Account G", Kind: "liability", Role: "loan"},
			{ID: "capital", Code: "H", Name: "Account H", Kind: "equity"},
			{ID: "sales", Code: "I", Name: "Account I", Kind: "income"},
			{ID: "costs", Code: "J", Name: "Account J", Kind: "expense"},
			{ID: "interest", Code: "K", Name: "Account K", Kind: "expense"},
		}}
	extensionEntry(&in, "opening", "2025-01-01", "unknown", extensionLeg{"checking", "500000"}, extensionLeg{"capital", "-500000"})
	return in
}

func roleRequest(from, to string) Request {
	return Request{BookID: "book-example", Report: CashFlows, From: from, To: to}
}

func TestRoleCashFlowBridgesTwelveMonthsAndExcludesTransfers(t *testing.T) {
	in := roleFixture()
	for m := 1; m <= 12; m++ {
		d := func(day int) string { return fmt.Sprintf("2025-%02d-%02d", m, day) }
		extensionEntry(&in, fmt.Sprintf("sale-%d", m), d(3), "unknown", extensionLeg{"checking", "120000"}, extensionLeg{"sales", "-120000"})
		extensionEntry(&in, fmt.Sprintf("rent-%d", m), d(5), "unknown", extensionLeg{"costs", "90000"}, extensionLeg{"checking", "-90000"})
		extensionEntry(&in, fmt.Sprintf("move-%d", m), d(10), "unknown", extensionLeg{"savings", "10000"}, extensionLeg{"checking", "-10000"})
		// A card payment is cash out to a liability, not spending.
		extensionEntry(&in, fmt.Sprintf("card-%d", m), d(20), "unknown", extensionLeg{"card", "15000"}, extensionLeg{"checking", "-15000"})
	}
	extensionEntry(&in, "borrow", "2025-03-02", "unknown", extensionLeg{"checking", "1000000"}, extensionLeg{"loan", "-1000000"})
	extensionEntry(&in, "machine", "2025-04-02", "unknown", extensionLeg{"equipment", "300000"}, extensionLeg{"checking", "-300000"})
	extensionEntry(&in, "buy-fund", "2025-05-02", "unknown", extensionLeg{"funds", "50000"}, extensionLeg{"savings", "-50000"})
	extensionEntry(&in, "draw", "2025-06-02", "unknown", extensionLeg{"capital", "20000"}, extensionLeg{"checking", "-20000"})
	// Loan repayment with interest splits by the other side's line amounts.
	extensionEntry(&in, "repay", "2025-07-02", "unknown", extensionLeg{"checking", "-110000"}, extensionLeg{"loan", "100000"}, extensionLeg{"interest", "10000"})
	for m := 1; m <= 12; m++ {
		from, to := fmt.Sprintf("2025-%02d-01", m), fmt.Sprintf("2025-%02d-%02d", m, []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m-1])
		r, err := Build(in, roleRequest(from, to))
		if err != nil {
			t.Fatal(err)
		}
		if r.Status == "unsupported" || extensionCheckStatus(r, "cash_flow_bridge") != "pass" || extensionCheckStatus(r, "cash_flow_classification") != "pass" || r.Reconciliation["current_bridge_difference"] != "0" {
			t.Fatalf("month %d: opening plus sections must equal closing", m)
		}
		opening, _ := new(big.Int).SetString(r.Reconciliation["current_opening"], 10)
		sum := new(big.Int).Set(opening)
		for _, k := range []string{"operating", "investing", "financing", "unresolved", "internal_transfer", "exchange"} {
			v, _ := new(big.Int).SetString(r.Reconciliation["current_"+k], 10)
			sum.Add(sum, v)
		}
		if sum.String() != r.Reconciliation["current_closing"] || r.Reconciliation["current_internal_transfer"] != "0" {
			t.Fatalf("month %d: bridge %s != closing %s", m, sum, r.Reconciliation["current_closing"])
		}
	}
	year, err := Build(in, roleRequest("2025-01-01", "2025-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	// Operating: 12 × (1,200 − 900 − 150) − 100 interest = 1,700.00.
	extensionAssertValue(t, year, "cash_flow:operating", "current", "170000")
	// Investing: equipment −3,000 and the fund −500.
	extensionAssertValue(t, year, "cash_flow:investing", "current", "-350000")
	// Financing: the owner's 5,000 on Jan 1, loan +10,000, repayment −1,000, draw −200.
	extensionAssertValue(t, year, "cash_flow:financing", "current", "1380000")
	extensionAssertValue(t, year, "cash_flow:internal_transfer", "current", "0")
	extensionAssertValue(t, year, "cash_flow:unresolved", "current", "0")
	extensionAssertValue(t, year, "cash_flow:opening", "current", "0")
	extensionAssertValue(t, year, "cash_flow:closing", "current", "1200000")
	if row, _ := extensionFind(year.Rows, "cash_flow:unresolved"); row.Label != "Unclassified" {
		t.Fatal("the unexplained remainder must read Unclassified", row.Label)
	}
	if _, ok := extensionFind(year.Rows, "cash_flow:noncash"); ok {
		t.Fatal("a role-derived statement has no reviewed noncash section")
	}
}

func TestRoleCashFlowLeavesInexactSplitsUnclassified(t *testing.T) {
	in := roleFixture()
	// Borrowing that also pays a fee from the proceeds: cash +80, loan +100
	// financing and fee −20 operating carry opposite signs. No exact split
	// keeps each part's direction, so the whole movement is unclassified.
	extensionEntry(&in, "net-loan", "2026-01-05", "unknown", extensionLeg{"checking", "8000"}, extensionLeg{"loan", "-10000"}, extensionLeg{"costs", "2000"})
	// A transfer plus a fee spans cash in both directions and an expense.
	extensionEntry(&in, "move-fee", "2026-01-06", "unknown", extensionLeg{"savings", "5000"}, extensionLeg{"checking", "-5100"}, extensionLeg{"costs", "100"})
	r, err := Build(in, roleRequest("2026-01-01", "2026-01-31"))
	if err != nil {
		t.Fatal(err)
	}
	extensionAssertValue(t, r, "cash_flow:unresolved", "current", "7900")
	extensionAssertValue(t, r, "cash_flow:financing", "current", "0")
	if extensionCheckStatus(r, "cash_flow_classification") != "unknown" || extensionCheckStatus(r, "cash_flow_bridge") != "pass" || r.Status != "partial" {
		t.Fatal("an unclassified amount must be shown, never forced into a section")
	}
}

func TestRoleCashFlowWithoutCashRoleStaysUnsupported(t *testing.T) {
	in := roleFixture()
	for i := range in.Accounts {
		if in.Accounts[i].Role == "cash" {
			in.Accounts[i].Role = ""
		}
	}
	r, err := Build(in, roleRequest("2026-01-01", "2026-01-31"))
	if err != nil || r.Status != "unsupported" || len(r.Rows) != 0 {
		t.Fatal("no cash role means no cash scope", err)
	}
}

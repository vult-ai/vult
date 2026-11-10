package ledger_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func rowValue(rows []reporting.Row, id string) string {
	for _, r := range rows {
		if r.ID == id {
			return r.Values["current"]
		}
		if v := rowValue(r.Children, id); v != "" {
			return v
		}
	}
	return ""
}

func TestCashFlowClassifiesByRolesAndBridgesEveryMonth(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, savings, card, loan, equipment := byCode["1110"].ID, byCode["1120"].ID, byCode["2100"].ID, byCode["2300"].ID, byCode["1500"].ID
	sales, software := byCode["4100"].ID, byCode["6200"].ID
	for m := 1; m <= 12; m++ {
		d := func(day int) string { return fmt.Sprintf("2025-%02d-%02d", m, day) }
		journal(t, db, s, fmt.Sprintf("sale-%d", m), d(3), checking, sales, "120000")
		journal(t, db, s, fmt.Sprintf("tools-%d", m), d(5), software, checking, "30000")
		journal(t, db, s, fmt.Sprintf("save-%d", m), d(8), savings, checking, "10000")
		// A card purchase is spending; paying the card is cash to a liability.
		journal(t, db, s, fmt.Sprintf("card-buy-%d", m), d(9), software, card, "5000")
		journal(t, db, s, fmt.Sprintf("card-pay-%d", m), d(20), card, checking, "5000")
	}
	journal(t, db, s, "borrow", "2025-02-02", checking, loan, "500000")
	journal(t, db, s, "machine", "2025-03-02", equipment, checking, "200000")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	for m := 1; m <= 12; m++ {
		last := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m-1]
		r, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.CashFlows, From: fmt.Sprintf("2025-%02d-01", m), To: fmt.Sprintf("2025-%02d-%02d", m, last)}, false)
		if err != nil || r.Status == "unsupported" || r.Reconciliation["current_bridge_difference"] != "0" || r.Reconciliation["current_internal_transfer"] != "0" || r.Reconciliation["current_unresolved_count"] != "0" {
			t.Fatalf("month %d: opening plus sections must equal closing: %v %v", m, err, r.Reconciliation)
		}
	}
	var out ledger.CashFlowRead
	if json.Unmarshal(db.Call(t, s, "get_cash_flow", ledger.CashFlowQuery{From: "2025-01-01", To: "2025-12-31"}), &out) != nil {
		t.Fatal("invalid cash flow read")
	}
	st := out.Statement
	// Operating: 12 × (1,200 − 300 − 50 card payment) = 10,200.00.
	if rowValue(st.Rows, "cash_flow:operating") != "1020000" || rowValue(st.Rows, "cash_flow:financing") != "500000" || rowValue(st.Rows, "cash_flow:investing") != "-200000" ||
		rowValue(st.Rows, "cash_flow:internal_transfer") != "0" || rowValue(st.Rows, "cash_flow:opening") != "0" || rowValue(st.Rows, "cash_flow:closing") != "1320000" {
		t.Fatal("unexpected sections", st.Reconciliation)
	}
	reader := connectionScope(t, db, book.ID, "read")
	if json.Unmarshal(db.Call(t, reader, "get_cash_flow", ledger.CashFlowQuery{From: "2025-06-01", To: "2025-06-30"}), &out) != nil || rowValue(out.Statement.Rows, "cash_flow:operating") != "85000" {
		t.Fatal("read agents read cash flow")
	}
	if err := callError(db, s, "get_cash_flow", ledger.CashFlowQuery{From: "2025-07-01", To: "2025-06-30"}); err == nil {
		t.Fatal("a reversed period was accepted")
	}
}

func TestOpeningBalanceIsOpeningCashNotFinancing(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	checking, opening, contributions, sales := byCode["1110"].ID, byCode["3900"].ID, byCode["3100"].ID, byCode["4100"].ID
	// The Book starts mid-month with cash already in the bank.
	journal(t, db, s, "opening", "2025-03-10", checking, opening, "100000")
	journal(t, db, s, "contribution", "2025-03-15", checking, contributions, "20000")
	journal(t, db, s, "sale", "2025-04-02", checking, sales, "5000")
	read := func(from, to string) reporting.Result {
		var out ledger.CashFlowRead
		if json.Unmarshal(db.Call(t, s, "get_cash_flow", ledger.CashFlowQuery{From: from, To: to}), &out) != nil {
			t.Fatal("invalid cash flow read")
		}
		if out.Statement.Reconciliation["current_bridge_difference"] != "0" || out.Statement.Reconciliation["current_unresolved_count"] != "0" {
			t.Fatal("opening plus sections must equal closing", from, out.Statement.Reconciliation)
		}
		return out.Statement
	}
	for _, c := range []struct{ name, from, to, opening, financing, operating, closing string }{
		{"month before", "2025-02-01", "2025-02-28", "0", "0", "0", "0"},
		{"first month", "2025-03-01", "2025-03-31", "100000", "20000", "0", "120000"},
		{"next month", "2025-04-01", "2025-04-30", "120000", "0", "5000", "125000"},
		{"year", "2025-01-01", "2025-12-31", "100000", "20000", "5000", "125000"},
	} {
		st := read(c.from, c.to)
		if rowValue(st.Rows, "cash_flow:opening") != c.opening || rowValue(st.Rows, "cash_flow:financing") != c.financing ||
			rowValue(st.Rows, "cash_flow:operating") != c.operating || rowValue(st.Rows, "cash_flow:closing") != c.closing {
			t.Fatal(c.name+": the opening balance starts the cash flow and is not financing", st.Reconciliation)
		}
	}
}

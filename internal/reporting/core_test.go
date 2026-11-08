package reporting

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

func coreFixture() Input {
	in := Input{Book: Book{ID: "book-example", Name: "Example Book", Currency: "USD", Scale: 2, StartDate: "2024-01-01", FiscalStartMonth: 1, FiscalStartDay: 1, RecognitionBasis: "accrual", ReportingFramework: "unknown"},
		Provenance: Provenance{LedgerRevision: "ledger-r1", KnowledgeCutoff: "2025-01-01T00:00:00Z", MappingVersion: "report-v1", HierarchyBasis: "explicit_current_presentation", GeneratedAt: "2025-01-02T00:00:00Z", Coverage: "complete", OpeningBalances: "confirmed"},
		Accounts:   []Account{{ID: "cash", Code: "1000", Name: "Cash", Kind: "asset"}, {ID: "equipment", Code: "1200", Name: "Equipment", Kind: "asset"}, {ID: "contra", Code: "1210", Name: "Accumulated allowance", Kind: "asset", ParentID: "equipment"}, {ID: "capital", Code: "3000", Name: "Capital", Kind: "equity"}, {ID: "sales", Code: "4000", Name: "Revenue", Kind: "income"}, {ID: "costs", Code: "5000", Name: "Costs", Kind: "expense"}, {ID: "services", Code: "5010", Name: "Services", Kind: "expense", ParentID: "costs"}, {ID: "detail", Code: "5020", Name: "Detail", Kind: "expense", ParentID: "services"}}}
	add := func(id, date, account, side, amount, role string) {
		in.Postings = append(in.Postings, Posting{ID: id + "-" + account + "-" + side, EntryID: id, BookID: in.Book.ID, AccountID: account, Date: date, Side: side, Amount: amount, Currency: "USD", Scale: 2, Treatment: role})
	}
	add("open", "2024-01-01", "cash", "debit", "10000", "opening")
	add("open", "2024-01-01", "capital", "credit", "10000", "opening")
	add("sale", "2024-06-01", "cash", "debit", "2000", "ordinary")
	add("sale", "2024-06-01", "sales", "credit", "2000", "ordinary")
	for i, v := range []struct{ account, amount string }{{"costs", "100"}, {"services", "200"}, {"detail", "300"}} {
		id := "cost-" + string(rune('a'+i))
		add(id, "2024-06-02", v.account, "debit", v.amount, "ordinary")
		add(id, "2024-06-02", "cash", "credit", v.amount, "ordinary")
	}
	add("asset", "2024-06-03", "equipment", "debit", "1000", "ordinary")
	add("asset", "2024-06-03", "cash", "credit", "1000", "ordinary")
	add("allowance", "2024-06-04", "detail", "debit", "100", "adjustment")
	add("allowance", "2024-06-04", "contra", "credit", "100", "adjustment")
	return in
}
func coreRequest(kind Kind) Request {
	r := Request{BookID: "book-example", Report: kind, From: "2024-01-01", To: "2024-12-31"}
	if kind == BalanceSheet || kind == TrialBalance {
		r.From = ""
		r.To = ""
		r.AsOf = "2024-12-31"
	}
	return r
}
func requireCore(t *testing.T, in Input, kind Kind) Result {
	t.Helper()
	r, e := BuildCore(in, coreRequest(kind))
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestCoreHierarchyDirectAndContraAmounts(t *testing.T) {
	in := coreFixture()
	profit := requireCore(t, in, ProfitLoss)
	if profit.Reconciliation["income"] != "2000" || profit.Reconciliation["expenses"] != "700" || profit.Reconciliation["result"] != "1300" {
		t.Fatal("period reconstruction")
	}
	parent := findRow(profit.Rows, "account:costs")
	if parent == nil || parent.Values["current"] != "700" || parent.OwnValues["current"] != "100" {
		t.Fatal("parent direct/descendant reconstruction")
	}
	direct := findRow(profit.Rows, "direct:costs")
	if direct == nil || direct.Values["current"] != "100" {
		t.Fatal("direct activity row")
	}
	c, e := Contributions(in, profit, direct.ID, "current")
	if e != nil || c.Total != "100" || len(c.Contributions) != 1 {
		t.Fatal("direct activity contribution", e)
	}
	position := requireCore(t, in, BalanceSheet)
	if position.Reconciliation["assets"] != "11300" || position.Reconciliation["equity_total"] != "11300" {
		t.Fatal("position reconstruction")
	}
	equipment := findRow(position.Rows, "account:equipment")
	if equipment.Values["current"] != "900" || equipment.OwnValues["current"] != "1000" {
		t.Fatal("contra account sign")
	}
}

func TestCoreExplicitClosingPreservesProfitAndDoesNotDoubleCountEquity(t *testing.T) {
	in := coreFixture()
	for i, v := range []struct{ id, side, amount string }{{"sales", "debit", "2000"}, {"costs", "credit", "100"}, {"services", "credit", "200"}, {"detail", "credit", "400"}, {"capital", "credit", "1300"}} {
		in.Postings = append(in.Postings, Posting{ID: "close-" + string(rune('a'+i)), EntryID: "close", BookID: in.Book.ID, AccountID: v.id, Date: "2024-12-31", Side: v.side, Amount: v.amount, Currency: "USD", Scale: 2, Treatment: "closing"})
	}
	position := requireCore(t, in, BalanceSheet)
	profit := requireCore(t, in, ProfitLoss)
	if profit.Reconciliation["result"] != "1300" || position.Reconciliation["posted_equity"] != "11300" || position.Reconciliation["current_unclosed_result"] != "0" || position.Reconciliation["current_fiscal_closing"] != "-1300" || position.Reconciliation["equity_total"] != "11300" {
		t.Fatal("closing/equity bridge")
	}
	if findRow(position.Rows, "equity:unclosed_result").Values["current"] != "0" {
		t.Fatal("closing duplicated in equity")
	}
}

func TestCoreOpeningIncomeIsNotCurrentEarnedProfit(t *testing.T) {
	in := coreFixture()
	for _, v := range []struct{ id, side string }{{"cash", "debit"}, {"sales", "credit"}} {
		in.Postings = append(in.Postings, Posting{ID: "opening-result-" + v.id, EntryID: "opening-result", BookID: in.Book.ID, AccountID: v.id, Date: "2024-01-01", Side: v.side, Amount: "100", Currency: "USD", Scale: 2, Treatment: "opening"})
	}
	r := requireCore(t, in, BalanceSheet)
	p := requireCore(t, in, ProfitLoss)
	if r.Reconciliation["current_fiscal_opening"] != "100" || r.Reconciliation["current_fiscal_profit"] != "1300" || r.Reconciliation["current_unclosed_result"] != "1400" || p.Reconciliation["result"] != "1300" {
		t.Fatal("opening versus earned-result distinction")
	}
}

func TestCoreMonthComparisonsUseCalendarBoundaries(t *testing.T) {
	in := coreFixture()
	req := Request{BookID: in.Book.ID, Report: ProfitLoss, From: "2024-10-01", To: "2024-10-31", Comparison: "previous_period"}
	r, e := BuildCore(in, req)
	if e != nil {
		t.Fatal(e)
	}
	if r.Scope.Comparison.From != "2024-09-01" || r.Scope.Comparison.To != "2024-09-30" {
		t.Fatal("calendar comparison")
	}
	req = Request{BookID: in.Book.ID, Report: BalanceSheet, AsOf: "2024-03-31", Comparison: "previous_period"}
	r, e = BuildCore(in, req)
	if e != nil || r.Scope.Comparison.To != "2024-02-29" {
		t.Fatal("month-end comparison", e)
	}
	req.Comparison = "previous_year"
	req.AsOf = "2024-02-29"
	r, e = BuildCore(in, req)
	if e != nil || r.Scope.Comparison.To != "2023-02-28" {
		t.Fatal("leap comparison", e)
	}
	req.Comparison = "previous_period"
	req.AsOf = "2024-09-30"
	r, e = BuildCore(in, req)
	if e != nil || r.Scope.Comparison.To != "2024-08-31" {
		t.Fatal("prior month end", e)
	}
	req = Request{BookID: in.Book.ID, Report: ProfitLoss, From: "2025-02-01", To: "2025-02-28", Comparison: "previous_year"}
	r, e = BuildCore(in, req)
	if e != nil || r.Scope.Comparison.To != "2024-02-29" {
		t.Fatal("prior full calendar month", e)
	}
}

func TestCorePermutedInputReplaysIdenticallyWithoutInputMutation(t *testing.T) {
	in := coreFixture()
	before, _ := json.Marshal(in)
	a := requireCore(t, in, BalanceSheet)
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
	for i, j := 0, len(in.Accounts)-1; i < j; i, j = i+1, j-1 {
		in.Accounts[i], in.Accounts[j] = in.Accounts[j], in.Accounts[i]
	}
	for i, j := 0, len(in.Postings)-1; i < j; i, j = i+1, j-1 {
		in.Postings[i], in.Postings[j] = in.Postings[j], in.Postings[i]
	}
	b := requireCore(t, in, BalanceSheet)
	if a.Digest != b.Digest {
		t.Fatal("permutation changed deterministic result")
	}
}

func TestCorePresentationAndFiscalMetadataChangeDigest(t *testing.T) {
	for _, change := range []func(*Input){func(in *Input) { in.Accounts[0].Name = "Renamed account" }, func(in *Input) { in.Accounts[2].ParentID = "" }, func(in *Input) { in.Book.FiscalStartMonth = 4 }, func(in *Input) {
		in.Accounts = append(in.Accounts, Account{ID: "unused", Code: "9999", Name: "Unused", Kind: "asset"})
	}} {
		in := coreFixture()
		a := requireCore(t, in, BalanceSheet)
		change(&in)
		b := requireCore(t, in, BalanceSheet)
		if a.Scope.PopulationDigest == b.Scope.PopulationDigest || a.Digest == b.Digest {
			t.Fatal("unbound metadata")
		}
	}
}

func TestCoreSamePopulationContributionReadIgnoresRenderingClock(t *testing.T) {
	in := coreFixture()
	r := requireCore(t, in, BalanceSheet)
	in.Provenance.GeneratedAt = "2025-01-03T00:00:00Z"
	if _, e := Contributions(in, r, "account:cash", "current"); e != nil {
		t.Fatal(e)
	}
	in.Provenance.LedgerRevision = "ledger-r2"
	if _, e := Contributions(in, r, "account:cash", "current"); e == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestCoreUnknownTreatmentDoesNotInventClosingTreatment(t *testing.T) {
	in := coreFixture()
	for i := range in.Postings {
		in.Postings[i].Treatment = ""
	}
	r := requireCore(t, in, ProfitLoss)
	if r.Status != "partial" {
		t.Fatal("unknown purpose qualified")
	}
	found := false
	for _, c := range r.Checks {
		if c.ID == "entry_purpose" && c.Status == "unknown" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing purpose qualification")
	}
}

func TestCoreAllDisplayedValuesHaveExactContributions(t *testing.T) {
	in := coreFixture()
	for _, kind := range []Kind{BalanceSheet, ProfitLoss, TrialBalance, GeneralLedger, Journal} {
		t.Run(string(kind), func(t *testing.T) {
			r := requireCore(t, in, kind)
			var visit func([]Row)
			visit = func(rows []Row) {
				for _, row := range rows {
					for _, col := range r.Columns {
						if _, ok := row.Values[col.ID]; !ok {
							continue
						}
						out, e := Contributions(in, r, row.ID, col.ID)
						if e != nil || out.Total != row.Values[col.ID] {
							t.Fatalf("report contribution parity role=%s column=%s: %v", row.Role, col.ID, e)
						}
					}
					visit(row.Children)
				}
			}
			visit(r.Rows)
		})
	}
}

func TestCoreCSVExactResultScopeAndTextSafety(t *testing.T) {
	in := coreFixture()
	in.Book.Name = "@Example"
	in.Accounts[0].Name = "=Example"
	r := requireCore(t, in, BalanceSheet)
	data, e := CSV(r)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if e != nil { // Variable-width metadata rows are intentional.
		reader := csv.NewReader(strings.NewReader(string(data)))
		reader.FieldsPerRecord = -1
		rows, e = reader.ReadAll()
	}
	if e != nil {
		t.Fatal(e)
	}
	foundName, foundDigest, foundAmount := false, false, false
	for _, row := range rows {
		for _, cell := range row {
			if cell == "'@Example" || cell == "'=Example" {
				foundName = true
			}
			if cell == r.Digest {
				foundDigest = true
			}
			if cell == "104.00" {
				foundAmount = true
			}
		}
	}
	if !foundName || !foundDigest || !foundAmount {
		t.Fatal("CSV result metadata/values")
	}
	r.Rows[0].Values["current"] = "1"
	if _, e := CSV(r); e == nil {
		t.Fatal("altered result accepted")
	}
}

func TestCoreInvalidComparisonAndFilteringFailClosed(t *testing.T) {
	in := coreFixture()
	cases := []Request{{BookID: in.Book.ID, Report: BalanceSheet, AsOf: "0001-01-01", Comparison: "previous_year"}, {BookID: in.Book.ID, Report: ProfitLoss, From: "2024-01-01", To: "2024-12-31", Currency: "EUR"}, {BookID: in.Book.ID, Report: BalanceSheet, AsOf: "2024-12-31", AccountIDs: []string{"cash"}}, {BookID: in.Book.ID, Report: Journal, From: "2024-01-01", To: "2024-12-31", Comparison: "previous_period"}}
	for _, req := range cases {
		if _, e := BuildCore(in, req); e == nil {
			t.Fatal("unsupported scope accepted")
		}
	}
	long := Request{BookID: in.Book.ID, Report: ProfitLoss, From: "2000-01-02", To: "9999-12-30", Comparison: "previous_period"}
	if _, e := BuildCore(in, long); e == nil {
		t.Fatal("out-of-domain prior period was silently shortened by duration overflow")
	}
}

func TestCoreUsesProvidedEntryReferenceWithoutInventingOne(t *testing.T) {
	in := coreFixture()
	for i := range in.Postings {
		if in.Postings[i].EntryID == "sale" {
			in.Postings[i].EntryReference = "ABCD2345"
		}
	}
	r := requireCore(t, in, Journal)
	row := findRow(r.Rows, "entry:sale")
	if row == nil || row.EntryReference != "ABCD2345" || row.Label != "Entry ABCD2345" {
		t.Fatal("provided reference not preserved")
	}
	c, e := Contributions(in, r, row.ID, "credit")
	if e != nil || len(c.Contributions) != 1 || c.Contributions[0].EntryReference != "ABCD2345" {
		t.Fatal("reference lost in contribution", e)
	}
	fallback := findRow(r.Rows, "entry:open")
	if fallback.Label != "Journal entry" || fallback.EntryReference != "" {
		t.Fatal("fallback fabricated an entry reference")
	}
}

func TestCoreFinalizeNeverUpgradesExplicitPartialStatus(t *testing.T) {
	r, e := NewResult(coreFixture(), coreRequest(BalanceSheet))
	if e != nil {
		t.Fatal(e)
	}
	r.Status = "partial"
	Finalize(&r)
	if r.Status != "partial" || !CheckResultDigest(r) {
		t.Fatal("explicit partial status upgraded")
	}
}

func TestCoreDecimalAvoidsFloatingPoint(t *testing.T) {
	for _, v := range []struct {
		value string
		scale int
		want  string
	}{{"123456789012345678901", 2, "1234567890123456789.01"}, {"-7", 3, "-0.007"}, {"0", 2, "0.00"}, {"1", 0, "1"}} {
		got, e := Decimal(v.value, v.scale)
		if e != nil || got != v.want {
			t.Fatal("exact decimal", e)
		}
	}
	for _, bad := range []string{"1.2", "NaN", "1e3", "+1", " 1"} {
		if _, e := Decimal(bad, 2); e == nil {
			t.Fatal("malformed amount")
		}
	}
}

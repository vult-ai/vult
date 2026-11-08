package reporting_test

// Independent expectations in this file are manually specified, invented
// records. Production builders never supply the expected answers.
import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"testing"

	r "github.com/vult-ai/vult/internal/reporting"
)

func reportFixtureBase() r.Input {
	return r.Input{
		Book: r.Book{ID: "fixture-book", Name: "Synthetic reporting book", Currency: "USD", Scale: 2,
			StartDate: "2020-01-01", FiscalStartMonth: 1, FiscalStartDay: 1,
			RecognitionBasis: "accrual", ReportingFramework: "unknown"},
		Provenance: r.Provenance{LedgerRevision: "ledger-v1", KnowledgeCutoff: "2026-05-01T00:00:00Z",
			MappingVersion: "mapping-v1", HierarchyBasis: "explicit_current_presentation",
			GeneratedAt: "2026-05-01T00:00:00Z", Coverage: "complete", OpeningBalances: "confirmed"},
	}
}

func reportFixtureAccount(id, kind, parent string) r.Account {
	return r.Account{ID: id, Code: id, Name: "Synthetic " + id, Kind: kind, ParentID: parent}
}

func reportFixturePosting(id, entry, account, date, side, amount, purpose string) r.Posting {
	return r.Posting{ID: id, EntryID: entry, BookID: "fixture-book", AccountID: account,
		Date: date, Side: side, Amount: amount, Currency: "USD", Scale: 2, Treatment: purpose}
}

func reportFixturePair(in *r.Input, entry, date, debit, credit, amount, purpose string) {
	in.Postings = append(in.Postings,
		reportFixturePosting(entry+"-d", entry, debit, date, "debit", amount, purpose),
		reportFixturePosting(entry+"-c", entry, credit, date, "credit", amount, purpose))
}

func reportFixtureHierarchy() r.Input {
	in := reportFixtureBase()
	in.Accounts = []r.Account{
		reportFixtureAccount("cash", "asset", ""),
		reportFixtureAccount("equipment", "asset", ""),
		reportFixtureAccount("equipment-child", "asset", "equipment"),
		reportFixtureAccount("equipment-grandchild", "asset", "equipment-child"),
		reportFixtureAccount("depreciation", "asset", "equipment-child"),
		reportFixtureAccount("capital", "equity", ""),
		reportFixtureAccount("income", "income", ""),
		reportFixtureAccount("cost", "expense", ""),
		reportFixtureAccount("unused", "asset", ""),
	}
	in.Postings = []r.Posting{
		reportFixturePosting("opening-cash", "opening", "cash", "2026-01-01", "debit", "2500", "opening"),
		reportFixturePosting("opening-parent", "opening", "equipment", "2026-01-01", "debit", "1000", "opening"),
		reportFixturePosting("opening-child", "opening", "equipment-child", "2026-01-01", "debit", "2000", "opening"),
		reportFixturePosting("opening-grandchild", "opening", "equipment-grandchild", "2026-01-01", "debit", "3000", "opening"),
		reportFixturePosting("opening-contra", "opening", "depreciation", "2026-01-01", "credit", "500", "opening"),
		reportFixturePosting("opening-capital", "opening", "capital", "2026-01-01", "credit", "8000", "opening"),
	}
	reportFixturePair(&in, "funding-one", "2026-01-05", "cash", "capital", "125", "ordinary")
	reportFixturePair(&in, "funding-two", "2026-01-05", "cash", "capital", "125", "ordinary")
	reportFixturePair(&in, "sale", "2026-01-15", "cash", "income", "800", "ordinary")
	reportFixturePair(&in, "cost", "2026-01-20", "cost", "cash", "300", "ordinary")
	return in
}

func reportFixtureRequest(kind r.Kind) r.Request {
	q := r.Request{BookID: "fixture-book", Report: kind}
	if kind == r.BalanceSheet || kind == r.TrialBalance {
		q.AsOf = "2026-01-31"
	} else {
		q.From, q.To = "2026-01-01", "2026-01-31"
	}
	return q
}

func reportFixtureBuild(t *testing.T, in r.Input, q r.Request) r.Result {
	t.Helper()
	out, err := r.BuildCore(in, q)
	if err != nil {
		t.Fatalf("synthetic report build failed: %v", err)
	}
	return out
}

func reportFixtureRows(rows []r.Row) []r.Row {
	var out []r.Row
	for _, row := range rows {
		out = append(out, row)
		out = append(out, reportFixtureRows(row.Children)...)
	}
	return out
}

func reportFixtureFindAccount(t *testing.T, out r.Result, id string) r.Row {
	t.Helper()
	for _, row := range reportFixtureRows(out.Rows) {
		if row.AccountID == id && row.Role == "account" {
			return row
		}
	}
	t.Fatalf("synthetic account row missing: %s", id)
	return r.Row{}
}

func reportFixtureEqual(t *testing.T, actual, expected, field string) {
	t.Helper()
	if actual != expected {
		t.Errorf("%s: got %q, want %q", field, actual, expected)
	}
}

func reportFixtureClone(in r.Input) r.Input {
	data, _ := json.Marshal(in)
	var out r.Input
	_ = json.Unmarshal(data, &out)
	return out
}

func TestReportDirectParentDescendantsContraAndDuplicates(t *testing.T) {
	in, q := reportFixtureHierarchy(), reportFixtureRequest(r.BalanceSheet)
	out := reportFixtureBuild(t, in, q)
	for account, expected := range map[string]string{
		"cash": "3250", "equipment": "5500", "equipment-child": "4500",
		"equipment-grandchild": "3000", "depreciation": "-500", "capital": "8250",
	} {
		row := reportFixtureFindAccount(t, out, account)
		reportFixtureEqual(t, row.Values["current"], expected, account+" recursive amount")
	}
	reportFixtureEqual(t, reportFixtureFindAccount(t, out, "equipment").OwnValues["current"], "1000", "parent direct amount")
	reportFixtureEqual(t, reportFixtureFindAccount(t, out, "equipment-child").OwnValues["current"], "2000", "child direct amount")
	for key, expected := range map[string]string{"assets": "8750", "liabilities": "0", "posted_equity": "8250", "equity_total": "8750", "current_unclosed_result": "500"} {
		reportFixtureEqual(t, out.Reconciliation[key], expected, key)
	}
	row := reportFixtureFindAccount(t, out, "equipment")
	contributions, err := r.Contributions(in, out, row.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	reportFixtureEqual(t, contributions.ResultDigest, out.Digest, "contribution result identity")
	reportFixtureEqual(t, contributions.Debit, "6000", "parent constituent debits")
	reportFixtureEqual(t, contributions.Credit, "500", "parent constituent credits")
	if len(contributions.Contributions) != 4 {
		t.Errorf("parent contribution population: got %d, want 4", len(contributions.Contributions))
	}
	cash := reportFixtureFindAccount(t, out, "cash")
	parts, err := r.Contributions(in, out, cash.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range parts.Contributions {
		if p.Amount == "125" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("distinct legitimate duplicate contributions: got %d, want 2", count)
	}
}

func TestReportNestedExpenseSubtotals(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("income", "income", ""),
		reportFixtureAccount("cost-parent", "expense", ""), reportFixtureAccount("cost-child", "expense", "cost-parent"),
		reportFixtureAccount("cost-grandchild", "expense", "cost-child")}
	reportFixturePair(&in, "sale", "2026-01-10", "cash", "income", "1000", "ordinary")
	reportFixturePair(&in, "parent-cost", "2026-01-11", "cost-parent", "cash", "50", "ordinary")
	reportFixturePair(&in, "child-cost", "2026-01-12", "cost-child", "cash", "200", "ordinary")
	reportFixturePair(&in, "grandchild-cost", "2026-01-13", "cost-grandchild", "cash", "100", "ordinary")
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.ProfitLoss))
	reportFixtureEqual(t, reportFixtureFindAccount(t, out, "cost-parent").Values["current"], "350", "recursive costs")
	reportFixtureEqual(t, reportFixtureFindAccount(t, out, "cost-parent").OwnValues["current"], "50", "own costs")
	reportFixtureEqual(t, reportFixtureFindAccount(t, out, "cost-child").Values["current"], "300", "nested costs")
	reportFixtureEqual(t, out.Reconciliation["income"], "1000", "income")
	reportFixtureEqual(t, out.Reconciliation["expenses"], "350", "expenses")
	reportFixtureEqual(t, out.Reconciliation["result"], "650", "result")
}

func reportFixtureFiscal() r.Input {
	in := reportFixtureBase()
	in.Book.FiscalStartMonth = 4
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", ""),
		reportFixtureAccount("retained", "equity", ""), reportFixtureAccount("income", "income", ""), reportFixtureAccount("cost", "expense", "")}
	reportFixturePair(&in, "opening", "2026-01-01", "cash", "capital", "1000", "opening")
	reportFixturePair(&in, "march-sale", "2026-03-31", "cash", "income", "800", "ordinary")
	reportFixturePair(&in, "march-cost", "2026-03-31", "cost", "cash", "300", "ordinary")
	in.Postings = append(in.Postings,
		reportFixturePosting("close-income", "close", "income", "2026-03-31", "debit", "800", "closing"),
		reportFixturePosting("close-cost", "close", "cost", "2026-03-31", "credit", "300", "closing"),
		reportFixturePosting("close-retained", "close", "retained", "2026-03-31", "credit", "500", "closing"))
	reportFixturePair(&in, "april-sale", "2026-04-15", "cash", "income", "900", "ordinary")
	reportFixturePair(&in, "april-cost", "2026-04-15", "cost", "cash", "400", "ordinary")
	return in
}

func TestReportFiscalCloseDoesNotDuplicateEquityOrEraseProfit(t *testing.T) {
	in := reportFixtureFiscal()
	for _, tc := range []struct{ asOf, assets, posted, residual string }{
		{"2026-03-31", "1500", "1500", "0"}, {"2026-04-30", "2000", "1500", "500"},
	} {
		t.Run(tc.asOf, func(t *testing.T) {
			out := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.BalanceSheet, AsOf: tc.asOf})
			reportFixtureEqual(t, out.Reconciliation["assets"], tc.assets, "assets")
			reportFixtureEqual(t, out.Reconciliation["equity_total"], tc.assets, "total equity")
			reportFixtureEqual(t, out.Reconciliation["posted_equity"], tc.posted, "posted equity")
			reportFixtureEqual(t, out.Reconciliation["current_unclosed_result"], tc.residual, "unclosed current result")
		})
	}
	for _, tc := range []struct{ from, to, income, expenses, result string }{
		{"2026-03-01", "2026-03-31", "800", "300", "500"},
		{"2026-04-01", "2026-04-30", "900", "400", "500"},
		{"2026-03-01", "2026-04-30", "1700", "700", "1000"},
	} {
		out := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: tc.from, To: tc.to})
		reportFixtureEqual(t, out.Reconciliation["income"], tc.income, "earned income")
		reportFixtureEqual(t, out.Reconciliation["expenses"], tc.expenses, "earned expenses")
		reportFixtureEqual(t, out.Reconciliation["result"], tc.result, "earned result")
	}
}

func TestReportReverseCloseCorrectAndReclosePreservesEarnedResult(t *testing.T) {
	in := reportFixtureFiscal()
	q := r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2026-03-01", To: "2026-03-31"}
	previous := reportFixtureBuild(t, in, q)
	previousCSV, err := r.CSV(previous)
	if err != nil {
		t.Fatal(err)
	}
	// The reverse-close explicitly retains closing-transfer treatment. That is
	// supplied typed information, not an inference from reversal names or memos.
	in.Postings = append(in.Postings,
		reportFixturePosting("reopen-income", "reopen", "income", "2026-03-31", "credit", "800", "closing"),
		reportFixturePosting("reopen-cost", "reopen", "cost", "2026-03-31", "debit", "300", "closing"),
		reportFixturePosting("reopen-retained", "reopen", "retained", "2026-03-31", "debit", "500", "closing"))
	reportFixturePair(&in, "correct-march-sale", "2026-03-31", "cash", "income", "200", "adjustment")
	in.Postings = append(in.Postings,
		reportFixturePosting("reclose-income", "reclose", "income", "2026-03-31", "debit", "1000", "closing"),
		reportFixturePosting("reclose-cost", "reclose", "cost", "2026-03-31", "credit", "300", "closing"),
		reportFixturePosting("reclose-retained", "reclose", "retained", "2026-03-31", "credit", "700", "closing"))
	in.Provenance.LedgerRevision = "ledger-v2"
	corrected := reportFixtureBuild(t, in, q)
	reportFixtureEqual(t, corrected.Reconciliation["income"], "1000", "corrected earned income")
	reportFixtureEqual(t, corrected.Reconciliation["expenses"], "300", "corrected earned expense")
	reportFixtureEqual(t, corrected.Reconciliation["result"], "700", "corrected earned result")
	position := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.BalanceSheet, AsOf: "2026-04-30"})
	reportFixtureEqual(t, position.Reconciliation["assets"], "2200", "corrected assets")
	reportFixtureEqual(t, position.Reconciliation["posted_equity"], "1700", "corrected posted equity")
	reportFixtureEqual(t, position.Reconciliation["prior_unclosed_result"], "0", "reclosed prior fiscal residual")
	reportFixtureEqual(t, position.Reconciliation["current_unclosed_result"], "500", "current earned residual")
	reportFixtureEqual(t, position.Reconciliation["equity_total"], "2200", "corrected equity without duplicate earned result")
	reportFixtureEqual(t, previous.Reconciliation["result"], "500", "previous result remains captured")
	if previous.Digest == corrected.Digest {
		t.Error("correcting entry retained the previous result digest")
	}
	again, err := r.CSV(previous)
	if err != nil || !bytes.Equal(again, previousCSV) {
		t.Error("correction changed the previously captured export")
	}
}

func TestReportTrialBalanceRetainsFullAccountsAndContraCredit(t *testing.T) {
	out := reportFixtureBuild(t, reportFixtureHierarchy(), reportFixtureRequest(r.TrialBalance))
	reportFixtureEqual(t, out.Reconciliation["debit"], "9550", "trial balance debits")
	reportFixtureEqual(t, out.Reconciliation["credit"], "9550", "trial balance credits")
	contra := reportFixtureFindAccount(t, out, "depreciation")
	reportFixtureEqual(t, contra.Values["debit"], "0", "contra debit balance")
	reportFixtureEqual(t, contra.Values["credit"], "500", "contra credit balance")
	unused := reportFixtureFindAccount(t, out, "unused")
	reportFixtureEqual(t, unused.Values["debit"], "0", "zero-activity debit")
	reportFixtureEqual(t, unused.Values["credit"], "0", "zero-activity credit")
}

func TestReportPositionIncludesOpeningAndExcludesFuture(t *testing.T) {
	in := reportFixtureHierarchy()
	reportFixturePair(&in, "future", "2026-02-01", "cash", "income", "999", "ordinary")
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	reportFixtureEqual(t, out.Reconciliation["assets"], "8750", "as-of assets")
	q := r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2026-01-16", To: "2026-01-31"}
	period := reportFixtureBuild(t, in, q)
	reportFixtureEqual(t, period.Reconciliation["income"], "0", "period income")
	reportFixtureEqual(t, period.Reconciliation["expenses"], "300", "period expenses")
	reportFixtureEqual(t, period.Reconciliation["result"], "-300", "period result")
}

func TestReportLiveCorrectionAndPresentationDoNotMutateReturnedResult(t *testing.T) {
	in, q := reportFixtureHierarchy(), reportFixtureRequest(r.BalanceSheet)
	before := reportFixtureBuild(t, in, q)
	beforeJSON, _ := json.Marshal(before)
	beforeCSV, err := r.CSV(before)
	if err != nil {
		t.Fatal(err)
	}
	changed := reportFixtureClone(in)
	reportFixturePair(&changed, "correction", "2026-01-15", "income", "cash", "100", "ordinary")
	changed.Provenance.LedgerRevision = "ledger-v2"
	changed.Provenance.KnowledgeCutoff = "2026-05-02T00:00:00Z"
	changed.Provenance.GeneratedAt = "2026-05-02T00:00:00Z"
	after := reportFixtureBuild(t, changed, q)
	reportFixtureEqual(t, after.Reconciliation["assets"], "8650", "corrected live assets")
	if before.Digest == after.Digest {
		t.Error("later correction retained stale result digest")
	}
	changed.Accounts[1].Name = "Renamed synthetic grouping"
	changed.Accounts[3].ParentID = "equipment"
	changed.Provenance.MappingVersion = "mapping-v2"
	represented := reportFixtureBuild(t, changed, q)
	if represented.Digest == after.Digest {
		t.Error("changed presentation retained stale result digest")
	}
	actualJSON, _ := json.Marshal(before)
	actualCSV, err := r.CSV(before)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeJSON, actualJSON) || !bytes.Equal(beforeCSV, actualCSV) {
		t.Error("later live inputs mutated the previously returned result")
	}
}

func TestReportScopeIdentityHierarchyAndAmountRejections(t *testing.T) {
	cases := map[string]func(*r.Input){
		"foreign posting":            func(in *r.Input) { in.Postings[0].BookID = "another-book" },
		"unknown account":            func(in *r.Input) { in.Postings[0].AccountID = "absent" },
		"duplicate posting identity": func(in *r.Input) { in.Postings = append(in.Postings, in.Postings[0]) },
		"duplicate account identity": func(in *r.Input) { in.Accounts = append(in.Accounts, in.Accounts[0]) },
		"missing parent":             func(in *r.Input) { in.Accounts[1].ParentID = "absent" },
		"cycle":                      func(in *r.Input) { in.Accounts[1].ParentID = "equipment-grandchild" },
		"foreign currency":           func(in *r.Input) { in.Postings[0].Currency = "EUR" },
		"foreign scale":              func(in *r.Input) { in.Postings[0].Scale = 3 },
		"float amount":               func(in *r.Input) { in.Postings[0].Amount = "2500.0" },
		"exponent amount":            func(in *r.Input) { in.Postings[0].Amount = "25e2" },
		"signed magnitude":           func(in *r.Input) { in.Postings[0].Amount = "-2500" },
		"invalid date":               func(in *r.Input) { in.Postings[0].Date = "2026-02-30" },
		"unbalanced entry":           func(in *r.Input) { in.Postings[0].Amount = "2501" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := reportFixtureHierarchy()
			change(&in)
			if _, err := r.BuildCore(in, reportFixtureRequest(r.BalanceSheet)); err == nil {
				t.Error("invalid synthetic input accepted")
			}
		})
	}
	for name, q := range map[string]r.Request{
		"foreign request": {BookID: "another-book", Report: r.BalanceSheet, AsOf: "2026-01-31"},
		"translation":     {BookID: "fixture-book", Report: r.BalanceSheet, AsOf: "2026-01-31", Currency: "EUR"},
		"invalid as-of":   {BookID: "fixture-book", Report: r.BalanceSheet, AsOf: "2026-02-30"},
		"reversed period": {BookID: "fixture-book", Report: r.ProfitLoss, From: "2026-02-01", To: "2026-01-01"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.BuildCore(reportFixtureHierarchy(), q); err == nil {
				t.Error("invalid synthetic request accepted")
			}
		})
	}
}

func TestReportExactAmountsBeyondInt64(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", "")}
	const large = "18446744073709551616"
	reportFixturePair(&in, "funding", "2026-01-01", "cash", "capital", large, "opening")
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	reportFixtureEqual(t, out.Reconciliation["assets"], large, "exact large assets")
	reportFixtureEqual(t, out.Reconciliation["equity_total"], large, "exact large equity")
	data, err := r.CSV(out)
	if err != nil {
		t.Fatal(err)
	}
	// The CSV contract expresses exact currency decimals, not raw minor units.
	// This expected decimal is written independently, without the export helper.
	if !bytes.Contains(data, []byte("184467440737095516.16")) {
		t.Error("export lost exact large decimal amount")
	}
}

func TestReportUnknownCoverageBasisAndTreatmentRemainExplicit(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", "")}
	in.Book.RecognitionBasis = "unknown"
	in.Provenance.Coverage, in.Provenance.OpeningBalances = "unknown", "unknown"
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	if out.Status == "ready" || out.Scope.Coverage == "complete" || len(out.Limitations) == 0 {
		t.Error("unconfirmed empty population presented as fully ready")
	}
	in = reportFixtureHierarchy()
	for i := range in.Postings {
		if in.Postings[i].EntryID == "opening" {
			in.Postings[i].Treatment = ""
		}
	}
	unknown := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	foundTreatment := false
	for _, limitation := range unknown.Limitations {
		foundTreatment = foundTreatment || strings.Contains(strings.ToLower(limitation), "purpose")
	}
	if !foundTreatment {
		t.Error("unknown typed purpose lost its interpretation limitation")
	}
}

func TestReportSameNamesDoNotMergeIdentities(t *testing.T) {
	in := reportFixtureHierarchy()
	in.Accounts[1].Name, in.Accounts[2].Name = "Same synthetic label", "Same synthetic label"
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	parent, child := reportFixtureFindAccount(t, out, "equipment"), reportFixtureFindAccount(t, out, "equipment-child")
	if parent.ID == child.ID {
		t.Error("separate stable account identities merged")
	}
	reportFixtureEqual(t, parent.OwnValues["current"], "1000", "same-name parent own")
	reportFixtureEqual(t, child.OwnValues["current"], "2000", "same-name child own")
}

func TestReportExplicitComparisonsUseIndependentPeriods(t *testing.T) {
	in := reportFixtureFiscal()
	q := r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2026-04-01", To: "2026-04-30",
		Comparison: "explicit", CompareFrom: "2026-03-01", CompareTo: "2026-03-31"}
	out := reportFixtureBuild(t, in, q)
	income, cost := reportFixtureFindAccount(t, out, "income"), reportFixtureFindAccount(t, out, "cost")
	for _, tc := range []struct{ got, expected, field string }{
		{income.Values["current"], "900", "current income"}, {income.Values["prior"], "800", "prior income"},
		{income.Values["variance"], "100", "income variance"}, {cost.Values["current"], "400", "current cost"},
		{cost.Values["prior"], "300", "prior cost"}, {cost.Values["variance"], "100", "cost variance"},
	} {
		reportFixtureEqual(t, tc.got, tc.expected, tc.field)
	}
}

func TestReportDeterministicPermutationAndCSVDoNotMutateResult(t *testing.T) {
	in, q := reportFixtureHierarchy(), reportFixtureRequest(r.BalanceSheet)
	out := reportFixtureBuild(t, in, q)
	repeated := reportFixtureBuild(t, in, q)
	if !reflect.DeepEqual(out, repeated) {
		t.Error("identical explicit input is nondeterministic")
	}
	reordered := reportFixtureClone(in)
	sort.Slice(reordered.Accounts, func(i, j int) bool { return reordered.Accounts[i].ID > reordered.Accounts[j].ID })
	sort.Slice(reordered.Postings, func(i, j int) bool { return reordered.Postings[i].ID > reordered.Postings[j].ID })
	permuted := reportFixtureBuild(t, reordered, q)
	reportFixtureEqual(t, permuted.Reconciliation["assets"], "8750", "permuted assets")
	reportFixtureEqual(t, permuted.Reconciliation["equity_total"], "8750", "permuted equity")
	before, _ := json.Marshal(out)
	_, err := r.CSV(out)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(out)
	if !bytes.Equal(before, after) {
		t.Error("CSV serialization mutated report result")
	}
}

func TestReportCSVIncludesRevisionAndSafeText(t *testing.T) {
	in := reportFixtureHierarchy()
	in.Accounts[0].Name = "=1+1"
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	data, err := r.CSV(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{out.Digest, out.Scope.LedgerRevision, out.Scope.MappingVersion, "2026-01-31"} {
		if required == "" || !bytes.Contains(data, []byte(required)) {
			t.Error("CSV omitted declared report identity/scope")
		}
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	protected := false
	for _, fields := range records {
		for _, field := range fields {
			if field == "=1+1" {
				t.Error("CSV exposed executable synthetic label")
			}
			if strings.HasPrefix(field, "'") && strings.TrimSpace(strings.TrimPrefix(field, "'")) == "=1+1" {
				protected = true
			}
		}
	}
	if !protected {
		t.Error("CSV missing spreadsheet-safe literal label")
	}
}

func TestReportContributionConservationMatchesReturnedAccountValue(t *testing.T) {
	in, q := reportFixtureHierarchy(), reportFixtureRequest(r.BalanceSheet)
	out := reportFixtureBuild(t, in, q)
	for _, row := range reportFixtureRows(out.Rows) {
		if row.Role != "account" || row.Drilldown == nil {
			continue
		}
		parts, err := r.Contributions(in, out, row.ID, "current")
		if err != nil {
			t.Fatal(err)
		}
		sum := new(big.Int)
		for _, part := range parts.Contributions {
			n, ok := new(big.Int).SetString(part.SignedAmount, 10)
			if !ok {
				t.Fatal("noninteger contribution")
			}
			sum.Add(sum, n)
		}
		if row.AccountID == "capital" {
			sum.Neg(sum)
		}
		reportFixtureEqual(t, sum.String(), row.Values["current"], "account contribution conservation")
		reportFixtureEqual(t, parts.ResultDigest, out.Digest, "contribution revision")
	}
}

func TestReportGeneralLedgerOpeningMovementClosingAndJournalPopulation(t *testing.T) {
	in := reportFixtureHierarchy()
	q := r.Request{BookID: in.Book.ID, Report: r.GeneralLedger, From: "2026-01-10", To: "2026-01-31", AccountIDs: []string{"cash"}}
	out := reportFixtureBuild(t, in, q)
	cash := reportFixtureFindAccount(t, out, "cash")
	reportFixtureEqual(t, cash.Values["debit"], "800", "selected GL debits")
	reportFixtureEqual(t, cash.Values["credit"], "300", "selected GL credits")
	reportFixtureEqual(t, cash.Values["balance"], "3250", "selected GL closing")
	var opening, closing string
	postingIDs := []string{}
	for _, row := range cash.Children {
		switch row.Role {
		case "opening":
			opening = row.Values["balance"]
		case "closing":
			closing = row.Values["balance"]
		case "posting":
			postingIDs = append(postingIDs, row.PostingID)
		}
	}
	reportFixtureEqual(t, opening, "2750", "GL opening")
	reportFixtureEqual(t, closing, "3250", "GL closing")
	sort.Strings(postingIDs)
	if !reflect.DeepEqual(postingIDs, []string{"cost-c", "sale-d"}) {
		t.Errorf("GL selected posting identity population: got %v", postingIDs)
	}
	journal := reportFixtureBuild(t, in, reportFixtureRequest(r.Journal))
	reportFixtureEqual(t, journal.Reconciliation["debit"], "9850", "gross Journal debits")
	reportFixtureEqual(t, journal.Reconciliation["credit"], "9850", "gross Journal credits")
	seen := map[string]bool{}
	for _, row := range reportFixtureRows(journal.Rows) {
		if row.Role == "posting" {
			if seen[row.PostingID] {
				t.Error("Journal repeated a posting identity")
			}
			seen[row.PostingID] = true
		}
	}
	if len(seen) != 14 {
		t.Errorf("Journal retained postings: got %d, want 14", len(seen))
	}
}

func TestReportStaleAndTamperedContributionResultRejected(t *testing.T) {
	in, q := reportFixtureHierarchy(), reportFixtureRequest(r.BalanceSheet)
	out := reportFixtureBuild(t, in, q)
	row := reportFixtureFindAccount(t, out, "cash")
	changed := reportFixtureClone(in)
	reportFixturePair(&changed, "late-correction", "2026-01-15", "income", "cash", "100", "ordinary")
	changed.Provenance.LedgerRevision = "ledger-v2"
	if _, err := r.Contributions(changed, out, row.ID, "current"); err == nil {
		t.Error("stale result was explained using changed live population")
	}
	tampered := out
	tampered.Reconciliation = map[string]string{"assets": "1"}
	if _, err := r.Contributions(in, tampered, row.ID, "current"); err == nil {
		t.Error("altered result accepted under unchanged digest")
	}
	if _, err := r.Contributions(in, out, "account:absent", "current"); err == nil {
		t.Error("unknown contribution row accepted")
	}
	if _, err := r.Contributions(in, out, row.ID, "absent-column"); err == nil {
		t.Error("unknown contribution column accepted")
	}
}

func TestReportCalendarComparisonClampsLeapDayAndUsesWholeMonth(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", "")}
	position := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.BalanceSheet, AsOf: "2024-02-29", Comparison: "previous_year"})
	if position.Scope.Comparison == nil {
		t.Fatal("missing comparison scope")
	}
	reportFixtureEqual(t, position.Scope.Comparison.To, "2023-02-28", "prior-year leap-date position")
	period := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2026-03-01", To: "2026-03-31", Comparison: "previous_period"})
	if period.Scope.Comparison == nil {
		t.Fatal("missing previous period")
	}
	reportFixtureEqual(t, period.Scope.Comparison.From, "2026-02-01", "previous calendar-month start")
	reportFixtureEqual(t, period.Scope.Comparison.To, "2026-02-28", "previous calendar-month end")
	monthEnd := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.BalanceSheet, AsOf: "2026-09-30", Comparison: "previous_period"})
	reportFixtureEqual(t, monthEnd.Scope.Comparison.To, "2026-08-31", "previous full-month-end position")
	leapMonth := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2023-02-01", To: "2023-02-28", Comparison: "previous_year"})
	reportFixtureEqual(t, leapMonth.Scope.Comparison.To, "2022-02-28", "previous-year full February end")
	postLeapMonth := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2025-02-01", To: "2025-02-28", Comparison: "previous_year"})
	reportFixtureEqual(t, postLeapMonth.Scope.Comparison.To, "2024-02-29", "previous-year leap February retains full month")
}

func TestReportZeroPopulationDoesNotCertifyMissingCoverage(t *testing.T) {
	in := reportFixtureBase()
	in.Provenance.Coverage = "unknown"
	in.Provenance.OpeningBalances = "unknown"
	in.Book.RecognitionBasis = "unknown"
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	reportFixtureEqual(t, out.Reconciliation["assets"], "0", "empty supplied assets")
	reportFixtureEqual(t, out.Reconciliation["equity_total"], "0", "empty supplied equity")
	reportFixtureEqual(t, out.Status, "partial", "empty population remains unconfirmed")
	for _, id := range []string{"source_coverage", "opening_balances", "recognition_basis"} {
		found := false
		for _, check := range out.Checks {
			if check.ID == id && check.Status == "unknown" {
				found = true
			}
		}
		if !found {
			t.Errorf("empty population silently confirmed %s", id)
		}
	}
}

func TestReportUnclassifiedAmountStaysVisibleAndUnqualified(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("unmapped", "unknown", ""), reportFixtureAccount("capital", "equity", "")}
	reportFixturePair(&in, "unmapped-opening", "2026-01-01", "unmapped", "capital", "1000", "opening")
	out := reportFixtureBuild(t, in, reportFixtureRequest(r.BalanceSheet))
	reportFixtureEqual(t, out.Status, "partial", "unclassified population")
	row := reportFixtureFindAccount(t, out, "unmapped")
	reportFixtureEqual(t, row.Values["current"], "1000", "unclassified balance remains visible")
	found := false
	for _, check := range out.Checks {
		if check.ID == "account_classification" && check.Status == "fail" {
			found = true
		}
	}
	if !found {
		t.Error("unclassified financial amount lacks a reportFixture failure")
	}
	parts, err := r.Contributions(in, out, row.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	reportFixtureEqual(t, parts.Total, "1000", "unclassified amount remains explainable")
}

func TestReportCrossClassHierarchyAndInconsistentEntryMetadataRejected(t *testing.T) {
	for name, change := range map[string]func(*r.Input){
		"cross-class hierarchy":    func(in *r.Input) { in.Accounts[7].ParentID = "equipment" },
		"mixed entry dates":        func(in *r.Input) { in.Postings[0].Date = "2026-01-02" },
		"mixed entry purposes":     func(in *r.Input) { in.Postings[0].Treatment = "ordinary" },
		"posting to virtual group": func(in *r.Input) { in.Accounts[1].Virtual = true },
	} {
		t.Run(name, func(t *testing.T) {
			in := reportFixtureHierarchy()
			change(&in)
			if _, err := r.BuildCore(in, reportFixtureRequest(r.BalanceSheet)); err == nil {
				t.Error("invalid accounting population accepted")
			}
		})
	}
}

func TestReportEveryCoreAmountHasConservedContributions(t *testing.T) {
	in := reportFixtureHierarchy()
	for _, kind := range []r.Kind{r.BalanceSheet, r.ProfitLoss, r.TrialBalance, r.GeneralLedger, r.Journal} {
		t.Run(string(kind), func(t *testing.T) {
			out := reportFixtureBuild(t, in, reportFixtureRequest(kind))
			for _, row := range reportFixtureRows(out.Rows) {
				for _, column := range out.Columns {
					expected, exists := row.Values[column.ID]
					if !exists || expected == "0" {
						continue
					}
					parts, err := r.Contributions(in, out, row.ID, column.ID)
					if err != nil {
						t.Errorf("%s/%s contribution failed: %v", row.ID, column.ID, err)
						continue
					}
					reportFixtureEqual(t, parts.Total, expected, row.ID+"/"+column.ID+" projected contributions")
					reportFixtureEqual(t, parts.ResultDigest, out.Digest, "same result revision")
					sum := new(big.Int)
					for _, contribution := range parts.Contributions {
						n, ok := new(big.Int).SetString(contribution.ReportAmount, 10)
						if !ok {
							t.Error("noninteger projected contribution")
							continue
						}
						sum.Add(sum, n)
					}
					reportFixtureEqual(t, sum.String(), expected, "independent sum of projected contributions")
				}
			}
		})
	}
}

func TestReportVarianceContributionsAreSignedByComparisonPeriod(t *testing.T) {
	in := reportFixtureFiscal()
	q := r.Request{BookID: in.Book.ID, Report: r.ProfitLoss, From: "2026-04-01", To: "2026-04-30",
		Comparison: "explicit", CompareFrom: "2026-03-01", CompareTo: "2026-03-31"}
	out := reportFixtureBuild(t, in, q)
	row := reportFixtureFindAccount(t, out, "income")
	parts, err := r.Contributions(in, out, row.ID, "variance")
	if err != nil {
		t.Fatal(err)
	}
	reportFixtureEqual(t, parts.Total, "100", "income variance explanation")
	byPeriod := map[string]*big.Int{"current": new(big.Int), "prior": new(big.Int)}
	for _, contribution := range parts.Contributions {
		n, ok := new(big.Int).SetString(contribution.ReportAmount, 10)
		if !ok || byPeriod[contribution.ColumnID] == nil {
			t.Fatal("variance contribution omitted its exact amount or comparison period")
		}
		byPeriod[contribution.ColumnID].Add(byPeriod[contribution.ColumnID], n)
	}
	reportFixtureEqual(t, byPeriod["current"].String(), "900", "variance current contribution")
	reportFixtureEqual(t, byPeriod["prior"].String(), "-800", "variance comparison contribution")
}

func TestReportUnrecognizedTreatmentNeverBecomesSilentOrdinaryTreatment(t *testing.T) {
	in := reportFixtureHierarchy()
	for i := range in.Postings {
		if in.Postings[i].EntryID == "sale" {
			in.Postings[i].Treatment = "unrecognized-purpose"
		}
	}
	out, err := r.BuildCore(in, reportFixtureRequest(r.ProfitLoss))
	if err != nil {
		// Rejecting an unsupported typed vocabulary is also fail-closed.
		return
	}
	for _, limitation := range out.Limitations {
		if strings.Contains(strings.ToLower(limitation), "purpose") {
			return
		}
	}
	t.Error("unrecognized purpose accepted without an interpretation limitation")
}

func reportFixtureLoan() (r.Input, r.Request, r.CashFlowPolicy) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", ""),
		reportFixtureAccount("loan", "liability", ""), reportFixtureAccount("interest", "expense", "")}
	reportFixturePair(&in, "opening", "2026-01-01", "cash", "capital", "1000", "opening")
	reportFixturePair(&in, "loan-draw", "2026-02-05", "cash", "loan", "1200", "ordinary")
	in.Postings = append(in.Postings,
		reportFixturePosting("repay-principal", "repay", "loan", "2026-02-20", "debit", "900", "ordinary"),
		reportFixturePosting("repay-interest", "repay", "interest", "2026-02-20", "debit", "300", "ordinary"),
		reportFixturePosting("repay-cash", "repay", "cash", "2026-02-20", "credit", "1200", "ordinary"))
	q := r.Request{BookID: in.Book.ID, Report: r.CashFlows, From: "2026-02-01", To: "2026-02-28"}
	policy := r.CashFlowPolicy{CashScopeConfirmed: true, CashAccountIDs: []string{"cash"}, Reference: "synthetic-reviewed-policy", NoncashReviewConfirmed: true,
		Allocations: []r.CashFlowAllocation{
			{PostingID: "loan-draw-d", Amount: "1200", Category: "financing", TreatmentReference: "synthetic-loan-draw"},
			{PostingID: "repay-cash", Amount: "-900", Category: "financing", TreatmentReference: "synthetic-principal"},
			{PostingID: "repay-cash", Amount: "-300", Category: "operating", TreatmentReference: "synthetic-interest-policy"},
		}}
	return in, q, policy
}

func reportFixtureCheckAllContributions(t *testing.T, in r.Input, out r.Result) {
	t.Helper()
	for _, row := range reportFixtureRows(out.Rows) {
		for _, column := range out.Columns {
			expected, exists := row.Values[column.ID]
			if !exists || expected == "0" {
				continue
			}
			parts, err := r.Contributions(in, out, row.ID, column.ID)
			if err != nil {
				t.Errorf("%s/%s contribution failed: %v", row.ID, column.ID, err)
				continue
			}
			reportFixtureEqual(t, parts.Total, expected, "extension exact contribution total")
		}
	}
}

func TestReportCashFlowSplitLoanPolicyAndEveryAmount(t *testing.T) {
	in, q, policy := reportFixtureLoan()
	out, err := r.BuildCashFlows(in, q, policy)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "ready" {
		t.Error("fully specified synthetic cash-flow treatment did not qualify")
	}
	for key, expected := range map[string]string{
		"current_opening": "1000", "current_closing": "1000", "current_operating": "-300",
		"current_investing": "0", "current_financing": "300", "current_bridge_difference": "0",
		"current_unresolved_count": "0", "current_unresolved_gross_minor": "0",
	} {
		reportFixtureEqual(t, out.Reconciliation[key], expected, key)
	}
	reportFixtureCheckAllContributions(t, in, out)
	policy.Allocations = policy.Allocations[:1]
	partial, err := r.BuildCashFlows(in, q, policy)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status == "ready" {
		t.Error("missing split classification presented as fully ready")
	}
	reportFixtureEqual(t, partial.Reconciliation["current_unresolved_count"], "1", "missing payment treatment count")
	reportFixtureEqual(t, partial.Reconciliation["current_unresolved_gross_minor"], "1200", "missing gross payment treatment")
	unsupported, err := r.Build(in, q)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Status != "unsupported" {
		t.Error("unestablished cash scope presented as supported cash-flow statement")
	}
}

func TestReportOffsettingCashUncertaintyCannotDisappear(t *testing.T) {
	in := reportFixtureBase()
	in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", "")}
	reportFixturePair(&in, "opening", "2026-01-01", "cash", "capital", "1000", "opening")
	reportFixturePair(&in, "inflow", "2026-02-10", "cash", "capital", "100", "ordinary")
	reportFixturePair(&in, "outflow", "2026-02-11", "capital", "cash", "100", "ordinary")
	q := r.Request{BookID: in.Book.ID, Report: r.CashFlows, From: "2026-02-01", To: "2026-02-28"}
	out, err := r.BuildCashFlows(in, q, r.CashFlowPolicy{CashScopeConfirmed: true, CashAccountIDs: []string{"cash"}, Reference: "synthetic-scope", NoncashReviewConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status == "ready" {
		t.Error("offsetting unclassified cash movements became ready")
	}
	reportFixtureEqual(t, out.Reconciliation["current_unresolved_count"], "2", "gross unresolved posting count")
	reportFixtureEqual(t, out.Reconciliation["current_unresolved_gross_minor"], "200", "gross unresolved amount")
}

func TestReportEquityRollForwardClosingAndSharedCostClassification(t *testing.T) {
	for _, costKind := range []string{"expense", "cost_of_sales", "cost_of_goods_sold"} {
		t.Run(costKind, func(t *testing.T) {
			in := reportFixtureBase()
			in.Accounts = []r.Account{reportFixtureAccount("cash", "asset", ""), reportFixtureAccount("capital", "equity", ""),
				reportFixtureAccount("retained", "equity", ""), reportFixtureAccount("income", "income", ""), reportFixtureAccount("cost", costKind, "")}
			reportFixturePair(&in, "opening", "2026-01-01", "cash", "capital", "1000", "opening")
			reportFixturePair(&in, "funding", "2026-02-05", "cash", "capital", "200", "ordinary")
			reportFixturePair(&in, "sale", "2026-02-10", "cash", "income", "800", "ordinary")
			reportFixturePair(&in, "cost", "2026-02-11", "cost", "cash", "300", "ordinary")
			reportFixturePair(&in, "distribution", "2026-02-20", "capital", "cash", "100", "ordinary")
			in.Postings = append(in.Postings,
				reportFixturePosting("close-income", "close", "income", "2026-02-28", "debit", "800", "closing"),
				reportFixturePosting("close-cost", "close", "cost", "2026-02-28", "credit", "300", "closing"),
				reportFixturePosting("close-retained", "close", "retained", "2026-02-28", "credit", "500", "closing"))
			q := r.Request{BookID: in.Book.ID, Report: r.ChangesInEquity, From: "2026-02-01", To: "2026-02-28"}
			policy := r.EquityPolicy{ScopeConfirmed: true, Reference: "synthetic-reviewed-equity", Allocations: []r.EquityAllocation{
				{PostingID: "funding-c", Amount: "200", Category: "contribution", TreatmentReference: "synthetic-capital-funding"},
				{PostingID: "distribution-d", Amount: "-100", Category: "distribution", TreatmentReference: "synthetic-owner-distribution"},
			}}
			out, err := r.BuildEquityChanges(in, q, policy)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != "ready" {
				t.Error("fully specified synthetic equity treatment did not qualify")
			}
			for key, expected := range map[string]string{"current_opening": "1000", "current_closing": "1600", "current_profit": "500",
				"current_closing_transfer": "0", "current_bridge_difference": "0", "current_unresolved_count": "0"} {
				reportFixtureEqual(t, out.Reconciliation[key], expected, key)
			}
			reportFixtureCheckAllContributions(t, in, out)
			core := reportFixtureBuild(t, in, r.Request{BookID: in.Book.ID, Report: r.BalanceSheet, AsOf: "2026-02-28"})
			reportFixtureEqual(t, core.Reconciliation["equity_total"], "1600", "independent Balance Sheet equity")
			reportFixtureEqual(t, core.Reconciliation["current_unclosed_result"], "0", "closed earnings residual")
		})
	}
}

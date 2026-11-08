package reporting

import (
	"encoding/json"
	"math/big"
	"slices"
	"testing"
)

// These independently specified fixtures are synthetic and contain no source
// organization labels, records, provider identifiers or private paths.
func extensionFixture() Input {
	in := Input{Book: Book{ID: "book-example", Name: "Example Book", Currency: "USD", Scale: 2, StartDate: "2025-01-01", FiscalStartMonth: 1, FiscalStartDay: 1, RecognitionBasis: "accrual", ReportingFramework: "unqualified"}, Accounts: []Account{{ID: "cash", Code: "100", Name: "Account A", Kind: "asset"}, {ID: "cash2", Code: "101", Name: "Account B", Kind: "asset"}, {ID: "equipment", Code: "120", Name: "Account C", Kind: "asset"}, {ID: "loan", Code: "200", Name: "Account D", Kind: "liability"}, {ID: "equity", Code: "300", Name: "Account E", Kind: "equity"}, {ID: "equity2", Code: "301", Name: "Account F", Kind: "equity"}, {ID: "revenue", Code: "400", Name: "Account G", Kind: "revenue"}, {ID: "expense", Code: "500", Name: "Account H", Kind: "expense"}, {ID: "cost", Code: "501", Name: "Account I", Kind: "cost_of_goods_sold"}}, Provenance: Provenance{LedgerRevision: "revision-1", KnowledgeCutoff: "2026-02-01T00:00:00Z", MappingVersion: "mapping-1", HierarchyBasis: "explicit_current", GeneratedAt: "2026-02-01T00:00:00Z", Coverage: "complete", OpeningBalances: "confirmed"}}
	extensionEntry(&in, "opening", "2025-01-01", "opening", extensionLeg{"cash", "500000"}, extensionLeg{"equity", "-500000"})
	return in
}

type extensionLeg struct{ account, amount string }

func extensionEntry(in *Input, id, date, purpose string, legs ...extensionLeg) []string {
	ids := []string{}
	for i, leg := range legs {
		n, _ := new(big.Int).SetString(leg.amount, 10)
		side := "debit"
		if n.Sign() < 0 {
			side = "credit"
			n.Neg(n)
		}
		postingID := id + ":" + big.NewInt(int64(i+1)).String()
		in.Postings = append(in.Postings, Posting{ID: postingID, EntryID: id, BookID: in.Book.ID, AccountID: leg.account, Date: date, Side: side, Amount: n.String(), Currency: in.Book.Currency, Scale: in.Book.Scale, Treatment: purpose})
		ids = append(ids, postingID)
	}
	return ids
}
func extensionRequest(kind Kind) Request {
	return Request{BookID: "book-example", Report: kind, From: "2026-01-01", To: "2026-01-31"}
}
func extensionFind(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
		if child, ok := extensionFind(r.Children, id); ok {
			return child, true
		}
	}
	return Row{}, false
}
func extensionAssertValue(t *testing.T, r Result, id, column, want string) {
	t.Helper()
	row, ok := extensionFind(r.Rows, id)
	if !ok {
		t.Fatalf("missing synthetic row %s", id)
	}
	if row.Values[column] != want {
		t.Fatalf("row %s column %s: got %s want %s", id, column, row.Values[column], want)
	}
}
func extensionCheckStatus(r Result, id string) string {
	for _, check := range r.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return "missing"
}
func extensionPolicy() CashFlowPolicy {
	return CashFlowPolicy{CashScopeConfirmed: true, CashAccountIDs: []string{"cash", "cash2"}, Reference: "review-policy-1", NoncashReviewConfirmed: true}
}

func TestExtensionDefaultDispatchDoesNotManufactureFormalScope(t *testing.T) {
	in := extensionFixture()
	r, err := Build(in, extensionRequest(CashFlows))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "unsupported" || len(r.Rows) != 0 {
		t.Fatal("unknown cash scope must not produce zero-filled formal totals")
	}
	extensionEntry(&in, "funding", "2026-01-02", "ordinary", extensionLeg{"cash", "100000"}, extensionLeg{"equity", "-100000"})
	r, err = Build(in, extensionRequest(ChangesInEquity))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" {
		t.Fatal("missing equity treatment must remain partial")
	}
	extensionAssertValue(t, r, "equity:opening", "current", "500000")
	extensionAssertValue(t, r, "equity:closing", "current", "600000")
	extensionAssertValue(t, r, "equity:unresolved", "current", "100000")
	if extensionCheckStatus(r, "equity_bridge") != "pass" || r.Reconciliation["current_unresolved_count"] != "1" {
		t.Fatal("bridge and treatment coverage must remain separate")
	}
}

func TestExtensionSplitLoanPrincipalInterestAndExactContribution(t *testing.T) {
	in := extensionFixture()
	loan := extensionEntry(&in, "loan", "2026-01-05", "ordinary", extensionLeg{"cash", "1000000"}, extensionLeg{"loan", "-1000000"})
	receipt := extensionEntry(&in, "receipt", "2026-01-06", "ordinary", extensionLeg{"cash", "200000"}, extensionLeg{"revenue", "-200000"})
	payment := extensionEntry(&in, "payment", "2026-01-07", "ordinary", extensionLeg{"cash", "-120000"}, extensionLeg{"loan", "90000"}, extensionLeg{"expense", "30000"})
	policy := extensionPolicy()
	policy.Allocations = []CashFlowAllocation{{PostingID: loan[0], Amount: "1000000", Category: "financing", TreatmentReference: "review-loan"}, {PostingID: receipt[0], Amount: "200000", Category: "operating", TreatmentReference: "review-receipt"}, {PostingID: payment[0], Amount: "-90000", Category: "financing", TreatmentReference: "review-principal"}, {PostingID: payment[0], Amount: "-30000", Category: "operating", TreatmentReference: "review-interest"}}
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatalf("qualified synthetic scope expected ready, got %s", r.Status)
	}
	for id, want := range map[string]string{"opening": "500000", "operating": "170000", "financing": "910000", "net": "1080000", "closing": "1580000", "unresolved": "0"} {
		extensionAssertValue(t, r, "cash_flow:"+id, "current", want)
	}
	contributions, err := Contributions(in, r, "cash_flow:financing", "current")
	if err != nil {
		t.Fatal(err)
	}
	sum := new(big.Int)
	splitFound := false
	for _, c := range contributions.Contributions {
		n, _ := new(big.Int).SetString(c.ReportAmount, 10)
		sum.Add(sum, n)
		if c.PostingID == payment[0] {
			splitFound = c.Amount == "120000" && c.SignedAmount == "-120000" && c.ReportAmount == "-90000" && c.TreatmentReference == "review-principal"
		}
	}
	if sum.String() != "910000" || !splitFound {
		t.Fatal("projected contribution must retain untouched source amount and explicit split")
	}
}

func TestExtensionZeroNetCompoundEntryRetainsGrossUnresolvedCash(t *testing.T) {
	in := extensionFixture()
	ids := extensionEntry(&in, "compound", "2026-01-10", "ordinary", extensionLeg{"cash", "100000"}, extensionLeg{"loan", "-100000"}, extensionLeg{"equipment", "100000"}, extensionLeg{"cash", "-100000"})
	policy := extensionPolicy()
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" || r.Reconciliation["current_unresolved_count"] != "2" || r.Reconciliation["current_unresolved_gross_minor"] != "200000" {
		t.Fatal("zero-net gross cash must remain unresolved")
	}
	extensionAssertValue(t, r, "cash_flow:unresolved", "current", "0")
	if extensionCheckStatus(r, "cash_flow_bridge") != "pass" {
		t.Fatal("arithmetic bridge should still pass")
	}
	policy.Allocations = []CashFlowAllocation{{PostingID: ids[0], Amount: "100000", Category: "financing", TreatmentReference: "review-funding"}, {PostingID: ids[3], Amount: "-100000", Category: "investing", TreatmentReference: "review-equipment"}}
	r, err = BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatal("fully reviewed synthetic components should qualify")
	}
	extensionAssertValue(t, r, "cash_flow:financing", "current", "100000")
	extensionAssertValue(t, r, "cash_flow:investing", "current", "-100000")
}

func TestExtensionInternalTransfersAndExchangeAreSeparate(t *testing.T) {
	in := extensionFixture()
	transfer := extensionEntry(&in, "transfer", "2026-01-11", "ordinary", extensionLeg{"cash", "-100000"}, extensionLeg{"cash2", "100000"})
	fx := extensionEntry(&in, "exchange", "2026-01-12", "ordinary", extensionLeg{"cash", "5000"}, extensionLeg{"revenue", "-5000"})
	policy := extensionPolicy()
	policy.Allocations = []CashFlowAllocation{{PostingID: transfer[0], Amount: "-100000", Category: "internal_transfer", TreatmentReference: "review-transfer", TransferGroup: "transfer-group"}, {PostingID: transfer[1], Amount: "100000", Category: "internal_transfer", TreatmentReference: "review-transfer", TransferGroup: "transfer-group"}, {PostingID: fx[0], Amount: "5000", Category: "exchange", TreatmentReference: "review-exchange"}}
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatal("reviewed transfer/FX fixture should qualify")
	}
	extensionAssertValue(t, r, "cash_flow:net", "current", "0")
	extensionAssertValue(t, r, "cash_flow:exchange", "current", "5000")
	extensionAssertValue(t, r, "cash_flow:closing", "current", "505000")
	policy.Allocations = policy.Allocations[1:]
	r, err = BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" || extensionCheckStatus(r, "cash_flow_internal_transfers") == "pass" {
		t.Fatal("an unmatched transfer component cannot be complete")
	}
}

func TestExtensionCashAllocationRejectsUnsupportedAuthorityAndOverallocation(t *testing.T) {
	in := extensionFixture()
	ids := extensionEntry(&in, "receipt", "2026-01-03", "ordinary", extensionLeg{"cash", "100000"}, extensionLeg{"revenue", "-100000"})
	for _, bad := range []CashFlowAllocation{{PostingID: ids[0], Amount: "100001", Category: "operating", TreatmentReference: "review"}, {PostingID: ids[0], Amount: "-100000", Category: "operating", TreatmentReference: "review"}, {PostingID: ids[1], Amount: "-100000", Category: "operating", TreatmentReference: "review"}, {PostingID: ids[0], Amount: "100000", Category: "operating"}, {PostingID: ids[0], Amount: "100000", Category: "unsupported", TreatmentReference: "review"}, {PostingID: ids[0], Amount: "100000", Category: "internal_transfer", TreatmentReference: "review"}} {
		policy := extensionPolicy()
		policy.Allocations = []CashFlowAllocation{bad}
		if _, err := BuildCashFlows(in, extensionRequest(CashFlows), policy); err == nil {
			t.Fatal("invalid synthetic allocation accepted")
		}
	}
}

func TestExtensionNoncashDisclosuresDoNotEnterCashOrFalseGrandTotal(t *testing.T) {
	in := extensionFixture()
	ids := extensionEntry(&in, "noncash", "2026-01-04", "ordinary", extensionLeg{"equipment", "100000"}, extensionLeg{"loan", "-100000"})
	policy := extensionPolicy()
	policy.Noncash = []NoncashActivity{{EntryID: "noncash", PostingID: ids[0], Amount: "100000", Category: "investing", TreatmentReference: "review-asset"}, {EntryID: "noncash", PostingID: ids[1], Amount: "100000", Category: "financing", TreatmentReference: "review-debt"}}
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatal("reviewed synthetic noncash scope should qualify")
	}
	extensionAssertValue(t, r, "cash_flow:net", "current", "0")
	extensionAssertValue(t, r, "cash_flow:closing", "current", "500000")
	section, _ := extensionFind(r.Rows, "cash_flow:noncash")
	if len(section.Values) != 0 {
		t.Fatal("two aspects of noncash activity must not become a misleading summed total")
	}
	for _, category := range []string{"investing", "financing"} {
		contributions, err := Contributions(in, r, "cash_flow:noncash_"+category, "current")
		if err != nil {
			t.Fatal(err)
		}
		if len(contributions.Contributions) != 1 || contributions.Contributions[0].ReportAmount != "100000" {
			t.Fatal("noncash disclosure must retain exact posting explanation")
		}
	}
	policy.Noncash = append(policy.Noncash, policy.Noncash[0])
	if _, err := BuildCashFlows(in, extensionRequest(CashFlows), policy); err == nil {
		t.Fatal("duplicated noncash disclosure accepted")
	}
}

func TestExtensionCashComparisonAndVariancePreserveAllocatedAmounts(t *testing.T) {
	in := extensionFixture()
	prior := extensionEntry(&in, "prior", "2025-12-15", "ordinary", extensionLeg{"cash", "25000"}, extensionLeg{"revenue", "-25000"})
	current := extensionEntry(&in, "current", "2026-01-15", "ordinary", extensionLeg{"cash", "50000"}, extensionLeg{"revenue", "-50000"})
	policy := extensionPolicy()
	policy.Allocations = []CashFlowAllocation{{PostingID: prior[0], Amount: "25000", Category: "operating", TreatmentReference: "review-prior"}, {PostingID: current[0], Amount: "50000", Category: "operating", TreatmentReference: "review-current"}}
	request := extensionRequest(CashFlows)
	request.Comparison = "previous_period"
	r, err := BuildCashFlows(in, request, policy)
	if err != nil {
		t.Fatal(err)
	}
	extensionAssertValue(t, r, "cash_flow:operating", "current", "50000")
	extensionAssertValue(t, r, "cash_flow:operating", "prior", "25000")
	extensionAssertValue(t, r, "cash_flow:operating", "variance", "25000")
	contributions, err := Contributions(in, r, "cash_flow:operating", "variance")
	if err != nil {
		t.Fatal(err)
	}
	sum := new(big.Int)
	for _, c := range contributions.Contributions {
		n, _ := new(big.Int).SetString(c.ReportAmount, 10)
		sum.Add(sum, n)
	}
	if sum.String() != "25000" {
		t.Fatal("variance needs signed contributions from both periods")
	}
}

func TestExtensionCashReviewAndPolicyRevisionAreIndependentGates(t *testing.T) {
	in := extensionFixture()
	policy := extensionPolicy()
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Reference = "review-policy-2"
	amended, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if amended.Digest == r.Digest {
		t.Fatal("policy revision must change result identity even with identical figures")
	}
	policy.NoncashReviewConfirmed = false
	r, err = BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" || extensionCheckStatus(r, "cash_flow_bridge") != "pass" {
		t.Fatal("missing noncash review cannot be upgraded by arithmetic bridge")
	}
	policy.CashScopeConfirmed = false
	r, err = BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "unsupported" || len(r.Rows) != 0 {
		t.Fatal("unknown cash scope must not show invented balances")
	}
}

func TestExtensionEquityClosingProfitIsCountedOnce(t *testing.T) {
	in := extensionFixture()
	extensionEntry(&in, "income", "2026-01-03", "ordinary", extensionLeg{"cash", "100000"}, extensionLeg{"revenue", "-100000"})
	extensionEntry(&in, "cost", "2026-01-04", "ordinary", extensionLeg{"cost", "20000"}, extensionLeg{"cash", "-20000"})
	extensionEntry(&in, "close", "2026-01-31", "closing", extensionLeg{"revenue", "100000"}, extensionLeg{"cost", "-20000"}, extensionLeg{"equity", "-80000"})
	fund := extensionEntry(&in, "fund", "2026-01-06", "ordinary", extensionLeg{"cash", "50000"}, extensionLeg{"equity", "-50000"})
	draw := extensionEntry(&in, "draw", "2026-01-07", "ordinary", extensionLeg{"equity", "10000"}, extensionLeg{"cash", "-10000"})
	policy := EquityPolicy{ScopeConfirmed: true, Reference: "review-equity", Allocations: []EquityAllocation{{PostingID: fund[1], Amount: "50000", Category: "contribution", TreatmentReference: "review-fund"}, {PostingID: draw[0], Amount: "-10000", Category: "distribution", TreatmentReference: "review-draw"}}}
	r, err := BuildEquityChanges(in, extensionRequest(ChangesInEquity), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatalf("qualified synthetic equity expected ready, got %s", r.Status)
	}
	for id, want := range map[string]string{"opening": "500000", "profit": "80000", "contribution": "50000", "distribution": "-10000", "closing_transfer": "0", "closing": "620000"} {
		extensionAssertValue(t, r, "equity:"+id, "current", want)
	}
	if extensionCheckStatus(r, "equity_bridge") != "pass" || extensionCheckStatus(r, "equity_closing_transfers") != "pass" {
		t.Fatal("closing bridge failed")
	}
	contributions, err := Contributions(in, r, "equity:closing_transfer", "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(contributions.Contributions) != 3 {
		t.Fatal("closing gross movements must remain explainable despite zero net")
	}
}

func TestExtensionUnknownZeroNetEquityMovementRemainsPartial(t *testing.T) {
	in := extensionFixture()
	ids := extensionEntry(&in, "move", "2026-01-08", "ordinary", extensionLeg{"equity", "1000"}, extensionLeg{"equity2", "-1000"})
	policy := EquityPolicy{ScopeConfirmed: true, Reference: "review-equity"}
	r, err := BuildEquityChanges(in, extensionRequest(ChangesInEquity), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" || r.Reconciliation["current_unresolved_count"] != "2" || r.Reconciliation["current_unresolved_gross_minor"] != "2000" {
		t.Fatal("zero-net unknown equity transfers must not disappear")
	}
	policy.Allocations = []EquityAllocation{{PostingID: ids[0], Amount: "-1000", Category: "transfer", TreatmentReference: "review-move"}, {PostingID: ids[1], Amount: "1000", Category: "transfer", TreatmentReference: "review-move"}}
	r, err = BuildEquityChanges(in, extensionRequest(ChangesInEquity), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" {
		t.Fatal("explicit zero-change equity transfer should qualify in bounded scope")
	}
	contributions, err := Contributions(in, r, "equity:transfer", "current")
	if err != nil {
		t.Fatal(err)
	}
	if len(contributions.Contributions) != 2 {
		t.Fatal("transfer contribution multiplicity lost")
	}
}

func TestExtensionUnreviewedTreatmentAndMalformedClosingCannotClaimReady(t *testing.T) {
	in := extensionFixture()
	ids := extensionEntry(&in, "fund", "2026-01-08", "ordinary", extensionLeg{"cash", "1000"}, extensionLeg{"equity", "-1000"})
	policy := EquityPolicy{ScopeConfirmed: true, Reference: "review-equity", Allocations: []EquityAllocation{{PostingID: ids[1], Amount: "1000", Category: "contribution"}}}
	if _, err := BuildEquityChanges(in, extensionRequest(ChangesInEquity), policy); err == nil {
		t.Fatal("missing reviewed reference accepted")
	}
	in = extensionFixture()
	extensionEntry(&in, "invalid-close", "2026-01-31", "closing", extensionLeg{"cash", "1000"}, extensionLeg{"equity", "-1000"})
	r, err := BuildEquityChanges(in, extensionRequest(ChangesInEquity), EquityPolicy{ScopeConfirmed: true, Reference: "review-equity"})
	if err == nil && r.Status == "ready" {
		t.Fatal("closing label cannot disguise a cash/equity change")
	}
}

func TestExtensionOpeningEntriesAtPeriodStartArePositionsNotCashFlows(t *testing.T) {
	in := extensionFixture()
	in.Postings = nil
	extensionEntry(&in, "start", "2026-01-01", "opening", extensionLeg{"cash", "12345"}, extensionLeg{"equity", "-12345"})
	cash, err := BuildCashFlows(in, extensionRequest(CashFlows), extensionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	extensionAssertValue(t, cash, "cash_flow:opening", "current", "12345")
	extensionAssertValue(t, cash, "cash_flow:net", "current", "0")
	extensionAssertValue(t, cash, "cash_flow:closing", "current", "12345")
	equity, err := BuildEquityChanges(in, extensionRequest(ChangesInEquity), EquityPolicy{ScopeConfirmed: true, Reference: "review-equity"})
	if err != nil {
		t.Fatal(err)
	}
	extensionAssertValue(t, equity, "equity:opening", "current", "12345")
	extensionAssertValue(t, equity, "equity:closing", "current", "12345")
}

func TestExtensionInputAndPolicyPermutationIsReadOnlyAndDeterministic(t *testing.T) {
	in := extensionFixture()
	a := extensionEntry(&in, "one", "2026-01-02", "ordinary", extensionLeg{"cash", "90071992547409930000"}, extensionLeg{"loan", "-90071992547409930000"})
	b := extensionEntry(&in, "two", "2026-01-03", "ordinary", extensionLeg{"cash2", "500"}, extensionLeg{"revenue", "-500"})
	policy := extensionPolicy()
	policy.Allocations = []CashFlowAllocation{{PostingID: a[0], Amount: "90071992547409930000", Category: "financing", TreatmentReference: "review-one"}, {PostingID: b[0], Amount: "500", Category: "operating", TreatmentReference: "review-two"}}
	before, _ := json.Marshal(struct {
		In     Input
		Policy CashFlowPolicy
	}{in, policy})
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(struct {
		In     Input
		Policy CashFlowPolicy
	}{in, policy})
	if string(before) != string(after) {
		t.Fatal("extension mutated caller data")
	}
	slices.Reverse(in.Accounts)
	slices.Reverse(in.Postings)
	slices.Reverse(policy.CashAccountIDs)
	slices.Reverse(policy.Allocations)
	reordered, err := BuildCashFlows(in, extensionRequest(CashFlows), policy)
	if err != nil {
		t.Fatal(err)
	}
	if r.Digest != reordered.Digest {
		t.Fatal("input or policy permutation changed report")
	}
	extensionAssertValue(t, r, "cash_flow:financing", "current", "90071992547409930000")
}

func TestExtensionCoverageUnknownClassificationAndCurrencyStayExplicit(t *testing.T) {
	in := extensionFixture()
	in.Provenance.Coverage = "partial"
	r, err := BuildCashFlows(in, extensionRequest(CashFlows), extensionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" {
		t.Fatal("source coverage cannot become complete from empty cash movements")
	}
	in = extensionFixture()
	in.Accounts = append(in.Accounts, Account{ID: "unknown", Code: "900", Name: "Account J", Kind: "unknown"})
	extensionEntry(&in, "unknown-class", "2026-01-02", "ordinary", extensionLeg{"equipment", "1000"}, extensionLeg{"unknown", "-1000"})
	r, err = BuildEquityChanges(in, extensionRequest(ChangesInEquity), EquityPolicy{ScopeConfirmed: true, Reference: "review-equity"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "partial" || extensionCheckStatus(r, "extension_account_classification") != "unknown" {
		t.Fatal("unknown account classification was hidden")
	}
	request := extensionRequest(CashFlows)
	request.Currency = "EUR"
	if _, err := BuildCashFlows(extensionFixture(), request, extensionPolicy()); err == nil {
		t.Fatal("arbitrary reporting currency accepted")
	}
}

func TestExtensionEveryDisplayedValueHasExactRecordedOrAllocatedExplanation(t *testing.T) {
	in := extensionFixture()
	extensionEntry(&in, "sale", "2026-01-03", "ordinary", extensionLeg{"cash", "70000"}, extensionLeg{"revenue", "-70000"})
	extensionEntry(&in, "unclassified-funding", "2026-01-04", "ordinary", extensionLeg{"cash", "30000"}, extensionLeg{"equity", "-30000"})
	request := extensionRequest(CashFlows)
	request.Comparison = "previous_period"
	cash, err := BuildCashFlows(in, request, extensionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	request.Report = ChangesInEquity
	equity, err := BuildEquityChanges(in, request, EquityPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []Result{cash, equity} {
		var visit func([]Row)
		visit = func(rows []Row) {
			for _, row := range rows {
				for column, want := range row.Values {
					contributions, err := Contributions(in, report, row.ID, column)
					if err != nil {
						t.Fatalf("synthetic extension explanation failed for %s/%s: %v", row.ID, column, err)
					}
					if contributions.Total != want {
						t.Fatalf("synthetic extension explanation total differs for %s/%s", row.ID, column)
					}
				}
				visit(row.Children)
			}
		}
		visit(report.Rows)
		if _, err := CSV(report); err != nil {
			t.Fatal(err)
		}
	}
}

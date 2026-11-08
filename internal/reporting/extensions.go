package reporting

import (
	"math/big"
	"sort"
	"strings"
)

// EquityAllocation is reviewed treatment of an equity posting component. Its
// amount is the projected equity change (credit positive), not a new posting.
// The adapter must establish review authority before supplying this policy.
type EquityAllocation struct {
	PostingID          string `json:"posting_id"`
	Amount             string `json:"amount_minor"`
	Category           string `json:"category"` // contribution, distribution, restatement, transfer, other
	TreatmentReference string `json:"treatment_reference"`
}

type EquityPolicy struct {
	ScopeConfirmed bool               `json:"scope_confirmed"`
	Reference      string             `json:"reference"`
	Allocations    []EquityAllocation `json:"allocations"`
}

// CashFlowAllocation binds a reviewed component to an actual cash posting.
// Amount is signed cash movement, debit positive. Several components can explain
// a split payment, but each posting's allocated amount cannot exceed its amount.
type CashFlowAllocation struct {
	PostingID          string `json:"posting_id"`
	Amount             string `json:"amount_minor"`
	Category           string `json:"category"` // operating, investing, financing, exchange, internal_transfer
	TreatmentReference string `json:"treatment_reference"`
	TransferGroup      string `json:"transfer_group,omitempty"`
}

// NoncashActivity is separately reviewed investing/financing information. Its
// amount never enters cash-flow totals. A disclosure must bind a balanced,
// noncash entry; the policy reference establishes its interpretation, not labels.
type NoncashActivity struct {
	EntryID            string `json:"entry_id"`
	PostingID          string `json:"posting_id"`
	Amount             string `json:"amount_minor"`
	Category           string `json:"category"` // investing or financing
	TreatmentReference string `json:"treatment_reference"`
}

type CashFlowPolicy struct {
	CashScopeConfirmed     bool                 `json:"cash_scope_confirmed"`
	CashAccountIDs         []string             `json:"cash_account_ids"`
	Reference              string               `json:"reference"`
	NoncashReviewConfirmed bool                 `json:"noncash_review_confirmed"`
	Allocations            []CashFlowAllocation `json:"allocations"`
	Noncash                []NoncashActivity    `json:"noncash"`
}

// Build preserves the five core calculations. Formal extension reports receive
// no invented policy: the default equity result is partial, and cash scope is
// unavailable until an authorized adapter supplies explicit reviewed treatment.
func Build(in Input, request Request) (Result, error) {
	switch request.Report {
	case BalanceSheet, ProfitLoss, TrialBalance, GeneralLedger, Journal:
		return BuildCore(in, request)
	case ChangesInEquity:
		return BuildEquityChanges(in, request, EquityPolicy{})
	case CashFlows:
		return BuildCashFlows(in, request, CashFlowPolicy{})
	case CashMovement:
		return BuildCashMovement(in, request)
	default:
		return Result{}, &Error{Code: "unsupported_report"}
	}
}

type extensionPeriod struct {
	column string
	period Period
}
type extensionAmount struct {
	amount      *big.Int
	allocations []Allocation
}
type extensionPeriodResult struct {
	values          map[string]*extensionAmount
	unresolvedCount int
	unresolvedGross *big.Int
	bridge          *big.Int
	transferValid   bool
	closingValid    bool
}

func extensionPeriods(r Result) []extensionPeriod {
	out := []extensionPeriod{{"current", r.Scope.Period}}
	if r.Scope.Comparison != nil {
		out = append(out, extensionPeriod{"prior", *r.Scope.Comparison})
	}
	return out
}
func extensionInteger(value string) (*big.Int, error) {
	if value == "" || strings.TrimSpace(value) != value || value == "-0" || strings.HasPrefix(value, "+") {
		return nil, &Error{Code: "invalid_extension_amount"}
	}
	start := 0
	if value[0] == '-' {
		start = 1
	}
	if start == len(value) {
		return nil, &Error{Code: "invalid_extension_amount"}
	}
	for _, c := range value[start:] {
		if c < '0' || c > '9' {
			return nil, &Error{Code: "invalid_extension_amount"}
		}
	}
	if len(value[start:]) > 1 && value[start] == '0' {
		return nil, &Error{Code: "invalid_extension_amount"}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, &Error{Code: "invalid_extension_amount"}
	}
	return n, nil
}
func extensionSigned(p Posting) *big.Int {
	n, _ := new(big.Int).SetString(p.Amount, 10)
	if p.Side == "credit" {
		n.Neg(n)
	}
	return n
}
func extensionEarnings(kind string) bool {
	return class(kind) == "income" || class(kind) == "expense"
}
func extensionReference(ref string) bool { return strings.TrimSpace(ref) != "" && len(ref) <= 256 }
func extensionEquityPolicyDigest(policy EquityPolicy) string {
	policy.Allocations = append([]EquityAllocation(nil), policy.Allocations...)
	sort.Slice(policy.Allocations, func(i, j int) bool {
		a, b := policy.Allocations[i], policy.Allocations[j]
		return a.PostingID+"\x00"+a.Category+"\x00"+a.Amount+"\x00"+a.TreatmentReference < b.PostingID+"\x00"+b.Category+"\x00"+b.Amount+"\x00"+b.TreatmentReference
	})
	return digest(policy)
}
func extensionCashPolicyDigest(policy CashFlowPolicy) string {
	policy.CashAccountIDs = append([]string(nil), policy.CashAccountIDs...)
	sort.Strings(policy.CashAccountIDs)
	policy.Allocations = append([]CashFlowAllocation(nil), policy.Allocations...)
	sort.Slice(policy.Allocations, func(i, j int) bool {
		a, b := policy.Allocations[i], policy.Allocations[j]
		return a.PostingID+"\x00"+a.Category+"\x00"+a.Amount+"\x00"+a.TreatmentReference+"\x00"+a.TransferGroup < b.PostingID+"\x00"+b.Category+"\x00"+b.Amount+"\x00"+b.TreatmentReference+"\x00"+b.TransferGroup
	})
	policy.Noncash = append([]NoncashActivity(nil), policy.Noncash...)
	sort.Slice(policy.Noncash, func(i, j int) bool {
		a, b := policy.Noncash[i], policy.Noncash[j]
		return a.EntryID+"\x00"+a.PostingID+"\x00"+a.Category+"\x00"+a.TreatmentReference < b.EntryID+"\x00"+b.PostingID+"\x00"+b.Category+"\x00"+b.TreatmentReference
	})
	return digest(policy)
}
func extensionIndex(in Input) (map[string]Account, map[string]Posting, map[string][]Posting) {
	a := map[string]Account{}
	p := map[string]Posting{}
	e := map[string][]Posting{}
	for _, v := range in.Accounts {
		a[v.ID] = v
	}
	for _, v := range in.Postings {
		p[v.ID] = v
		e[v.EntryID] = append(e[v.EntryID], v)
	}
	return a, p, e
}
func extensionNewPeriod(keys ...string) extensionPeriodResult {
	r := extensionPeriodResult{values: map[string]*extensionAmount{}, unresolvedGross: new(big.Int), bridge: new(big.Int), transferValid: true, closingValid: true}
	for _, k := range keys {
		r.values[k] = &extensionAmount{amount: new(big.Int), allocations: []Allocation{}}
	}
	return r
}
func extensionAdd(r *extensionPeriodResult, key string, p Posting, n *big.Int, category, reference string) {
	v := r.values[key]
	v.amount.Add(v.amount, n)
	if n.Sign() != 0 {
		if reference == "" {
			// A ledger reference binds the calculation to recorded facts. It
			// does not turn unresolved treatment into reviewed classification.
			reference = "ledger:recorded"
			if category == "unresolved" {
				reference = "ledger:unresolved_treatment"
			}
		}
		v.allocations = append(v.allocations, Allocation{PostingID: p.ID, Amount: n.String(), Category: category, TreatmentReference: reference})
	}
}
func extensionUnknown(r *extensionPeriodResult, p Posting, n *big.Int) {
	if n.Sign() == 0 {
		return
	}
	r.unresolvedCount++
	r.unresolvedGross.Add(r.unresolvedGross, new(big.Int).Abs(n))
	extensionAdd(r, "unresolved", p, n, "unresolved", "")
}
func extensionContains(period Period, p Posting) bool {
	return p.Date >= period.From && p.Date <= period.To
}
func extensionIsOpening(period Period, p Posting) bool {
	return p.Date < period.From || p.Date == period.From && p.Treatment == "opening"
}

func extensionRow(id, label, role string, periods []extensionPeriod, results []extensionPeriodResult, key string) Row {
	r := Row{ID: id, Label: label, Role: role, Values: map[string]string{}, Children: []Row{}}
	for i, p := range periods {
		v := results[i].values[key]
		r.Values[p.column] = v.amount.String()
		for _, a := range v.allocations {
			a.ColumnID = p.column
			r.Allocations = append(r.Allocations, a)
		}
	}
	if len(results) == 2 {
		r.Values["variance"] = new(big.Int).Sub(results[0].values[key].amount, results[1].values[key].amount).String()
		for i, result := range results {
			for _, a := range result.values[key].allocations {
				a.ColumnID = "variance"
				if i == 1 {
					n, _ := extensionInteger(a.Amount)
					a.Amount = n.Neg(n).String()
				}
				r.Allocations = append(r.Allocations, a)
			}
		}
	}
	sort.SliceStable(r.Allocations, func(i, j int) bool {
		a, b := r.Allocations[i], r.Allocations[j]
		if a.ColumnID != b.ColumnID {
			return a.ColumnID < b.ColumnID
		}
		if a.PostingID != b.PostingID {
			return a.PostingID < b.PostingID
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Amount != b.Amount {
			return a.Amount < b.Amount
		}
		return a.TreatmentReference < b.TreatmentReference
	})
	return r
}
func extensionSumRows(r *Row, children ...Row) {
	r.Children = children
	r.Values = map[string]string{}
	r.Allocations = nil
	for _, child := range children {
		for k, v := range child.Values {
			n, _ := extensionInteger(v)
			prior, _ := extensionInteger(r.Values[k])
			if prior == nil {
				prior = new(big.Int)
			}
			r.Values[k] = prior.Add(prior, n).String()
		}
		r.Allocations = append(r.Allocations, child.Allocations...)
	}
}
func extensionCheck(r *Result, id, status, message string) {
	r.Checks = append(r.Checks, Check{ID: id, Status: status, Message: message})
}
func extensionClassificationCheck(r *Result, in Input) {
	accounts, _, _ := extensionIndex(in)
	end := r.Scope.Period.To
	if r.Scope.Comparison != nil && r.Scope.Comparison.To > end {
		end = r.Scope.Comparison.To
	}
	known := true
	for _, p := range in.Postings {
		if p.Date <= end && p.Amount != "0" && class(accounts[p.AccountID].Kind) == "unknown" {
			known = false
			break
		}
	}
	extensionCheck(r, "extension_account_classification", extensionBoolStatus(known), "Unknown account classifications remain separate from arithmetic reconciliation.")
}
func extensionBoolStatus(ok bool) string {
	if ok {
		return "pass"
	}
	return "unknown"
}
func extensionConstraintStatus(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}
func extensionRecordMetrics(r *Result, periods []extensionPeriod, results []extensionPeriodResult) {
	if r.Reconciliation == nil {
		r.Reconciliation = map[string]string{}
	}
	for i, p := range periods {
		prefix := p.column + "_"
		for k, v := range results[i].values {
			r.Reconciliation[prefix+k] = v.amount.String()
		}
		r.Reconciliation[prefix+"unresolved_count"] = big.NewInt(int64(results[i].unresolvedCount)).String()
		r.Reconciliation[prefix+"unresolved_gross_minor"] = results[i].unresolvedGross.String()
		r.Reconciliation[prefix+"bridge_difference"] = results[i].bridge.String()
	}
}

// BuildEquityChanges explains a bounded simple-entity equity roll-forward. It
// distinguishes recognized profit, posted equity movements and explicit closing
// transfers. Closing accounts never add the same profit to equity twice.
func BuildEquityChanges(in Input, request Request, policy EquityPolicy) (Result, error) {
	request.Report = ChangesInEquity
	r, err := NewResult(in, request)
	if err != nil {
		return r, err
	}
	if r.Scope.Period.From == "" {
		return r, &Error{Code: "equity_period_required"}
	}
	extensionClassificationCheck(&r, in)
	r.Reconciliation["policy_sha256"] = extensionEquityPolicyDigest(policy)
	accounts, postings, entries := extensionIndex(in)
	allocations := map[string][]EquityAllocation{}
	for _, a := range policy.Allocations {
		p, ok := postings[a.PostingID]
		if !ok || accounts[p.AccountID].Kind != "equity" || p.Treatment == "closing" || p.Treatment == "opening" || !extensionReference(a.TreatmentReference) {
			return r, &Error{Code: "invalid_equity_treatment"}
		}
		switch a.Category {
		case "contribution", "distribution", "restatement", "transfer", "other":
		default:
			return r, &Error{Code: "invalid_equity_category"}
		}
		n, e := extensionInteger(a.Amount)
		expected := extensionSigned(p)
		expected.Neg(expected)
		if e != nil || n.Sign() != 0 && n.Sign() != expected.Sign() {
			return r, &Error{Code: "invalid_equity_allocation"}
		}
		allocations[p.ID] = append(allocations[p.ID], a)
	}
	for id, list := range allocations {
		total := new(big.Int)
		for _, a := range list {
			n, _ := extensionInteger(a.Amount)
			total.Add(total, n)
		}
		expected := extensionSigned(postings[id])
		expected.Neg(expected)
		if new(big.Int).Abs(total).Cmp(new(big.Int).Abs(expected)) > 0 {
			return r, &Error{Code: "equity_allocation_exceeds_posting"}
		}
	}
	periods := extensionPeriods(r)
	results := make([]extensionPeriodResult, 0, len(periods))
	for _, period := range periods {
		out := extensionNewPeriod("opening", "closing", "profit", "contribution", "distribution", "restatement", "transfer", "other", "closing_transfer", "unresolved")
		transfers := map[string]*big.Int{}
		for _, p := range in.Postings {
			a := accounts[p.AccountID]
			if a.Kind != "equity" && !extensionEarnings(a.Kind) {
				continue
			}
			n := extensionSigned(p)
			n.Neg(n)
			if p.Date <= period.period.To {
				extensionAdd(&out, "closing", p, n, "equity_position", "")
			}
			if extensionIsOpening(period.period, p) {
				extensionAdd(&out, "opening", p, n, "equity_position", "")
				continue
			}
			if !extensionContains(period.period, p) {
				continue
			}
			if p.Treatment == "closing" {
				extensionAdd(&out, "closing_transfer", p, n, "closing_transfer", "typed:closing")
				for _, other := range entries[p.EntryID] {
					kind := accounts[other.AccountID].Kind
					if other.Treatment != "closing" || kind != "equity" && !extensionEarnings(kind) {
						out.closingValid = false
					}
				}
				continue
			}
			if p.Treatment == "opening" {
				extensionUnknown(&out, p, n)
				continue
			}
			if extensionEarnings(a.Kind) {
				extensionAdd(&out, "profit", p, n, "recognized_profit", "")
				continue
			}
			assigned := new(big.Int)
			for _, allocation := range allocations[p.ID] {
				value, _ := extensionInteger(allocation.Amount)
				extensionAdd(&out, allocation.Category, p, value, allocation.Category, allocation.TreatmentReference)
				assigned.Add(assigned, value)
				if allocation.Category == "transfer" {
					if transfers[p.EntryID] == nil {
						transfers[p.EntryID] = new(big.Int)
					}
					transfers[p.EntryID].Add(transfers[p.EntryID], value)
				}
			}
			extensionUnknown(&out, p, new(big.Int).Sub(n, assigned))
		}
		for _, n := range transfers {
			if n.Sign() != 0 {
				out.transferValid = false
			}
		}
		if out.values["closing_transfer"].amount.Sign() != 0 {
			out.closingValid = false
		}
		out.bridge.Sub(out.values["closing"].amount, out.values["opening"].amount)
		for _, key := range []string{"profit", "contribution", "distribution", "restatement", "transfer", "other", "closing_transfer", "unresolved"} {
			out.bridge.Sub(out.bridge, out.values[key].amount)
		}
		results = append(results, out)
	}
	rows := func(key, label string) Row { return extensionRow("equity:"+key, label, "total", periods, results, key) }
	movement := Row{ID: "equity:movements", Label: "Changes in equity", Role: "section", Children: []Row{}}
	extensionSumRows(&movement, rows("profit", "Recognized profit / loss"), rows("contribution", "Contributions"), rows("distribution", "Distributions"), rows("restatement", "Explicit adjustments and restatements"), rows("transfer", "Internal equity transfers"), rows("other", "Other reviewed equity changes"), rows("closing_transfer", "Closing transfers across equity and earnings"), rows("unresolved", "Unresolved equity movements"))
	r.Rows = []Row{rows("opening", "Opening total equity"), movement, rows("closing", "Closing total equity")}
	r.Rows[0].Role = "opening"
	r.Rows[2].Role = "closing"
	allBridge, allTreatment, allTransfer, allClosing := true, true, true, true
	for _, v := range results {
		allBridge = allBridge && v.bridge.Sign() == 0
		allTreatment = allTreatment && v.unresolvedCount == 0
		allTransfer = allTransfer && v.transferValid
		allClosing = allClosing && v.closingValid
	}
	extensionCheck(&r, "equity_bridge", extensionConstraintStatus(allBridge), "Opening equity plus reported movements reconciles to closing equity.")
	extensionCheck(&r, "equity_treatments", extensionBoolStatus(allTreatment), "Each equity posting component needs explicit reviewed treatment; unresolved gross activity is retained.")
	extensionCheck(&r, "equity_internal_transfers", extensionConstraintStatus(allTransfer), "Reviewed internal equity transfers must net to zero within each entry.")
	extensionCheck(&r, "equity_closing_transfers", extensionConstraintStatus(allClosing), "Explicit closing transfers must preserve total equity across equity and earnings.")
	extensionCheck(&r, "equity_supported_scope", extensionBoolStatus(policy.ScopeConfirmed && extensionReference(policy.Reference)), "Simple-entity ownership and equity treatment scope requires explicit review; unsupported OCI or ownership changes are not inferred.")
	if !allTransfer || !allClosing {
		r.Limitations = append(r.Limitations, "An explicitly labeled transfer does not meet the supported zero-change treatment.")
	}
	r.Limitations = append(r.Limitations, "This roll-forward is a ledger explanation, not independent recognition, ownership, OCI, disclosure or standards-compliance qualification.")
	extensionRecordMetrics(&r, periods, results)
	Finalize(&r)
	return r, nil
}

// BuildCashFlows uses explicit reviewed allocations. It does not classify from
// account names, counterparties, memos, signs, entry netting or timing metadata.
func BuildCashFlows(in Input, request Request, policy CashFlowPolicy) (Result, error) {
	request.Report = CashFlows
	r, err := NewResult(in, request)
	if err != nil {
		return r, err
	}
	if r.Scope.Period.From == "" {
		return r, &Error{Code: "cash_flow_period_required"}
	}
	extensionClassificationCheck(&r, in)
	r.Reconciliation["policy_sha256"] = extensionCashPolicyDigest(policy)
	accounts, postings, entries := extensionIndex(in)
	cash := map[string]bool{}
	for _, id := range policy.CashAccountIDs {
		a, ok := accounts[id]
		if !ok || a.Virtual || a.Kind != "asset" || cash[id] {
			return r, &Error{Code: "invalid_cash_scope"}
		}
		cash[id] = true
	}
	if !policy.CashScopeConfirmed || !extensionReference(policy.Reference) {
		r.Status = "unsupported"
		extensionCheck(&r, "cash_equivalent_scope", "unknown", "Cash and cash-equivalent scope and its policy must be explicitly confirmed before cash-flow totals are shown.")
		r.Limitations = append(r.Limitations, "The existing Cash movement report remains available independently; no cash-equivalent scope or formal classification is inferred.")
		Finalize(&r)
		return r, nil
	}
	allocations := map[string][]CashFlowAllocation{}
	for _, a := range policy.Allocations {
		p, ok := postings[a.PostingID]
		if !ok || !cash[p.AccountID] || p.Treatment == "opening" || !extensionReference(a.TreatmentReference) {
			return r, &Error{Code: "invalid_cash_flow_treatment"}
		}
		switch a.Category {
		case "operating", "investing", "financing", "exchange":
			if a.TransferGroup != "" {
				return r, &Error{Code: "invalid_cash_flow_transfer"}
			}
		case "internal_transfer":
			if strings.TrimSpace(a.TransferGroup) == "" {
				return r, &Error{Code: "invalid_cash_flow_transfer"}
			}
		default:
			return r, &Error{Code: "invalid_cash_flow_category"}
		}
		n, e := extensionInteger(a.Amount)
		expected := extensionSigned(p)
		if e != nil || n.Sign() != 0 && n.Sign() != expected.Sign() {
			return r, &Error{Code: "invalid_cash_flow_allocation"}
		}
		allocations[p.ID] = append(allocations[p.ID], a)
	}
	for id, list := range allocations {
		total := new(big.Int)
		for _, a := range list {
			n, _ := extensionInteger(a.Amount)
			total.Add(total, n)
		}
		if new(big.Int).Abs(total).Cmp(new(big.Int).Abs(extensionSigned(postings[id]))) > 0 {
			return r, &Error{Code: "cash_flow_allocation_exceeds_posting"}
		}
	}
	noncashSeen := map[string]bool{}
	for _, a := range policy.Noncash {
		entry, ok := entries[a.EntryID]
		p, postingOK := postings[a.PostingID]
		n, e := extensionInteger(a.Amount)
		if !ok || !postingOK || p.EntryID != a.EntryID || e != nil || n.Sign() < 0 || a.Amount != p.Amount || !extensionReference(a.TreatmentReference) || (a.Category != "investing" && a.Category != "financing") {
			return r, &Error{Code: "invalid_noncash_disclosure"}
		}
		key := a.PostingID + ":" + a.Category
		if noncashSeen[key] {
			return r, &Error{Code: "duplicate_noncash_disclosure"}
		}
		noncashSeen[key] = true
		for _, p := range entry {
			if cash[p.AccountID] {
				return r, &Error{Code: "noncash_disclosure_contains_cash"}
			}
		}
	}
	periods := extensionPeriods(r)
	results := make([]extensionPeriodResult, 0, len(periods))
	for _, period := range periods {
		out := extensionNewPeriod("opening", "closing", "operating", "investing", "financing", "exchange", "internal_transfer", "unresolved", "noncash_investing", "noncash_financing")
		transfers := map[string]*big.Int{}
		for _, p := range in.Postings {
			if !cash[p.AccountID] {
				continue
			}
			n := extensionSigned(p)
			if p.Date <= period.period.To {
				extensionAdd(&out, "closing", p, n, "cash_position", "")
			}
			if extensionIsOpening(period.period, p) {
				extensionAdd(&out, "opening", p, n, "cash_position", "")
				continue
			}
			if !extensionContains(period.period, p) {
				continue
			}
			if p.Treatment == "opening" {
				extensionUnknown(&out, p, n)
				continue
			}
			assigned := new(big.Int)
			for _, a := range allocations[p.ID] {
				value, _ := extensionInteger(a.Amount)
				extensionAdd(&out, a.Category, p, value, a.Category, a.TreatmentReference)
				assigned.Add(assigned, value)
				if a.Category == "internal_transfer" {
					if transfers[a.TransferGroup] == nil {
						transfers[a.TransferGroup] = new(big.Int)
					}
					transfers[a.TransferGroup].Add(transfers[a.TransferGroup], value)
				}
			}
			extensionUnknown(&out, p, new(big.Int).Sub(n, assigned))
		}
		for _, n := range transfers {
			if n.Sign() != 0 {
				out.transferValid = false
			}
		}
		for _, a := range policy.Noncash {
			p := postings[a.PostingID]
			if extensionContains(period.period, p) {
				n, _ := extensionInteger(a.Amount)
				extensionAdd(&out, "noncash_"+a.Category, p, n, "noncash_"+a.Category, a.TreatmentReference)
			}
		}
		out.bridge.Sub(out.values["closing"].amount, out.values["opening"].amount)
		for _, key := range []string{"operating", "investing", "financing", "exchange", "internal_transfer", "unresolved"} {
			out.bridge.Sub(out.bridge, out.values[key].amount)
		}
		results = append(results, out)
	}
	rows := func(key, label string) Row {
		return extensionRow("cash_flow:"+key, label, "total", periods, results, key)
	}
	flows := Row{ID: "cash_flow:net", Label: "Net cash flows", Role: "section", Children: []Row{}}
	extensionSumRows(&flows, rows("operating", "Operating activities"), rows("investing", "Investing activities"), rows("financing", "Financing activities"))
	r.Rows = []Row{rows("opening", "Opening cash and cash equivalents"), flows, rows("exchange", "Exchange effects on cash"), rows("internal_transfer", "Reviewed internal transfers (net zero)"), rows("unresolved", "Unresolved cash movements"), rows("closing", "Closing cash and cash equivalents")}
	r.Rows[0].Role = "opening"
	r.Rows[5].Role = "closing"
	noncash := Row{ID: "cash_flow:noncash", Label: "Separately reviewed noncash activity", Role: "section", Children: []Row{}}
	// Investing and financing disclosures may describe opposite sides of the
	// same noncash transaction. They are not added into a false grand total.
	noncash.Values = map[string]string{}
	noncash.Children = []Row{rows("noncash_investing", "Noncash investing activity"), rows("noncash_financing", "Noncash financing activity")}
	r.Rows = append(r.Rows, noncash)
	allBridge, allClassified, allTransfer := true, true, true
	for _, v := range results {
		allBridge = allBridge && v.bridge.Sign() == 0
		allClassified = allClassified && v.unresolvedCount == 0
		allTransfer = allTransfer && v.transferValid
	}
	extensionCheck(&r, "cash_flow_bridge", extensionConstraintStatus(allBridge), "Opening cash plus reported movements and exchange effects reconciles to closing cash.")
	extensionCheck(&r, "cash_flow_classification", extensionBoolStatus(allClassified), "Every cash posting component needs reviewed classification, including gross movements in zero-net entries.")
	extensionCheck(&r, "cash_flow_internal_transfers", extensionConstraintStatus(allTransfer), "Each reviewed internal-transfer group must net to zero within every displayed period.")
	extensionCheck(&r, "cash_flow_noncash_review", extensionBoolStatus(policy.NoncashReviewConfirmed), "Noncash investing and financing activity needs separate explicit review; it is excluded from cash totals.")
	if !allTransfer {
		r.Limitations = append(r.Limitations, "A stated internal-transfer group does not net to zero in the selected period.")
	}
	r.Limitations = append(r.Limitations, "Direct cash-flow classification uses explicit reviewed policy and recorded book-currency carrying amounts. It does not qualify recognition, currency translation, disclosures or standards compliance.")
	extensionRecordMetrics(&r, periods, results)
	Finalize(&r)
	return r, nil
}

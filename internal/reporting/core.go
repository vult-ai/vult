package reporting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

var unsignedInteger = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var signedInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func problem(code string) error { return &Error{Code: code} }
func zero() *big.Int            { return new(big.Int) }
func integer(s string) *big.Int { n, _ := new(big.Int).SetString(s, 10); return n }
func signed(p Posting) *big.Int {
	n := integer(p.Amount)
	if p.Side == "credit" {
		n.Neg(n)
	}
	return n
}
func dateValid(s string) bool {
	t, e := time.Parse(time.DateOnly, s)
	return e == nil && t.Year() >= 1 && t.Format(time.DateOnly) == s
}
func class(kind string) string {
	switch kind {
	case "asset", "liability", "equity":
		return kind
	case "income", "revenue", "other_income":
		return "income"
	case "expense", "other_expense", "cost_of_sales", "cost_of_goods_sold":
		return "expense"
	}
	return "unknown"
}
func creditClass(kind string) bool {
	k := class(kind)
	return k == "liability" || k == "equity" || k == "income"
}
func treatment(p Posting) string {
	if p.Treatment == "" {
		return "unknown"
	}
	return p.Treatment
}
func earned(p Posting) bool { return p.Treatment != "closing" && p.Treatment != "opening" }
func inPeriod(p Posting, period Period) bool {
	return p.Date <= period.To && (period.From == "" || p.Date >= period.From)
}

// ValidateInput rejects structural uncertainty; semantic unknowns stay explicit
// in Result checks. Error codes never contain source labels, IDs or records.
func ValidateInput(in Input) error {
	if in.Book.ID == "" || !currencyCode.MatchString(in.Book.Currency) || in.Book.Scale < 0 || in.Book.Scale > 9 {
		return problem("invalid_book")
	}
	if in.Book.StartDate != "" && !dateValid(in.Book.StartDate) {
		return problem("invalid_book_date")
	}
	m, d := in.Book.FiscalStartMonth, in.Book.FiscalStartDay
	f := time.Date(2001, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if (m != 0 || d != 0) && (int(f.Month()) != m || f.Day() != d) {
		return problem("invalid_fiscal_calendar")
	}
	accounts, codes := map[string]Account{}, map[string]bool{}
	for _, a := range in.Accounts {
		if a.ID == "" || (a.Code != "" && codes[a.Code]) {
			return problem("invalid_account_identity")
		}
		if _, ok := accounts[a.ID]; ok {
			return problem("duplicate_account_identity")
		}
		accounts[a.ID] = a
		codes[a.Code] = true
	}
	for _, a := range in.Accounts {
		seen := map[string]bool{a.ID: true}
		cursor := a
		for cursor.ParentID != "" {
			if len(seen) >= 128 {
				return problem("account_hierarchy_too_deep")
			}
			parent, ok := accounts[cursor.ParentID]
			if !ok {
				return problem("missing_parent_identity")
			}
			if seen[parent.ID] {
				return problem("cyclic_account_hierarchy")
			}
			if class(cursor.Kind) != "unknown" && class(parent.Kind) != "unknown" && class(cursor.Kind) != class(parent.Kind) {
				return problem("cross_class_hierarchy")
			}
			seen[parent.ID] = true
			cursor = parent
		}
	}
	seen := map[string]bool{}
	entryBalances, entryDates, entryTreatments := map[string]*big.Int{}, map[string]string{}, map[string]string{}
	entryReferences := map[string]string{}
	for _, p := range in.Postings {
		if p.ID == "" || p.EntryID == "" || seen[p.ID] {
			return problem("invalid_posting_identity")
		}
		seen[p.ID] = true
		if p.BookID != in.Book.ID {
			return problem("book_scope_mismatch")
		}
		if p.Currency != in.Book.Currency || p.Scale != in.Book.Scale {
			return problem("currency_scope_mismatch")
		}
		a, ok := accounts[p.AccountID]
		if !ok || a.Virtual {
			return problem("invalid_posting_account")
		}
		if !dateValid(p.Date) {
			return problem("invalid_posting_date")
		}
		if p.Side != "debit" && p.Side != "credit" {
			return problem("invalid_posting_side")
		}
		if !unsignedInteger.MatchString(p.Amount) {
			return problem("invalid_posting_amount")
		}
		if p.NativeAmount != nil && !unsignedInteger.MatchString(*p.NativeAmount) {
			return problem("invalid_native_measurement")
		}
		switch treatment(p) {
		case "unknown", "ordinary", "recognition", "adjustment", "opening", "closing", "contribution", "distribution", "restatement", "equity_transfer", "other":
		default:
			return problem("unsupported_posting_purpose")
		}
		if entryBalances[p.EntryID] == nil {
			entryBalances[p.EntryID] = zero()
			entryDates[p.EntryID] = p.Date
			entryTreatments[p.EntryID] = treatment(p)
			entryReferences[p.EntryID] = p.EntryReference
		}
		if entryDates[p.EntryID] != p.Date || entryTreatments[p.EntryID] != treatment(p) || entryReferences[p.EntryID] != p.EntryReference {
			return problem("inconsistent_entry_metadata")
		}
		entryBalances[p.EntryID].Add(entryBalances[p.EntryID], signed(p))
	}
	for _, n := range entryBalances {
		if n.Sign() != 0 {
			return problem("unbalanced_entry")
		}
	}
	if in.Provenance.GeneratedAt != "" {
		if _, e := time.Parse(time.RFC3339Nano, in.Provenance.GeneratedAt); e != nil {
			return problem("invalid_generation_time")
		}
	}
	return nil
}

func fiscalStart(book Book, end string) string {
	t, _ := time.Parse(time.DateOnly, end)
	m, d := book.FiscalStartMonth, book.FiscalStartDay
	if m == 0 || d == 0 {
		return ""
	}
	s := time.Date(t.Year(), time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if s.After(t) {
		s = s.AddDate(-1, 0, 0)
	}
	return s.Format(time.DateOnly)
}
func shiftMonth(t time.Time, months int) time.Time {
	base := time.Date(t.Year(), t.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := base.AddDate(0, 1, -1).Day()
	day := t.Day()
	if day > last {
		day = last
	}
	return time.Date(base.Year(), base.Month(), day, 0, 0, 0, 0, time.UTC)
}
func comparisonPeriod(req Request, period Period) (*Period, error) {
	mode := req.Comparison
	if mode == "" || mode == "none" {
		if req.CompareFrom != "" || req.CompareTo != "" {
			return nil, problem("unexpected_comparison_dates")
		}
		return nil, nil
	}
	position := period.From == ""
	end, _ := time.Parse(time.DateOnly, period.To)
	if mode == "explicit" {
		if !dateValid(req.CompareTo) || (position && req.CompareFrom != "") || (!position && (!dateValid(req.CompareFrom) || req.CompareFrom > req.CompareTo)) {
			return nil, problem("invalid_comparison_period")
		}
		return &Period{From: req.CompareFrom, To: req.CompareTo}, nil
	}
	if req.CompareFrom != "" || req.CompareTo != "" {
		return nil, problem("unexpected_comparison_dates")
	}
	if mode != "previous_period" && mode != "previous_year" {
		return nil, problem("unsupported_comparison")
	}
	if position {
		months := -1
		if mode == "previous_year" {
			months = -12
		}
		previous := shiftMonth(end, months)
		if mode == "previous_period" && end.AddDate(0, 0, 1).Day() == 1 {
			previous = time.Date(end.Year(), end.Month(), 0, 0, 0, 0, 0, time.UTC)
		}
		return &Period{To: previous.Format(time.DateOnly)}, nil
	}
	start, _ := time.Parse(time.DateOnly, period.From)
	if mode == "previous_year" {
		previousEnd := shiftMonth(end, -12)
		if start.Day() == 1 && end.AddDate(0, 0, 1).Day() == 1 {
			previousEnd = shiftMonth(end.AddDate(0, 0, 1), -12).AddDate(0, 0, -1)
		}
		return &Period{From: shiftMonth(start, -12).Format(time.DateOnly), To: previousEnd.Format(time.DateOnly)}, nil
	}
	priorEnd := start.AddDate(0, 0, -1)
	if start.Day() == 1 && end.AddDate(0, 0, 1).Day() == 1 {
		months := (end.Year()-start.Year())*12 + int(end.Month()-start.Month()) + 1
		return &Period{From: start.AddDate(0, -months, 0).Format(time.DateOnly), To: priorEnd.Format(time.DateOnly)}, nil
	}
	// Date-only Unix seconds remain exact throughout years 1..9999; a
	// time.Duration subtraction would silently saturate beyond about 292 years.
	days := int((end.Unix()-start.Unix())/86400) + 1
	return &Period{From: priorEnd.AddDate(0, 0, 1-days).Format(time.DateOnly), To: priorEnd.Format(time.DateOnly)}, nil
}

func normalizeRequest(in Input, req Request) (Request, Period, *Period, error) {
	if req.BookID == "" || req.BookID != in.Book.ID {
		return req, Period{}, nil, problem("book_scope_mismatch")
	}
	if req.Currency == "" {
		req.Currency = in.Book.Currency
	}
	if req.Currency != in.Book.Currency {
		return req, Period{}, nil, problem("unsupported_reporting_currency")
	}
	position := req.Report == BalanceSheet || req.Report == TrialBalance
	switch req.Report {
	case BalanceSheet, ProfitLoss, TrialBalance, GeneralLedger, Journal, ChangesInEquity, CashFlows, CashMovement:
	default:
		return req, Period{}, nil, problem("unsupported_report")
	}
	var period Period
	if position {
		if req.AsOf == "" {
			req.AsOf = req.To
		}
		if !dateValid(req.AsOf) || req.From != "" || (req.To != "" && req.To != req.AsOf) {
			return req, period, nil, problem("invalid_position_date")
		}
		req.To = ""
		period.To = req.AsOf
	} else {
		if req.AsOf != "" || !dateValid(req.From) || !dateValid(req.To) || req.From > req.To {
			return req, period, nil, problem("invalid_report_period")
		}
		period = Period{From: req.From, To: req.To}
	}
	if req.Comparison == "" {
		req.Comparison = "none"
	}
	prior, err := comparisonPeriod(req, period)
	if err != nil {
		return req, period, nil, err
	}
	if prior != nil && (req.Report == GeneralLedger || req.Report == Journal || req.Report == CashMovement) {
		return req, period, nil, problem("comparison_not_supported_for_detailed_report")
	}
	if prior != nil && (!dateValid(prior.To) || (prior.From != "" && !dateValid(prior.From))) {
		return req, period, nil, problem("invalid_comparison_period")
	}
	if len(req.AccountIDs) > 0 && req.Report != GeneralLedger {
		return req, period, nil, problem("unsupported_account_filter")
	}
	req.AccountIDs = append([]string(nil), req.AccountIDs...)
	sort.Strings(req.AccountIDs)
	ids := map[string]bool{}
	for _, a := range in.Accounts {
		ids[a.ID] = true
	}
	for i, id := range req.AccountIDs {
		if !ids[id] || (i > 0 && req.AccountIDs[i-1] == id) {
			return req, period, nil, problem("invalid_account_filter")
		}
	}
	return req, period, prior, nil
}

func digest(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func populationDigest(in Input) string {
	in.Accounts = append([]Account(nil), in.Accounts...)
	in.Postings = append([]Posting(nil), in.Postings...)
	sort.Slice(in.Accounts, func(i, j int) bool { return in.Accounts[i].ID < in.Accounts[j].ID })
	sort.Slice(in.Postings, func(i, j int) bool { return in.Postings[i].ID < in.Postings[j].ID })
	in.Provenance.Limitations = uniqueSorted(in.Provenance.Limitations)
	// Rendering time is retained in Result, but does not change the identity of
	// an unchanged financial population during a subsequent contribution read.
	in.Provenance.GeneratedAt = ""
	return digest(in)
}
func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.Strings(out)
	return out
}
func title(kind Kind) string {
	switch kind {
	case BalanceSheet:
		return "Balance Sheet"
	case ProfitLoss:
		return "Profit & Loss"
	case TrialBalance:
		return "Trial Balance"
	case GeneralLedger:
		return "General Ledger"
	case Journal:
		return "Journal"
	case ChangesInEquity:
		return "Changes in Equity"
	case CashFlows:
		return "Cash Flows"
	case CashMovement:
		return "Cash movement"
	}
	return "Report"
}

// NewResult supplies validated identity/scope/checks. Extension builders replace
// rows/columns and append treatment checks, then call Finalize.
func NewResult(in Input, req Request) (Result, error) {
	if e := ValidateInput(in); e != nil {
		return Result{}, e
	}
	req, period, prior, e := normalizeRequest(in, req)
	if e != nil {
		return Result{}, e
	}
	book := in.Book
	if book.RecognitionBasis == "" {
		book.RecognitionBasis = "unknown"
	}
	if book.ReportingFramework == "" {
		book.ReportingFramework = "unknown"
	}
	p := in.Provenance
	if p.Coverage == "" {
		p.Coverage = "unknown"
	}
	if p.OpeningBalances == "" {
		p.OpeningBalances = "unknown"
	}
	if p.HierarchyBasis == "" {
		p.HierarchyBasis = "unknown"
	}
	r := Result{SchemaVersion: 1, Report: req.Report, Title: title(req.Report), Book: book, Request: req,
		Scope: Scope{Mode: "live", Period: period, Comparison: prior, FiscalStart: fiscalStart(book, period.To), LedgerRevision: p.LedgerRevision, KnowledgeCutoff: p.KnowledgeCutoff, MappingVersion: p.MappingVersion, HierarchyBasis: p.HierarchyBasis, GeneratedAt: p.GeneratedAt, PopulationDigest: populationDigest(in), Coverage: p.Coverage, OpeningBalances: p.OpeningBalances},
		Rows:  []Row{}, Columns: []Column{{ID: "current", Label: "Current", Kind: "amount", Period: period}}, Checks: []Check{{ID: "ledger_population", Status: "pass", Message: "Every supplied entry balances with unique posting identities."}}, Findings: []Finding{}, Limitations: append([]string{}, p.Limitations...), Reconciliation: map[string]string{}, Status: "ready"}
	if prior != nil {
		r.Columns = append(r.Columns, Column{ID: "prior", Label: "Comparison", Kind: "amount", Period: *prior}, Column{ID: "variance", Label: "Change", Kind: "amount", Period: period})
	}
	coverageStatus := "unknown"
	if p.Coverage == "complete" {
		coverageStatus = "pass"
	}
	if p.Coverage == "partial" {
		coverageStatus = "fail"
	}
	r.Checks = append(r.Checks, Check{ID: "source_coverage", Status: coverageStatus, Message: "Coverage describes the supplied population; balance alone does not establish completeness."})
	openingStatus := "unknown"
	if p.OpeningBalances == "confirmed" {
		openingStatus = "pass"
	}
	if p.OpeningBalances == "partial" {
		openingStatus = "fail"
	}
	r.Checks = append(r.Checks, Check{ID: "opening_balances", Status: openingStatus, Message: "Opening-balance provenance is separate from mathematical ledger balance."})
	if book.RecognitionBasis == "unknown" {
		r.Checks = append(r.Checks, Check{ID: "recognition_basis", Status: "unknown", Message: "Accounting recognition basis has not been established."})
	}
	if r.Scope.FiscalStart == "" {
		r.Checks = append(r.Checks, Check{ID: "fiscal_calendar", Status: "unknown", Message: "The fiscal calendar is unset; accumulated results are not assigned to an invented fiscal year."})
	}
	if p.LedgerRevision == "" || p.MappingVersion == "" {
		r.Checks = append(r.Checks, Check{ID: "revision_provenance", Status: "unknown", Message: "Ledger or mapping revision provenance is unavailable."})
	}
	unknownTreatment := false
	for _, line := range in.Postings {
		if inPeriod(line, Period{To: period.To}) && (line.Treatment == "" || line.Treatment == "unknown") {
			unknownTreatment = true
			break
		}
	}
	if unknownTreatment {
		r.Limitations = append(r.Limitations, "Reporting treatment is not fully established; economic purpose and labels do not establish an opening or closing treatment.")
		if req.Report == BalanceSheet || req.Report == ProfitLoss || req.Report == ChangesInEquity || req.Report == CashFlows {
			r.Checks = append(r.Checks, Check{ID: "entry_purpose", Status: "unknown", Message: "Opening and closing purpose is not fully established; earned-result interpretation requires review."})
		}
	}
	addOpeningReview(in, &r)
	r.Limitations = append(r.Limitations, "Live recorded-ledger report; no audited, statutory or standards-compliance assertion is made.")
	r.Limitations = append(r.Limitations, "Amounts are recorded Book-currency values; no new currency translation or period-end remeasurement is performed.")
	return r, nil
}

func addOpeningReview(in Input, r *Result) {
	accounts := make(map[string]Account, len(in.Accounts))
	for _, a := range in.Accounts {
		accounts[a.ID] = a
	}
	equity, earnings := map[string]bool{}, map[string]*big.Int{}
	for _, p := range in.Postings {
		if !inPeriod(p, r.Scope.Period) && (r.Scope.Comparison == nil || !inPeriod(p, *r.Scope.Comparison)) {
			continue
		}
		if treatment(p) != "unknown" {
			continue
		}
		switch class(accounts[p.AccountID].Kind) {
		case "equity":
			equity[p.EntryID] = true
		case "income", "expense":
			if earnings[p.EntryID] == nil {
				earnings[p.EntryID] = zero()
			}
			earnings[p.EntryID].Add(earnings[p.EntryID], integer(p.Amount))
		}
	}
	ids, gross := []string{}, zero()
	for id, amount := range earnings {
		if equity[id] {
			ids = append(ids, id)
			gross.Add(gross, amount)
		}
	}
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids)
	message := "Income or expenses posted against equity. Included as recorded; opening and period treatment has not been reviewed."
	r.Findings = append(r.Findings, Finding{Code: "opening_treatment_unconfirmed", EntryIDs: ids, AmountMinor: gross.String(), Message: message})
	r.Checks = append(r.Checks, Check{ID: "opening_treatment_review", Status: "unknown", Message: message})
	r.Limitations = append(r.Limitations, message)
}

// Finalize binds every computed row, policy/scope value and contribution
// predicate. It never upgrades unknown/failed source or treatment checks.
func Finalize(r *Result) {
	r.Limitations = uniqueSorted(r.Limitations)
	if r.Status != "unsupported" {
		partial := r.Status == "partial"
		r.Status = "ready"
		if partial {
			r.Status = "partial"
		}
		for _, c := range r.Checks {
			if c.Status == "fail" || c.Status == "unknown" {
				r.Status = "partial"
				break
			}
		}
	}
	r.Digest = ""
	r.Digest = digest(*r)
}

func valuesFor(in Input, period Period, report Kind) map[string]*big.Int {
	out := map[string]*big.Int{}
	for _, a := range in.Accounts {
		out[a.ID] = zero()
	}
	for _, p := range in.Postings {
		if !inPeriod(p, period) || (report == ProfitLoss && !earned(p)) {
			continue
		}
		out[p.AccountID].Add(out[p.AccountID], signed(p))
	}
	return out
}
func natural(a Account, amount *big.Int) *big.Int {
	n := new(big.Int).Set(amount)
	if creditClass(a.Kind) {
		n.Neg(n)
	}
	return n
}
func valueColumns(current, prior *big.Int, compare bool) map[string]string {
	out := map[string]string{"current": current.String()}
	if compare {
		out["prior"] = prior.String()
		out["variance"] = new(big.Int).Sub(current, prior).String()
	}
	return out
}
func sumClass(in Input, values map[string]*big.Int, category string, useNatural bool) *big.Int {
	out := zero()
	for _, a := range in.Accounts {
		if class(a.Kind) == category {
			n := values[a.ID]
			if useNatural {
				n = natural(a, n)
			}
			out.Add(out, n)
		}
	}
	return out
}
func filterIDs(in Input, categories ...string) []string {
	out := []string{}
	for _, a := range in.Accounts {
		for _, c := range categories {
			if !a.Virtual && class(a.Kind) == c {
				out = append(out, a.ID)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
func simpleRow(id, label, role string, values map[string]string, drill *Drilldown) Row {
	return Row{ID: id, Label: label, Role: role, Values: values, Children: []Row{}, Drilldown: drill}
}

// BuildCore is the single core accounting calculation entrypoint. It does not
// invoke a database, alter postings, close periods or trust imported subtotals.
func BuildCore(in Input, req Request) (Result, error) {
	r, e := NewResult(in, req)
	if e != nil {
		return r, e
	}
	switch req.Report {
	case BalanceSheet:
		e = buildPosition(in, &r, func(period Period) map[string]*big.Int {
			return valuesFor(in, period, BalanceSheet)
		}, nil)
	case ProfitLoss:
		e = buildPerformance(in, &r, func(period Period) map[string]*big.Int {
			return valuesFor(in, period, ProfitLoss)
		})
	case TrialBalance:
		e = buildTrialBalance(in, &r)
	case GeneralLedger:
		e = buildGeneralLedger(in, &r)
	case Journal:
		e = buildJournal(in, &r)
	default:
		return r, problem("unsupported_core_report")
	}
	if e != nil {
		return Result{}, e
	}
	Finalize(&r)
	return r, nil
}

func addUnclassified(in Input, r *Result, current, prior map[string]*big.Int) {
	needed := false
	for _, a := range in.Accounts {
		if class(a.Kind) == "unknown" && (current[a.ID].Sign() != 0 || (prior != nil && prior[a.ID].Sign() != 0)) {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	r.Rows = append(r.Rows, section(in, "unclassified", "Unclassified accounts", []string{"unknown"}, current, prior, r.Scope.Period, r.Scope.Comparison, false))
	r.Checks = append(r.Checks, Check{ID: "account_classification", Status: "fail", Message: "Nonzero accounts need a supported accounting classification; the financial presentation is partial."})
}

func buildPosition(in Input, r *Result, values func(Period) map[string]*big.Int, fiscal *fiscalPosition) error {
	current := values(r.Scope.Period)
	var prior map[string]*big.Int
	if r.Scope.Comparison != nil {
		prior = values(*r.Scope.Comparison)
	}
	unclosed := unclosedResult(in, current)
	priorUnclosed := zero()
	if prior != nil {
		priorUnclosed = unclosedResult(in, prior)
	}
	for _, group := range []struct{ id, label string }{{"asset", "Assets"}, {"liability", "Liabilities"}, {"equity", "Equity"}} {
		id := group.id
		if id == "asset" {
			id = "assets"
		}
		if id == "liability" {
			id = "liabilities"
		}
		row := section(in, id, group.label, []string{group.id}, current, prior, r.Scope.Period, r.Scope.Comparison, true)
		if group.id == "equity" {
			residual := simpleRow("equity:unclosed_result", "Unclosed accumulated result", "total", valueColumns(unclosed, priorUnclosed, prior != nil), &Drilldown{AccountIDs: filterIDs(in, "income", "expense"), To: r.Scope.Period.To})
			if r.Scope.FiscalStart != "" {
				before := Period{To: previousDate(r.Scope.FiscalStart)}
				during := Period{From: r.Scope.FiscalStart, To: r.Scope.Period.To}
				var previousBefore, previousDuring *Period
				if r.Scope.Comparison != nil {
					start := fiscalStart(in.Book, r.Scope.Comparison.To)
					previousBefore = &Period{To: previousDate(start)}
					previousDuring = &Period{From: start, To: r.Scope.Comparison.To}
				}
				for _, part := range []struct {
					id, label  string
					period     Period
					comparison *Period
				}{{"prior_unclosed_result", "Prior fiscal unclosed result", before, previousBefore}, {"current_unclosed_result", "Current fiscal unclosed result", during, previousDuring}} {
					currentPart := unclosedResult(in, values(part.period))
					priorPart := zero()
					if part.comparison != nil {
						priorPart = unclosedResult(in, values(*part.comparison))
					}
					residual.Children = append(residual.Children, simpleRow("equity:"+part.id, part.label, "total", valueColumns(currentPart, priorPart, prior != nil), &Drilldown{AccountIDs: filterIDs(in, "income", "expense"), From: part.period.From, To: part.period.To, Comparison: part.comparison}))
				}
			}
			row.Children = append(row.Children, residual)
			cur := integer(row.Values["current"])
			cur.Add(cur, unclosed)
			prev := zero()
			if prior != nil {
				prev.Add(integer(row.Values["prior"]), priorUnclosed)
			}
			row.Values = valueColumns(cur, prev, prior != nil)
			row.Drilldown.AccountIDs = filterIDs(in, "equity", "income", "expense")
		}
		r.Rows = append(r.Rows, row)
	}
	assets := sumClass(in, current, "asset", false)
	liabilities := sumClass(in, current, "liability", true)
	posted := sumClass(in, current, "equity", true)
	equity := new(big.Int).Add(posted, unclosed)
	r.Reconciliation = map[string]string{"assets": assets.String(), "liabilities": liabilities.String(), "posted_equity": posted.String(), "unclosed_result": unclosed.String(), "equity_total": equity.String()}
	status := "pass"
	if assets.Cmp(new(big.Int).Add(liabilities, equity)) != 0 {
		status = "fail"
	}
	r.Checks = append(r.Checks, Check{ID: "balance_sheet_equation", Status: status, Message: "Recorded assets equal recorded liabilities plus posted equity and unclosed results."})
	if r.Scope.FiscalStart != "" {
		if fiscal == nil {
			fiscal = readFiscalPosition(in, r.Scope)
		}
		for key, value := range fiscal.totals {
			r.Reconciliation[key] = value
		}
		r.Checks = append(r.Checks, fiscal.check)
	}

	addUnclassified(in, r, current, prior)
	return nil
}

func unclosedResult(in Input, values map[string]*big.Int) *big.Int {
	total := sumClass(in, values, "income", false)
	total.Add(total, sumClass(in, values, "expense", false))
	return total.Neg(total)
}

type fiscalPosition struct {
	totals map[string]string
	check  Check
}

func readFiscalPosition(in Input, scope Scope) *fiscalPosition {
	priorResidual, currentResidual, currentProfit, currentClosing, currentOpening := zero(), zero(), zero(), zero(), zero()
	accountLookup := map[string]Account{}
	for _, a := range in.Accounts {
		accountLookup[a.ID] = a
	}
	for _, p := range in.Postings {
		if p.Date > scope.Period.To {
			continue
		}
		a := accountLookup[p.AccountID]
		if k := class(a.Kind); k != "income" && k != "expense" {
			continue
		}
		n := new(big.Int).Neg(signed(p))
		if p.Date < scope.FiscalStart {
			priorResidual.Add(priorResidual, n)
		} else {
			currentResidual.Add(currentResidual, n)
			if p.Treatment == "closing" {
				currentClosing.Add(currentClosing, n)
			} else if p.Treatment == "opening" {
				currentOpening.Add(currentOpening, n)
			} else {
				currentProfit.Add(currentProfit, n)
			}
		}
	}
	bridge := new(big.Int).Add(currentProfit, currentClosing)
	bridge.Add(bridge, currentOpening)
	status := "pass"
	if bridge.Cmp(currentResidual) != 0 {
		status = "fail"
	}
	return &fiscalPosition{
		totals: map[string]string{"prior_unclosed_result": priorResidual.String(), "current_unclosed_result": currentResidual.String(),
			"current_fiscal_profit": currentProfit.String(), "current_fiscal_closing": currentClosing.String(), "current_fiscal_opening": currentOpening.String()},
		check: Check{ID: "fiscal_result_bridge", Status: status, Message: "Current fiscal unclosed balances reconcile to recorded activity and established opening and closing treatments."},
	}
}

func buildPerformance(in Input, r *Result, values func(Period) map[string]*big.Int) error {
	current := values(r.Scope.Period)
	var prior map[string]*big.Int
	if r.Scope.Comparison != nil {
		prior = values(*r.Scope.Comparison)
	}
	income := section(in, "income", "Income", []string{"income"}, current, prior, r.Scope.Period, r.Scope.Comparison, true)
	// Cost of sales comes from the accounts' recorded role, never from codes
	// or names. Without such accounts the statement keeps its two sections.
	costIn, otherIn := in, in
	costIn.Accounts, otherIn.Accounts = nil, nil
	for _, a := range in.Accounts {
		if a.Kind == "cost_of_sales" {
			costIn.Accounts = append(costIn.Accounts, a)
		} else {
			otherIn.Accounts = append(otherIn.Accounts, a)
		}
	}
	expenses := section(otherIn, "expenses", "Expenses", []string{"expense"}, current, prior, r.Scope.Period, r.Scope.Comparison, true)
	profit := new(big.Int).Sub(integer(income.Values["current"]), integer(expenses.Values["current"]))
	previous := zero()
	if prior != nil {
		previous.Sub(integer(income.Values["prior"]), integer(expenses.Values["prior"]))
	}
	if len(costIn.Accounts) == 0 {
		r.Rows = append(r.Rows, income, expenses)
	} else {
		costs := section(costIn, "cost_of_sales", "Cost of sales", []string{"expense"}, current, prior, r.Scope.Period, r.Scope.Comparison, true)
		grossNow := new(big.Int).Sub(integer(income.Values["current"]), integer(costs.Values["current"]))
		grossBefore := zero()
		if prior != nil {
			grossBefore.Sub(integer(income.Values["prior"]), integer(costs.Values["prior"]))
		}
		gross := simpleRow("total:gross_profit", "Gross profit", "total", valueColumns(grossNow, grossBefore, prior != nil), &Drilldown{AccountIDs: sortedIDs(append(filterIDs(in, "income"), filterIDs(costIn, "expense")...)), From: r.Scope.Period.From, To: r.Scope.Period.To, ExcludeClosing: true, ExcludeOpening: true})
		r.Rows = append(r.Rows, income, costs, gross, expenses)
		profit.Sub(grossNow, integer(expenses.Values["current"]))
		if prior != nil {
			previous.Sub(grossBefore, integer(expenses.Values["prior"]))
		}
		defer func() {
			r.Reconciliation["cost_of_sales"], r.Reconciliation["gross_profit"] = costs.Values["current"], grossNow.String()
			r.Reconciliation["expenses"] = new(big.Int).Add(integer(costs.Values["current"]), integer(expenses.Values["current"])).String()
		}()
	}
	r.Rows = append(r.Rows, simpleRow("total:result", "Recorded result", "total", valueColumns(profit, previous, prior != nil), &Drilldown{AccountIDs: filterIDs(in, "income", "expense"), From: r.Scope.Period.From, To: r.Scope.Period.To, ExcludeClosing: true, ExcludeOpening: true}))
	r.Reconciliation = map[string]string{"income": income.Values["current"], "expenses": expenses.Values["current"], "result": profit.String()}
	r.Checks = append(r.Checks, Check{ID: "profit_loss_reconstruction", Status: "pass", Message: "The result reconstructs the selected income and expense postings, excluding explicitly typed openings and closes."})
	addUnclassified(in, r, current, prior)
	return nil
}

func buildTrialBalance(in Input, r *Result) error {
	current := valuesFor(in, r.Scope.Period, TrialBalance)
	var prior map[string]*big.Int
	if r.Scope.Comparison != nil {
		prior = valuesFor(in, *r.Scope.Comparison, TrialBalance)
	}
	r.Columns = []Column{{ID: "debit", Label: "Debit", Kind: "amount", Period: r.Scope.Period}, {ID: "credit", Label: "Credit", Kind: "amount", Period: r.Scope.Period}}
	if prior != nil {
		r.Columns = append(r.Columns, Column{ID: "prior_debit", Label: "Prior debit", Kind: "amount", Period: *r.Scope.Comparison}, Column{ID: "prior_credit", Label: "Prior credit", Kind: "amount", Period: *r.Scope.Comparison}, Column{ID: "variance", Label: "Net change", Kind: "amount", Period: r.Scope.Period})
	}
	row := trialTree(in, current, prior, r.Scope.Period)
	r.Rows = row.Children
	row.ID = "total:trial_balance"
	row.Label = "Total"
	row.Role = "total"
	row.Children = []Row{}
	r.Rows = append(r.Rows, row)
	r.Reconciliation = map[string]string{"debit": row.Values["debit"], "credit": row.Values["credit"]}
	status := "pass"
	if row.Values["debit"] != row.Values["credit"] {
		status = "fail"
	}
	r.Checks = append(r.Checks, Check{ID: "trial_balance", Status: status, Message: "Closing debit and credit account balances reconcile to the supplied journal population."})
	return nil
}

func sortedPostings(in Input) []Posting {
	rows := append([]Posting(nil), in.Postings...)
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.EntryID != b.EntryID {
			return a.EntryID < b.EntryID
		}
		return a.ID < b.ID
	})
	return rows
}
func ledgerColumns(period Period) []Column {
	return []Column{{ID: "debit", Label: "Debit", Kind: "amount", Period: period}, {ID: "credit", Label: "Credit", Kind: "amount", Period: period}, {ID: "balance", Label: "Debit balance", Kind: "amount", Period: period}}
}
func buildGeneralLedger(in Input, r *Result) error {
	if r.Scope.Comparison != nil {
		return problem("comparison_not_supported_for_detailed_report")
	}
	r.Columns = ledgerColumns(r.Scope.Period)
	r.Limitations = append(r.Limitations, "General Ledger running balances use debit-positive, credit-negative signs.")
	accounts := append([]Account(nil), in.Accounts...)
	accountOrder(accounts)
	selected := map[string]bool{}
	for _, id := range r.Request.AccountIDs {
		selected[id] = true
	}
	postings := map[string][]Posting{}
	for _, p := range sortedPostings(in) {
		postings[p.AccountID] = append(postings[p.AccountID], p)
	}
	for _, a := range accounts {
		if a.Virtual || (len(selected) > 0 && !selected[a.ID]) {
			continue
		}
		opening, closing, debit, credit := zero(), zero(), zero(), zero()
		for _, p := range postings[a.ID] {
			if p.AccountID == a.ID && p.Date < r.Scope.Period.From {
				opening.Add(opening, signed(p))
			}
		}
		closing.Set(opening)
		row := simpleRow("account:"+a.ID, a.Name, "account", map[string]string{}, &Drilldown{AccountIDs: []string{a.ID}, To: r.Scope.Period.To})
		row.AccountID = a.ID
		row.AccountCode = a.Code
		row.GLCode = a.GLCode
		row.Children = append(row.Children, simpleRow("opening:"+a.ID, "Opening balance", "opening", map[string]string{"debit": "0", "credit": "0", "balance": opening.String()}, &Drilldown{AccountIDs: []string{a.ID}, To: previousDate(r.Scope.Period.From)}))
		for _, p := range postings[a.ID] {
			if p.AccountID != a.ID || !inPeriod(p, r.Scope.Period) {
				continue
			}
			closing.Add(closing, signed(p))
			d, c := "0", "0"
			if p.Side == "debit" {
				d = p.Amount
				debit.Add(debit, integer(p.Amount))
			} else {
				c = p.Amount
				credit.Add(credit, integer(p.Amount))
			}
			label := "Journal posting"
			if p.EntryReference != "" {
				label = "Entry " + p.EntryReference
			}
			line := simpleRow("posting:"+p.ID, label, "posting", map[string]string{"debit": d, "credit": c, "balance": closing.String()}, &Drilldown{AccountIDs: []string{a.ID}, From: p.Date, To: p.Date, EntryID: p.EntryID, PostingID: p.ID})
			line.AccountID = a.ID
			line.AccountCode = a.Code
			line.GLCode = a.GLCode
			line.Date = p.Date
			line.EntryID = p.EntryID
			line.EntryReference = p.EntryReference
			line.PostingID = p.ID
			row.Children = append(row.Children, line)
		}
		row.Values = map[string]string{"debit": debit.String(), "credit": credit.String(), "balance": closing.String()}
		row.OwnValues = row.Values
		row.Children = append(row.Children, simpleRow("closing:"+a.ID, "Closing balance", "closing", map[string]string{"debit": debit.String(), "credit": credit.String(), "balance": closing.String()}, &Drilldown{AccountIDs: []string{a.ID}, To: r.Scope.Period.To}))
		r.Rows = append(r.Rows, row)
	}
	r.Checks = append(r.Checks, Check{ID: "general_ledger_reconstruction", Status: "pass", Message: "Every selected posting is retained once, including zero-valued postings, with opening and closing balances."})
	return nil
}
func previousDate(date string) string {
	t, _ := time.Parse(time.DateOnly, date)
	return t.AddDate(0, 0, -1).Format(time.DateOnly)
}
func buildJournal(in Input, r *Result) error {
	if r.Scope.Comparison != nil {
		return problem("comparison_not_supported_for_detailed_report")
	}
	r.Columns = ledgerColumns(r.Scope.Period)[:2]
	accounts := map[string]Account{}
	for _, a := range in.Accounts {
		accounts[a.ID] = a
	}
	entryIndex := map[string]int{}
	totalD, totalC := zero(), zero()
	for _, p := range sortedPostings(in) {
		if !inPeriod(p, r.Scope.Period) {
			continue
		}
		i, exists := entryIndex[p.EntryID]
		if !exists {
			i = len(r.Rows)
			entryIndex[p.EntryID] = i
			label := "Journal entry"
			if p.EntryReference != "" {
				label = "Entry " + p.EntryReference
			}
			entry := simpleRow("entry:"+p.EntryID, label, "entry", map[string]string{"debit": "0", "credit": "0"}, &Drilldown{AccountIDs: []string{}, From: p.Date, To: p.Date, EntryID: p.EntryID})
			entry.EntryID = p.EntryID
			entry.EntryReference = p.EntryReference
			entry.Date = p.Date
			r.Rows = append(r.Rows, entry)
		}
		d, c := "0", "0"
		if p.Side == "debit" {
			d = p.Amount
			totalD.Add(totalD, integer(d))
		} else {
			c = p.Amount
			totalC.Add(totalC, integer(c))
		}
		line := simpleRow("posting:"+p.ID, accounts[p.AccountID].Name, "posting", map[string]string{"debit": d, "credit": c}, &Drilldown{AccountIDs: []string{p.AccountID}, From: p.Date, To: p.Date, EntryID: p.EntryID, PostingID: p.ID})
		line.AccountID = p.AccountID
		line.AccountCode = accounts[p.AccountID].Code
		line.GLCode = accounts[p.AccountID].GLCode
		line.Date = p.Date
		line.EntryID = p.EntryID
		line.EntryReference = p.EntryReference
		line.PostingID = p.ID
		r.Rows[i].Children = append(r.Rows[i].Children, line)
		r.Rows[i].Values["debit"] = new(big.Int).Add(integer(r.Rows[i].Values["debit"]), integer(d)).String()
		r.Rows[i].Values["credit"] = new(big.Int).Add(integer(r.Rows[i].Values["credit"]), integer(c)).String()
	}
	r.Reconciliation = map[string]string{"debit": totalD.String(), "credit": totalC.String()}
	r.Checks = append(r.Checks, Check{ID: "journal_reconstruction", Status: "pass", Message: "Selected complete entries preserve every posting identity and debit/credit amount."})
	return nil
}

// PeriodFor is an adapter convenience that exposes normalized date semantics.
func PeriodFor(in Input, req Request) (Period, *Period, error) {
	_, period, prior, e := normalizeRequest(in, req)
	return period, prior, e
}

// CheckResultDigest protects same-result transport against accidental alteration.
// It is not a signature or authorization capability; adapters supply trusted
// server-calculated results and enforce existing Book authority independently.
func CheckResultDigest(r Result) bool {
	old := r.Digest
	r.Digest = ""
	return old != "" && digest(r) == old
}

// SafeCSVText marks formula-like text as literal in spreadsheet programs.
func SafeCSVText(s string) string {
	trim := strings.TrimLeft(s, " \t\r\n")
	if trim != "" && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + s
	}
	if strings.ContainsAny(s, "\t\r") {
		return "'" + s
	}
	return s
}

func sortedIDs(ids []string) []string {
	sort.Strings(ids)
	return ids
}

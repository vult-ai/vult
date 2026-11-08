package reporting

import (
	"bytes"
	"encoding/csv"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

const MaxCSVBytes = 32 << 20

// Decimal formats an exact integer minor-unit value without a float conversion.
func Decimal(value string, scale int) (string, error) {
	if !signedInteger.MatchString(value) || scale < 0 || scale > 9 {
		return "", problem("invalid_export_amount")
	}
	negative := strings.HasPrefix(value, "-")
	digits := strings.TrimPrefix(value, "-")
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		i := len(digits) - scale
		digits = digits[:i] + "." + digits[i:]
	}
	if negative && integer(value).Sign() != 0 {
		digits = "-" + digits
	}
	return digits, nil
}

// CSV serializes the authoritative Result. It never reads or recalculates a
// changing ledger, and never silently truncates a detailed export.
func CSV(r Result) ([]byte, error) {
	if !CheckResultDigest(r) {
		return nil, problem("invalid_result_digest")
	}
	var buffer bytes.Buffer
	w := csv.NewWriter(&buffer)
	write := func(row []string) error {
		if e := w.Write(row); e != nil {
			return e
		}
		w.Flush()
		if e := w.Error(); e != nil {
			return e
		}
		if buffer.Len() > MaxCSVBytes {
			return problem("report_export_too_large")
		}
		return nil
	}
	meta := [][]string{{"Report", SafeCSVText(r.Title)}, {"Book", SafeCSVText(r.Book.Name)}, {"Book ID", SafeCSVText(r.Book.ID)}, {"Currency", r.Book.Currency}, {"Recognition basis", SafeCSVText(r.Book.RecognitionBasis)}, {"Reporting framework", SafeCSVText(r.Book.ReportingFramework)}, {"Mode", r.Scope.Mode}, {"Status", r.Status}, {"From", r.Scope.Period.From}, {"Through", r.Scope.Period.To}, {"Ledger revision", SafeCSVText(r.Scope.LedgerRevision)}, {"Knowledge cutoff", SafeCSVText(r.Scope.KnowledgeCutoff)}, {"Mapping version", SafeCSVText(r.Scope.MappingVersion)}, {"Hierarchy basis", SafeCSVText(r.Scope.HierarchyBasis)}, {"Population digest", r.Scope.PopulationDigest}, {"Result digest", r.Digest}, {"Coverage", r.Scope.Coverage}, {"Opening balances", r.Scope.OpeningBalances}}
	if r.Scope.Comparison != nil {
		meta = append(meta, []string{"Comparison from", r.Scope.Comparison.From}, []string{"Comparison through", r.Scope.Comparison.To})
	}
	for _, row := range meta {
		if e := write(row); e != nil {
			return nil, e
		}
	}
	for _, check := range r.Checks {
		if e := write([]string{"Check", SafeCSVText(check.ID), check.Status, SafeCSVText(check.Message)}); e != nil {
			return nil, e
		}
	}
	for _, limit := range r.Limitations {
		if e := write([]string{"Limitation", SafeCSVText(limit)}); e != nil {
			return nil, e
		}
	}
	if e := write([]string{}); e != nil {
		return nil, e
	}
	header := []string{"Row ID", "Parent row ID", "Depth", "Role", "Account ID", "Account code", "Label", "Date", "Entry ID", "Posting ID"}
	for _, col := range r.Columns {
		header = append(header, SafeCSVText(col.Label)+" ("+r.Book.Currency+")")
	}
	header = append(header, "GL code")
	if e := write(header); e != nil {
		return nil, e
	}
	var visit func([]Row, string, int) error
	visit = func(rows []Row, parent string, depth int) error {
		for _, row := range rows {
			cells := []string{SafeCSVText(row.ID), SafeCSVText(parent), strconv.Itoa(depth), SafeCSVText(row.Role), SafeCSVText(row.AccountID), SafeCSVText(row.AccountCode), SafeCSVText(row.Label), row.Date, SafeCSVText(row.EntryID), SafeCSVText(row.PostingID)}
			for _, col := range r.Columns {
				value, ok := row.Values[col.ID]
				if !ok {
					cells = append(cells, "")
					continue
				}
				text, e := Decimal(value, r.Book.Scale)
				if e != nil {
					return e
				}
				cells = append(cells, text)
			}
			cells = append(cells, SafeCSVText(row.GLCode))
			if e := write(cells); e != nil {
				return e
			}
			if e := visit(row.Children, row.ID, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := visit(r.Rows, "", 0); e != nil {
		return nil, e
	}
	return buffer.Bytes(), nil
}

func findRow(rows []Row, id string) *Row {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
		if row := findRow(rows[i].Children, id); row != nil {
			return row
		}
	}
	return nil
}

// Contributions explains the exact trusted result, not a new live query. The
// transport must supply a server-built result and enforce Book authorization.
// Digest checking prevents accidental stale/altered scope; it is no signature.
func Contributions(in Input, r Result, rowID, columnID string) (ContributionResult, error) {
	out := ContributionResult{ResultDigest: r.Digest, RowID: rowID, ColumnID: columnID, Contributions: []Contribution{}, Debit: "0", Credit: "0", Total: "0"}
	if e := ValidateInput(in); e != nil {
		return out, e
	}
	if in.Book.ID != r.Book.ID || populationDigest(in) != r.Scope.PopulationDigest || !CheckResultDigest(r) {
		return out, problem("stale_report_population")
	}
	row := findRow(r.Rows, rowID)
	if row == nil {
		return out, problem("unknown_report_row")
	}
	expected, ok := row.Values[columnID]
	if !ok || !signedInteger.MatchString(expected) {
		return out, problem("unknown_report_column")
	}
	posts := map[string]Posting{}
	accounts := map[string]Account{}
	for _, p := range in.Postings {
		posts[p.ID] = p
	}
	for _, a := range in.Accounts {
		accounts[a.ID] = a
	}
	total, debit, credit := zero(), zero(), zero()
	rawSeen := map[string]bool{}
	appendContribution := func(p Posting, amount *big.Int, phase, category, reference string) {
		out.Contributions = append(out.Contributions, Contribution{PostingID: p.ID, EntryID: p.EntryID, EntryReference: p.EntryReference, AccountID: p.AccountID, Date: p.Date, Side: p.Side, Amount: p.Amount, SignedAmount: signed(p).String(), Treatment: treatment(p), ColumnID: phase, ReportAmount: amount.String(), Category: category, TreatmentReference: reference})
		total.Add(total, amount)
		if !rawSeen[p.ID] {
			if p.Side == "debit" {
				debit.Add(debit, integer(p.Amount))
			} else {
				credit.Add(credit, integer(p.Amount))
			}
			rawSeen[p.ID] = true
		}
	}
	if len(row.Allocations) > 0 {
		for _, a := range row.Allocations {
			if a.ColumnID != columnID {
				continue
			}
			p, exists := posts[a.PostingID]
			if !exists || !signedInteger.MatchString(a.Amount) || a.TreatmentReference == "" {
				return out, problem("invalid_report_allocation")
			}
			appendContribution(p, integer(a.Amount), columnID, a.Category, a.TreatmentReference)
		}
	} else {
		if row.Drilldown == nil {
			if expected == "0" {
				return out, nil
			}
			return out, problem("unqualified_contribution_scope")
		}
		d := *row.Drilldown
		// An empty financial section has no contributing accounts. Only an
		// explicitly identified Journal entry may intentionally select all its
		// accounts; empty must never silently mean the entire Book.
		if len(d.AccountIDs) == 0 && d.EntryID == "" && d.PostingID == "" {
			if expected == "0" {
				return out, nil
			}
			return out, problem("unqualified_contribution_scope")
		}
		selected := map[string]bool{}
		for _, id := range d.AccountIDs {
			selected[id] = true
		}
		period := Period{From: d.From, To: d.To}
		phase := "current"
		if columnID == "prior" || columnID == "prior_debit" || columnID == "prior_credit" {
			if r.Scope.Comparison == nil {
				return out, problem("missing_comparison_scope")
			}
			period = *r.Scope.Comparison
			if d.Comparison != nil {
				period = *d.Comparison
			}
			phase = "prior"
		}
		type window struct {
			period Period
			sign   int
			phase  string
		}
		windows := []window{{period, 1, phase}}
		if columnID == "variance" {
			if r.Scope.Comparison == nil {
				return out, problem("missing_comparison_scope")
			}
			previous := *r.Scope.Comparison
			if d.Comparison != nil {
				previous = *d.Comparison
			}
			windows = []window{{Period{From: d.From, To: d.To}, 1, "current"}, {previous, -1, "prior"}}
		}
		ordered := sortedPostings(in)
		for _, win := range windows {
			active := win.period
			if r.Report == GeneralLedger && (columnID == "debit" || columnID == "credit") && row.Role != "posting" {
				active = r.Scope.Period
			}
			if r.Report == GeneralLedger && columnID == "balance" && row.Role == "posting" {
				active = Period{To: row.Date}
			}
			balances := map[string]*big.Int{}
			if r.Report == TrialBalance {
				balances = valuesFor(in, Period{To: active.To}, TrialBalance)
			}
			for _, p := range ordered {
				if len(selected) > 0 && !selected[p.AccountID] {
					continue
				}
				if d.EntryID != "" && p.EntryID != d.EntryID && !(r.Report == GeneralLedger && columnID == "balance" && row.Role == "posting") {
					continue
				}
				if d.PostingID != "" && p.ID != d.PostingID && !(r.Report == GeneralLedger && columnID == "balance" && row.Role == "posting") {
					continue
				}
				if !inPeriod(p, active) || (d.ExcludeClosing && p.Treatment == "closing") || (d.ExcludeOpening && p.Treatment == "opening") {
					continue
				}
				if r.Report == GeneralLedger && columnID == "balance" && row.Role == "posting" {
					target := posts[row.PostingID]
					if p.Date > target.Date || (p.Date == target.Date && (p.EntryID > target.EntryID || (p.EntryID == target.EntryID && p.ID > target.ID))) {
						continue
					}
				}
				amount := signed(p)
				switch r.Report {
				case BalanceSheet:
					flip := creditClass(accounts[p.AccountID].Kind) || row.ID == "section:equity" || strings.HasPrefix(row.ID, "equity:")
					if flip {
						amount.Neg(amount)
					}
				case ProfitLoss:
					if row.ID == "total:result" || creditClass(accounts[p.AccountID].Kind) {
						amount.Neg(amount)
					}
				case TrialBalance:
					if columnID == "debit" || columnID == "prior_debit" {
						if balances[p.AccountID].Sign() <= 0 {
							continue
						}
					} else if columnID == "credit" || columnID == "prior_credit" {
						if balances[p.AccountID].Sign() >= 0 {
							continue
						}
						amount.Neg(amount)
					}
				case GeneralLedger, Journal:
					if row.Role == "opening" && (columnID == "debit" || columnID == "credit") {
						continue
					}
					if columnID == "debit" || columnID == "credit" {
						if p.Side != columnID {
							continue
						}
						amount.Set(integer(p.Amount))
					}
				}
				if win.sign < 0 {
					amount.Neg(amount)
				}
				appendContribution(p, amount, win.phase, "", "")
			}
		}
	}
	if total.Cmp(integer(expected)) != 0 {
		return out, problem("contribution_sum_mismatch")
	}
	sort.SliceStable(out.Contributions, func(i, j int) bool {
		a, b := out.Contributions[i], out.Contributions[j]
		if a.ColumnID != b.ColumnID {
			return a.ColumnID < b.ColumnID
		}
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.EntryID != b.EntryID {
			return a.EntryID < b.EntryID
		}
		return a.PostingID < b.PostingID
	})
	out.Debit = debit.String()
	out.Credit = credit.String()
	out.Total = total.String()
	return out, nil
}

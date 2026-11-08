package reporting

import "math/big"

// BuildCashMovement explains recorded cash-role changes, independently of a
// formal operating/investing/financing classification or bank observations.
func BuildCashMovement(in Input, req Request) (Result, error) {
	r, err := NewResult(in, req)
	if err != nil {
		return r, err
	}
	d, err := BuildDashboard(in, r.Scope.Period.From, r.Scope.Period.To)
	if err != nil {
		return Result{}, err
	}
	r.Findings = d.Findings
	r.Limitations = uniqueSorted(append(r.Limitations, d.Limitations...))
	if d.Cash == nil {
		r.Status = "unsupported"
		r.Checks = append(r.Checks, Check{ID: "recorded_cash_scope", Status: "unknown", Message: "No established cash account scope is available."})
		Finalize(&r)
		return r, nil
	}
	cash := map[string]bool{}
	for _, a := range in.Accounts {
		cash[a.ID] = a.Kind == "asset" && a.Role == "cash"
	}
	net := map[string]*big.Int{}
	for _, p := range sortedPostings(in) {
		if cash[p.AccountID] && inPeriod(p, r.Scope.Period) && cashFlowPosting(p) {
			if net[p.EntryID] == nil {
				net[p.EntryID] = zero()
			}
			net[p.EntryID].Add(net[p.EntryID], signed(p))
		}
	}
	rows := map[string]*Row{}
	add := func(id, label, role, amount string) {
		row := simpleRow(id, label, role, map[string]string{"current": amount}, nil)
		rows[id] = &row
	}
	add("cash:closing", "Recorded cash carrying subtotal", "closing", *d.Cash)
	if d.CashFlow != nil {
		add("cash:in", "Recorded money in", "total", *d.MoneyIn)
		add("cash:out", "Recorded money out", "total", *d.MoneyOut)
		add("cash:net", "Recorded net cash movement", "total", *d.CashFlow)
		add("cash:effects", "Established openings and valuation effects", "total", *d.CashEffects)
	}
	allocate := func(id string, p Posting, value *big.Int) {
		if row := rows[id]; row != nil {
			row.Allocations = append(row.Allocations, Allocation{PostingID: p.ID, ColumnID: "current", Amount: value.String(), Category: "recorded_cash_change", TreatmentReference: "recorded-cash-roles-v1"})
		}
	}
	for _, p := range in.Postings {
		if !cash[p.AccountID] || p.Date > r.Scope.Period.To {
			continue
		}
		allocate("cash:closing", p, signed(p))
		if p.Date < r.Scope.Period.From || d.CashFlow == nil {
			continue
		}
		if !cashFlowPosting(p) {
			allocate("cash:effects", p, signed(p))
			continue
		}
		allocate("cash:net", p, signed(p))
		if net[p.EntryID].Sign() > 0 {
			allocate("cash:in", p, signed(p))
		} else if net[p.EntryID].Sign() < 0 {
			allocate("cash:out", p, new(big.Int).Neg(signed(p)))
		}
	}
	for _, id := range []string{"cash:in", "cash:out", "cash:net", "cash:effects", "cash:closing"} {
		if row := rows[id]; row != nil {
			r.Rows = append(r.Rows, *row)
		}
	}
	r.Checks = append(r.Checks, Check{ID: "recorded_cash_scope", Status: "unknown", Message: "Recorded cash-role subtotals do not establish full cash-equivalent scope or bank-source completeness."})
	if d.CashFlow == nil {
		r.Checks = append(r.Checks, Check{ID: "cash_movement_treatment", Status: "unknown", Message: "Money-in/out totals are unavailable until transfer and valuation treatment is established."})
	}
	Finalize(&r)
	return r, nil
}

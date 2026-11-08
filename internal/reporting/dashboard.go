package reporting

import (
	"math/big"
	"sort"
	"time"
)

type DashboardPeriod = Period

type DashboardPoint struct {
	Date     string  `json:"date"`
	Profit   string  `json:"profit_minor"`
	CashFlow *string `json:"cash_flow_minor,omitempty"`
}

type DashboardAttention struct {
	Kind     string `json:"kind"`
	Account  string `json:"account"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Scale    int    `json:"scale"`
	Amount   string `json:"amount_minor"`
	Basis    string `json:"basis"`
}

type Dashboard struct {
	SchemaVersion int                  `json:"schema_version"`
	BookID        string               `json:"book_id"`
	Currency      string               `json:"currency"`
	Scale         int                  `json:"scale"`
	Period        Period               `json:"period"`
	Version       string               `json:"version"`
	Status        string               `json:"status"`
	Coverage      string               `json:"coverage"`
	Basis         string               `json:"basis"`
	Cash          *string              `json:"cash_minor,omitempty"`
	CashStatus    string               `json:"cash_status"`
	Profit        string               `json:"profit_minor"`
	CashFlow      *string              `json:"cash_flow_minor,omitempty"`
	CashFlowState string               `json:"cash_flow_status"`
	CashEffects   *string              `json:"cash_effects_minor,omitempty"`
	MoneyIn       *string              `json:"money_in_minor,omitempty"`
	MoneyOut      *string              `json:"money_out_minor,omitempty"`
	MovementCount *int                 `json:"movement_count,omitempty"`
	Transactions  int                  `json:"transaction_count"`
	LastDate      string               `json:"last_date"`
	Interval      string               `json:"interval"`
	Series        []DashboardPoint     `json:"series"`
	Attention     []DashboardAttention `json:"attention"`
	Findings      []Finding            `json:"findings"`
	Limitations   []string             `json:"limitations"`
}

func textAmount(n *big.Int) *string { value := n.String(); return &value }

func cashFlowPosting(p Posting) bool {
	return p.Treatment != "opening" && (p.NativeAmount == nil || *p.NativeAmount != "0")
}

// BuildDashboard summarizes recorded accounting activity. A cash role is an
// explicit account definition, never a guess from names or the presence of FX.
// These Book-currency changes are not bank observations or formal cash flows.
func BuildDashboard(in Input, from, to string) (Dashboard, error) {
	report, err := BuildCore(in, Request{BookID: in.Book.ID, Report: ProfitLoss, From: from, To: to})
	if err != nil {
		return Dashboard{}, err
	}
	out := Dashboard{SchemaVersion: 1, BookID: in.Book.ID, Currency: in.Book.Currency, Scale: in.Book.Scale, Period: Period{From: from, To: to}, Version: in.Provenance.LedgerRevision, Status: report.Status, Coverage: report.Scope.Coverage, Basis: "recorded_accounting_dates", Profit: report.Reconciliation["result"], CashStatus: "unavailable", CashFlowState: "unavailable", Series: []DashboardPoint{}, Attention: []DashboardAttention{}, Findings: append([]Finding{}, report.Findings...), Limitations: append([]string{}, report.Limitations...)}
	if out.Version == "" {
		out.Version = report.Scope.PopulationDigest
	}
	accounts, cash := map[string]Account{}, map[string]bool{}
	for _, account := range in.Accounts {
		accounts[account.ID] = account
		if account.Kind == "asset" && account.Role == "cash" {
			cash[account.ID] = true
		}
	}
	type event struct {
		date       string
		flow       *big.Int
		cashGross  *big.Int
		profit     *big.Int
		nativeMove bool
		cashIDs    map[string]bool
		noncash    bool
		equity     bool
		uncertain  bool
	}
	events := map[string]*event{}
	position, effects := zero(), zero()
	cashBalances := map[string]*big.Int{}
	for _, p := range in.Postings {
		if p.Date > to {
			continue
		}
		if p.Date > out.LastDate {
			out.LastDate = p.Date
		}
		if cash[p.AccountID] {
			position.Add(position, signed(p))
			if cashBalances[p.AccountID] == nil {
				cashBalances[p.AccountID] = zero()
			}
			cashBalances[p.AccountID].Add(cashBalances[p.AccountID], signed(p))
		}
		if p.Date < from {
			continue
		}
		e := events[p.EntryID]
		if e == nil {
			e = &event{date: p.Date, flow: zero(), cashGross: zero(), profit: zero(), cashIDs: map[string]bool{}}
			events[p.EntryID] = e
		}
		kind := class(accounts[p.AccountID].Kind)
		if (kind == "income" || kind == "expense") && earned(p) {
			e.profit.Sub(e.profit, signed(p))
		}
		if !cash[p.AccountID] {
			e.noncash = true
			e.equity = e.equity || kind == "equity"
			continue
		}
		e.cashIDs[p.AccountID] = true
		e.cashGross.Add(e.cashGross, integer(p.Amount))
		e.uncertain = e.uncertain || treatment(p) == "unknown"
		if !cashFlowPosting(p) {
			effects.Add(effects, signed(p))
			continue
		}
		e.flow.Add(e.flow, signed(p))
		e.nativeMove = e.nativeMove || p.NativeAmount != nil && *p.NativeAmount != "0"
	}
	out.Transactions = len(events)
	for id, amount := range cashBalances {
		if amount.Sign() < 0 {
			out.Attention = append(out.Attention, DashboardAttention{Kind: "negative_cash", Account: id, Name: accounts[id].Name, Currency: in.Book.Currency, Scale: in.Book.Scale, Amount: amount.String(), Basis: "recorded_book_carrying"})
		}
	}
	sort.Slice(out.Attention, func(i, j int) bool { return out.Attention[i].Account < out.Attention[j].Account })
	first, _ := time.Parse(time.DateOnly, from)
	last, _ := time.Parse(time.DateOnly, to)
	days := (last.Unix()-first.Unix())/86400 + 1
	out.Interval = "day"
	if days > 62 {
		out.Interval = "month"
	}
	if days > 5*366 {
		out.Interval = "year"
	}
	bucketKey := func(date string) string {
		switch out.Interval {
		case "month":
			return date[:7] + "-01"
		case "year":
			return date[:4] + "-01-01"
		default:
			return date
		}
	}
	profitBuckets, cashBuckets := map[string]*big.Int{}, map[string]*big.Int{}
	flow, moneyIn, moneyOut := zero(), zero(), zero()
	count := 0
	openingIDs, ambiguousIDs := []string{}, []string{}
	openingGross, ambiguousGross := zero(), zero()
	for id, e := range events {
		key := bucketKey(e.date)
		if profitBuckets[key] == nil {
			profitBuckets[key], cashBuckets[key] = zero(), zero()
		}
		profitBuckets[key].Add(profitBuckets[key], e.profit)
		cashBuckets[key].Add(cashBuckets[key], e.flow)
		flow.Add(flow, e.flow)
		if e.flow.Sign() > 0 {
			moneyIn.Add(moneyIn, e.flow)
		} else {
			moneyOut.Sub(moneyOut, e.flow)
		}
		if e.flow.Sign() != 0 || e.noncash && e.nativeMove {
			count++
		}
		if len(e.cashIDs) > 0 && e.equity && e.uncertain && e.flow.Sign() != 0 {
			openingIDs = append(openingIDs, id)
			openingGross.Add(openingGross, new(big.Int).Abs(e.flow))
		}
		if len(e.cashIDs) > 1 && e.noncash {
			ambiguousIDs = append(ambiguousIDs, id)
			ambiguousGross.Add(ambiguousGross, e.cashGross)
		}
	}
	if len(cash) > 0 {
		out.Cash, out.CashStatus = textAmount(position), "partial"
		out.CashFlowState = "partial"
		if len(ambiguousIDs) == 0 {
			out.CashFlow, out.CashEffects = textAmount(flow), textAmount(effects)
			out.MoneyIn, out.MoneyOut, out.MovementCount = textAmount(moneyIn), textAmount(moneyOut), &count
		} else {
			out.CashFlowState = "unavailable"
			sort.Strings(ambiguousIDs)
			out.Findings = append(out.Findings, Finding{Code: "cash_transfer_treatment_unconfirmed", EntryIDs: ambiguousIDs, AmountMinor: ambiguousGross.String(), Message: "A change across multiple cash accounts has additional counterparts. Transfer, fee and valuation treatment must be established before money-in/out totals are shown."})
		}
		out.Limitations = append(out.Limitations, "Cash is the recorded Book carrying subtotal of accounts explicitly assigned the cash role. Scope and source completeness are not established; it is not an observed bank balance.")
	} else {
		out.Limitations = append(out.Limitations, "Cash account scope is not established. Missing cash totals are unavailable, not zero.")
	}
	if len(openingIDs) > 0 {
		sort.Strings(openingIDs)
		out.Findings = append(out.Findings, Finding{Code: "opening_treatment_unconfirmed", EntryIDs: openingIDs, AmountMinor: openingGross.String(), Message: "Cash posted against equity is included as recorded. Opening or contribution treatment has not been established."})
	}
	for cursor := first; !cursor.After(last); {
		if len(out.Series) == 1000 {
			return Dashboard{}, problem("dashboard_period_too_long")
		}
		key := bucketKey(cursor.Format(time.DateOnly))
		profit, cashFlow := zero(), zero()
		if value := profitBuckets[key]; value != nil {
			profit.Set(value)
			cashFlow.Set(cashBuckets[key])
		}
		point := DashboardPoint{Date: key, Profit: profit.String()}
		if out.CashFlow != nil {
			point.CashFlow = textAmount(cashFlow)
		}
		out.Series = append(out.Series, point)
		switch out.Interval {
		case "month":
			cursor = time.Date(cursor.Year(), cursor.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case "year":
			cursor = time.Date(cursor.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
		default:
			cursor = cursor.AddDate(0, 0, 1)
		}
	}
	out.Limitations = uniqueSorted(out.Limitations)
	return out, nil
}

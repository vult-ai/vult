package reporting

import (
	"math/big"
	"sort"
)

type DashboardProjection struct {
	*Dashboard
	Contract    DashboardProjectionContract `json:"projection_contract"`
	Financial   RecognitionPosition         `json:"financial"`
	Comparison  RecognitionComparison       `json:"overviewComparison"`
	Performance RecognitionPerformance      `json:"performance"`
}
type DashboardProjectionContract struct {
	Version         int    `json:"version"`
	BookID          string `json:"book_id"`
	EventKind       string `json:"event_kind"`
	Precision       string `json:"precision"`
	Currency        string `json:"currency"`
	Scale           int    `json:"scale"`
	ReportingZone   string `json:"reporting_zone"`
	LedgerRevision  string `json:"ledger_revision"`
	KnowledgeCutoff string `json:"knowledge_cutoff"`
	Coverage        string `json:"coverage"`
	MappingVersion  string `json:"mapping_version"`
}
type RecognitionRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Amount string `json:"amount"`
	Role   string `json:"role,omitempty"`
}
type RecognitionPosition struct {
	Assets            string           `json:"assets"`
	Liabilities       string           `json:"liabilities"`
	Equity            string           `json:"equity"`
	EquityAccounts    string           `json:"equityAccounts"`
	AccumulatedResult string           `json:"accumulatedResult"`
	Income            string           `json:"income"`
	Expenses          string           `json:"expenses"`
	Result            string           `json:"result"`
	Through           string           `json:"through"`
	Rows              []RecognitionRow `json:"rows"`
	IncomeRows        []RecognitionRow `json:"incomeRows"`
	ExpenseRows       []RecognitionRow `json:"expenseRows"`
}
type RecognitionPoint struct {
	Date        string `json:"date"`
	Assets      string `json:"assets"`
	Liabilities string `json:"liabilities"`
	Equity      string `json:"equity"`
}
type RecognitionComparison struct {
	Current       []RecognitionPoint `json:"current"`
	Previous      []RecognitionPoint `json:"previous"`
	Range         DashboardPeriod    `json:"range"`
	PreviousRange *DashboardPeriod   `json:"previousRange,omitempty"`
	PreviousState string             `json:"previous_status"`
}
type recognitionDelta struct {
	Code, Name, Kind, Date string
	Amount                 big.Int
	Earned, Cash           bool
}

func recognitionKind(kind string) string {
	switch class(kind) {
	case "income":
		return "income"
	case "expense":
		return "expenses"
	}
	return ""
}
func recognitionCredit(kind string) bool {
	return kind == "liability" || kind == "equity" || recognitionKind(kind) == "income"
}
func recognitionPosition(rows []recognitionDelta, accountByID map[string]Account, period DashboardPeriod) (RecognitionPosition, error) {
	out := RecognitionPosition{Through: period.To, Rows: []RecognitionRow{}, IncomeRows: []RecognitionRow{}, ExpenseRows: []RecognitionRow{}}
	accounts := map[string]*recognitionDelta{}
	balances := map[string]*big.Int{}
	during := map[string]*big.Int{}
	for i := range rows {
		r := &rows[i]
		if r.Date > period.To {
			continue
		}
		accounts[r.Code] = r
		if balances[r.Code] == nil {
			balances[r.Code] = new(big.Int)
			during[r.Code] = new(big.Int)
		}
		balances[r.Code].Add(balances[r.Code], &r.Amount)
		if r.Date >= period.From && r.Earned {
			during[r.Code].Add(during[r.Code], &r.Amount)
		}
	}
	assets, liabilities, equity, accumulated, income, expenses, balance := new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	codes := make([]string, 0, len(accounts))
	for code := range accounts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		r := accounts[code]
		n := balances[code]
		balance.Add(balance, n)
		v := new(big.Int).Set(n)
		if recognitionCredit(r.Kind) {
			v.Neg(v)
		}
		if n.Sign() != 0 || r.Kind == "unknown" {
			out.Rows = append(out.Rows, RecognitionRow{Code: code, Name: r.Name, Kind: r.Kind, Amount: v.String(), Role: accountByID[code].Role})
		}
		switch r.Kind {
		case "asset":
			assets.Add(assets, n)
		case "liability":
			liabilities.Sub(liabilities, n)
		case "equity":
			equity.Sub(equity, n)
		case "unknown":
			// Keep the exact balance in its own row without guessing its class.
		default:
			if recognitionKind(r.Kind) == "" && n.Sign() != 0 {
				return out, problem("an_account_needs_an_accounting_category")
			}
		}
		kind := recognitionKind(r.Kind)
		if kind != "" {
			accumulated.Sub(accumulated, n)
		}
		p := new(big.Int).Set(during[code])
		if kind == "income" {
			p.Neg(p)
			income.Add(income, p)
			if p.Sign() != 0 {
				out.IncomeRows = append(out.IncomeRows, RecognitionRow{Code: code, Name: r.Name, Kind: r.Kind, Amount: p.String()})
			}
		}
		if kind == "expenses" {
			expenses.Add(expenses, p)
			if p.Sign() != 0 {
				out.ExpenseRows = append(out.ExpenseRows, RecognitionRow{Code: code, Name: r.Name, Kind: r.Kind, Amount: p.String()})
			}
		}
	}
	if balance.Sign() != 0 {
		return out, problem("the_recorded_account_totals_do_not_balance")
	}
	out.Assets = assets.String()
	out.Liabilities = liabilities.String()
	out.EquityAccounts = equity.String()
	out.AccumulatedResult = accumulated.String()
	// Tracked assets less liabilities stay readable while unknown counterparts
	// remain explicit. Never manufacture an asset, income or expense from them.
	out.Equity = new(big.Int).Sub(assets, liabilities).String()
	out.Income = income.String()
	out.Expenses = expenses.String()
	out.Result = new(big.Int).Sub(income, expenses).String()
	return out, nil
}

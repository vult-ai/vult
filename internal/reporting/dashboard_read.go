package reporting

import (
	"math/big"
	"sort"
	"time"
)

// BuildDashboardProjection reuses the recorded-ledger contribution rules. Chart
// buckets contain whole accounting dates, never fabricated occurrence times.
func BuildDashboardProjection(in Input, from, to, unit string) (DashboardProjection, error) {
	base, err := BuildDashboard(in, from, to)
	if err != nil {
		return DashboardProjection{}, err
	}
	rows, accountByID := recognitionRows(in, to)
	out := DashboardProjection{Dashboard: &base}
	out.Financial, out.Performance, err = dashboardFigures(rows, accountByID, base.Period, unit, in.Book.AccountingTimezone)
	if err != nil {
		return DashboardProjection{}, err
	}
	if out.Financial.Result != base.Profit {
		return DashboardProjection{}, problem("dashboard_result_mismatch")
	}
	out.Comparison, _, _, _ = dashboardTrends(rows, base.Period, nil, false)
	out.Contract = DashboardProjectionContract{Version: 2, BookID: in.Book.ID, EventKind: "accounting_recognition", Precision: "date", Currency: in.Book.Currency, Scale: in.Book.Scale, LedgerRevision: base.Version, KnowledgeCutoff: in.Provenance.KnowledgeCutoff, Coverage: base.Coverage, MappingVersion: "recorded-accounting-calendar-v2", ReportingZone: in.Book.AccountingTimezone}
	return out, nil
}

type DashboardCashAccount struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Carrying string `json:"carrying_minor"`
}

type DashboardActivity struct {
	Income   string `json:"income"`
	Expenses string `json:"expenses"`
	Result   string `json:"result"`
}

type DashboardTrendPoint struct {
	DashboardActivity
	Date string  `json:"date"`
	Cash *string `json:"cash_minor,omitempty"`
}

type DashboardTrends struct {
	Current  []DashboardTrendPoint `json:"current"`
	Previous []DashboardTrendPoint `json:"previous"`
}

type DashboardComparison struct {
	DashboardActivity
	Period Period `json:"period"`
}

type DashboardSnapshot struct {
	SchemaVersion int                    `json:"schema_version"`
	BookID        string                 `json:"book_id"`
	Currency      string                 `json:"currency"`
	Scale         int                    `json:"scale"`
	Period        Period                 `json:"period"`
	Financial     RecognitionPosition    `json:"financial"`
	PositionTrend RecognitionComparison  `json:"overviewComparison"`
	Performance   RecognitionPerformance `json:"performance"`
	Cash          *string                `json:"cash_minor,omitempty"`
	CashStatus    string                 `json:"cash_status"`
	CashAccounts  []DashboardCashAccount `json:"cash_accounts"`
	PriorYear     *DashboardComparison   `json:"prior_year,omitempty"`
	Trends        DashboardTrends        `json:"trends"`
}

// BuildDashboardSnapshot builds the Dashboard's carrying positions, chart, and
// comparisons from one population of complete recorded entries.
func BuildDashboardSnapshot(in Input, from, to, unit string) (DashboardSnapshot, error) {
	req := Request{BookID: in.Book.ID, Report: ProfitLoss, From: from, To: to}
	// Year one has no representable earlier year.
	if dateValid(from) && from[:4] != "0001" {
		req.Comparison = "previous_year"
	}
	period, prior, err := PeriodFor(in, req)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	if err := ValidateInput(in); err != nil {
		return DashboardSnapshot{}, err
	}
	rows, accountByID := recognitionRows(in, to)
	out := DashboardSnapshot{
		SchemaVersion: 1, BookID: in.Book.ID, Currency: in.Book.Currency, Scale: in.Book.Scale,
		Period: period, CashStatus: "unavailable", CashAccounts: []DashboardCashAccount{},
	}
	out.Financial, out.Performance, err = dashboardFigures(rows, accountByID, period, unit, in.Book.AccountingTimezone)
	if err != nil {
		return DashboardSnapshot{}, err
	}
	balances := make(map[string]string, len(out.Financial.Rows))
	for _, row := range out.Financial.Rows {
		if row.Kind == "asset" {
			balances[row.Code] = row.Amount
		}
	}
	for _, account := range in.Accounts {
		if account.Kind == "asset" && account.Role == "cash" {
			amount, ok := balances[account.ID]
			if !ok {
				amount = "0"
			}
			out.CashAccounts = append(out.CashAccounts, DashboardCashAccount{account.ID, account.Name, amount})
		}
	}
	if len(out.CashAccounts) > 0 {
		out.CashStatus = "partial"
	}
	out.PositionTrend, out.Trends, out.PriorYear, out.Cash = dashboardTrends(rows, period, prior, len(out.CashAccounts) > 0)
	return out, nil
}

func recognitionRows(in Input, through string) ([]recognitionDelta, map[string]Account) {
	accounts := make(map[string]Account, len(in.Accounts))
	for _, a := range in.Accounts {
		accounts[a.ID] = a
	}
	rows := make([]recognitionDelta, 0, len(in.Postings))
	for _, p := range in.Postings {
		if p.Date <= through {
			a := accounts[p.AccountID]
			rows = append(rows, recognitionDelta{
				Code: a.ID, Name: a.Name, Kind: a.Kind, Date: p.Date, Amount: *signed(p), Earned: earned(p),
				Cash: a.Kind == "asset" && a.Role == "cash",
			})
		}
	}
	return rows, accounts
}

func dashboardFigures(rows []recognitionDelta, accountByID map[string]Account, period Period, unit, zone string) (RecognitionPosition, RecognitionPerformance, error) {
	position, err := recognitionPosition(rows, accountByID, period)
	if err != nil {
		return position, RecognitionPerformance{}, err
	}
	performance, err := calendarPerformance(rows, period, unit, zone)
	if err == nil && performance.Status == "recorded" && (performance.Income != position.Income || performance.Expenses != position.Expenses) {
		err = problem("dashboard_chart_total_mismatch")
	}
	return position, performance, err
}

func daysInPeriod(start, end time.Time) int {
	return int((end.Unix()-start.Unix())/86400) + 1
}

type activityTotal struct {
	income, expenses big.Int
}

func (a *activityTotal) add(kind string, amount *big.Int) {
	switch kind {
	case "income":
		a.income.Sub(&a.income, amount)
	case "expenses":
		a.expenses.Add(&a.expenses, amount)
	}
}

func (a *activityTotal) value() DashboardActivity {
	return DashboardActivity{a.income.String(), a.expenses.String(), new(big.Int).Sub(&a.income, &a.expenses).String()}
}

func dashboardTrends(rows []recognitionDelta, period Period, prior *Period, hasCash bool) (RecognitionComparison, DashboardTrends, *DashboardComparison, *string) {
	position := RecognitionComparison{Range: period, Current: []RecognitionPoint{}, Previous: []RecognitionPoint{}, PreviousState: "unavailable"}
	activity := DashboardTrends{Current: []DashboardTrendPoint{}, Previous: []DashboardTrendPoint{}}
	type sample struct {
		date   string
		series int
	}
	const (
		current = iota
		previousPosition
		previousActivity
	)
	var samples []sample
	schedule := func(window Period, series int) {
		start, _ := time.Parse(time.DateOnly, window.From)
		end, _ := time.Parse(time.DateOnly, window.To)
		days := daysInPeriod(start, end)
		count := min(14, days)
		for i := range count {
			offset := 0
			if count > 1 {
				offset = (i*(days-1) + (count-1)/2) / (count - 1)
			}
			samples = append(samples, sample{start.AddDate(0, 0, offset).Format(time.DateOnly), series})
		}
	}
	schedule(period, current)
	first, _ := time.Parse(time.DateOnly, period.From)
	last, _ := time.Parse(time.DateOnly, period.To)
	if start := first.AddDate(0, 0, -daysInPeriod(first, last)); start.Year() >= 1 {
		position.PreviousState = "recorded"
		position.PreviousRange = &Period{From: start.Format(time.DateOnly), To: first.AddDate(0, 0, -1).Format(time.DateOnly)}
		schedule(*position.PreviousRange, previousPosition)
	}
	if prior != nil {
		schedule(*prior, previousActivity)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].date < samples[j].date })
	byDate := func(i, j int) bool { return rows[i].Date < rows[j].Date }
	if !sort.SliceIsSorted(rows, byDate) {
		sort.Slice(rows, byDate)
	}

	// All series share one chronological walk. Positions include the full
	// history to each sample; each result resets at its own period start.
	var assets, liabilities, cash big.Int
	var selected, earlier activityTotal
	cursor := 0
	for _, point := range samples {
		for cursor < len(rows) && rows[cursor].Date <= point.date {
			row := &rows[cursor]
			cursor++
			switch row.Kind {
			case "asset":
				assets.Add(&assets, &row.Amount)
				if row.Cash {
					cash.Add(&cash, &row.Amount)
				}
			case "liability":
				liabilities.Sub(&liabilities, &row.Amount)
			}
			if row.Earned {
				kind := recognitionKind(row.Kind)
				if row.Date >= period.From {
					selected.add(kind, &row.Amount)
				}
				if prior != nil && row.Date >= prior.From && row.Date <= prior.To {
					earlier.add(kind, &row.Amount)
				}
			}
		}
		// Leave dates before the first recorded entry unobserved.
		if cursor == 0 {
			continue
		}
		if point.series != previousActivity {
			p := RecognitionPoint{point.date, assets.String(), liabilities.String(), new(big.Int).Sub(&assets, &liabilities).String()}
			if point.series == current {
				position.Current = append(position.Current, p)
			} else {
				position.Previous = append(position.Previous, p)
			}
		}
		if point.series == current || point.series == previousActivity {
			trend := DashboardTrendPoint{Date: point.date}
			if hasCash {
				trend.Cash = textAmount(&cash)
			}
			if point.series == current {
				trend.DashboardActivity = selected.value()
				activity.Current = append(activity.Current, trend)
			} else {
				trend.DashboardActivity = earlier.value()
				activity.Previous = append(activity.Previous, trend)
			}
		}
	}
	var comparison *DashboardComparison
	if prior != nil {
		comparison = &DashboardComparison{earlier.value(), *prior}
	}
	var cashTotal *string
	if hasCash {
		cashTotal = textAmount(&cash)
	}
	return position, activity, comparison, cashTotal
}

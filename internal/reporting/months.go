package reporting

import (
	"context"
	"math/big"
	"slices"
	"time"
)

// MonthPeriods splits date-only reporting windows; it never allocates amounts.
func MonthPeriods(from, to string) ([]Period, error) {
	start, e := time.Parse("2006-01-02", from)
	end, endErr := time.Parse("2006-01-02", to)
	if e != nil || endErr != nil || !dateValid(from) || !dateValid(to) || start.After(end) {
		return nil, problem("invalid_month_period")
	}
	var periods []Period
	for !start.After(end) {
		if len(periods) == 12 {
			return nil, problem("month_period_too_long")
		}
		next := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		last := next.AddDate(0, 0, -1)
		if last.After(end) {
			last = end
		}
		periods = append(periods, Period{From: start.Format("2006-01-02"), To: last.Format("2006-01-02")})
		start = next
	}
	return periods, nil
}

// MonthRequests uses the same latest-through population for every comparison.
// Each result remains inspectable with its own canonical request/digest/cutoff.
func MonthRequests(base Result, periods []Period) ([]Request, error) {
	if (base.Report != BalanceSheet && base.Report != ProfitLoss) || base.Status == "unsupported" || len(periods) == 0 || len(periods) > 12 || periods[len(periods)-1].To != base.Scope.Period.To || (base.Report == ProfitLoss && periods[0].From != base.Scope.Period.From) {
		return nil, problem("invalid_month_scope")
	}
	anchor := base.Scope.Period.To
	if base.Scope.Comparison != nil && base.Scope.Comparison.To > anchor {
		anchor = base.Scope.Comparison.To
	}
	requests := make([]Request, 0, len(periods))
	for _, period := range periods {
		r := base.Request
		r.Comparison = "explicit"
		r.CompareTo = period.To
		r.CompareFrom = ""
		if base.Report == BalanceSheet {
			r.AsOf = anchor
		} else {
			r.To = anchor
			r.CompareFrom = period.From
		}
		requests = append(requests, r)
	}
	return requests, nil
}

// BuildMonthComparisons shares the validated population and current column
// across requests from MonthRequests. Each comparison gets its own result
// digest and remains reproducible as a standalone report.
func BuildMonthComparisons(ctx context.Context, in Input, requests []Request) ([]Result, error) {
	if len(requests) == 0 || len(requests) > 12 || (requests[0].Report != BalanceSheet && requests[0].Report != ProfitLoss) {
		return nil, problem("invalid_month_scope")
	}
	template, err := NewResult(in, requests[0])
	if err != nil {
		return nil, err
	}
	if template.Scope.Comparison == nil {
		return nil, problem("invalid_month_scope")
	}
	balances := make(map[Period]map[string]*big.Int)
	values := func(period Period) map[string]*big.Int {
		if sums, ok := balances[period]; ok {
			return sums
		}
		sums := valuesFor(in, period, template.Report)
		balances[period] = sums
		return sums
	}
	var fiscal *fiscalPosition
	if template.Report == BalanceSheet && template.Scope.FiscalStart != "" {
		fiscal = readFiscalPosition(in, template.Scope)
	}
	results := make([]Result, 0, len(requests))
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, period, prior, err := normalizeRequest(in, request)
		if err != nil {
			return nil, err
		}
		// The current period must include every comparison, so the findings
		// built over their union are also shared.
		if req.Report != template.Report || period != template.Scope.Period || prior == nil || prior.From < period.From || prior.To > period.To {
			return nil, problem("invalid_month_scope")
		}
		result := template
		result.Request, result.Scope.Comparison = req, prior
		result.Columns = slices.Clone(template.Columns)
		result.Columns[1].Period = *prior
		result.Checks = slices.Clone(template.Checks)
		result.Findings = slices.Clone(template.Findings)
		for i := range result.Findings {
			result.Findings[i].EntryIDs = slices.Clone(result.Findings[i].EntryIDs)
		}
		if req.Report == BalanceSheet {
			err = buildPosition(in, &result, values, fiscal)
		} else {
			err = buildPerformance(in, &result, values)
		}
		if err != nil {
			return nil, err
		}
		Finalize(&result)
		results = append(results, result)
	}
	return results, nil
}

package reporting

import (
	"math/big"
	"sort"
	"time"
	_ "time/tzdata"
)

type CalendarAccountAmount struct {
	AccountID    string `json:"account_id"`
	Name         string `json:"name"`
	Amount       string `json:"amount_minor"`
	Positive     string `json:"positive_minor"`
	Negative     string `json:"negative_minor"`
	PostingCount int    `json:"posting_count"`
}

type CalendarAmounts struct {
	Total    string                  `json:"total_minor"`
	Positive string                  `json:"positive_minor"`
	Negative string                  `json:"negative_minor"`
	Accounts []CalendarAccountAmount `json:"accounts"`
}

// CalendarBucket boundaries are civil accounting dates in the stated Book
// timezone, not observed instants. Both upper bounds are exclusive. Covered
// bounds identify the selected portion of a partial calendar interval.
type CalendarBucket struct {
	From        string          `json:"from"`
	To          string          `json:"to"`
	CoveredFrom string          `json:"covered_from"`
	CoveredTo   string          `json:"covered_to"`
	Income      CalendarAmounts `json:"income"`
	Expenses    CalendarAmounts `json:"expenses"`
}

type RecognitionPerformance struct {
	Period      DashboardPeriod  `json:"period"`
	Unit        string           `json:"unit"`
	Timezone    string           `json:"timezone"`
	Status      string           `json:"status"`
	Unavailable string           `json:"unavailable_reason,omitempty"`
	Income      string           `json:"income_minor,omitempty"`
	Expenses    string           `json:"expenses_minor,omitempty"`
	Buckets     []CalendarBucket `json:"buckets"`
}

func calendarStart(date time.Time, unit string) (time.Time, error) {
	year, month, day := date.Date()
	switch unit {
	case "day":
	case "week":
		day -= (int(date.Weekday()) + 6) % 7
	case "month":
		day = 1
	case "quarter":
		month, day = time.Month((int(month)-1)/3*3+1), 1
	case "year":
		month, day = time.January, 1
	default:
		return time.Time{}, problem("invalid_calendar_unit")
	}
	// UTC is a civil-date arithmetic carrier. No ledger date is converted
	// through UTC, a machine timezone or an elapsed-hour interval.
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), nil
}

func nextCalendar(date time.Time, unit string) time.Time {
	switch unit {
	case "day":
		return date.AddDate(0, 0, 1)
	case "week":
		return date.AddDate(0, 0, 7)
	case "month":
		return date.AddDate(0, 1, 0)
	case "quarter":
		return date.AddDate(0, 3, 0)
	default:
		return date.AddDate(1, 0, 0)
	}
}

func calendarBounds(period DashboardPeriod, unit string) (time.Time, time.Time, error) {
	first, _ := time.Parse(time.DateOnly, period.From)
	last, _ := time.Parse(time.DateOnly, period.To)
	if !dateValid(period.From) || !dateValid(period.To) || first.After(last) {
		return time.Time{}, time.Time{}, problem("invalid_calendar_period")
	}
	start, err := calendarStart(first, unit)
	return start, last.AddDate(0, 0, 1), err
}

func calendarBuckets(period DashboardPeriod, unit string) ([]CalendarBucket, error) {
	start, until, err := calendarBounds(period, unit)
	if err != nil {
		return nil, err
	}
	out := []CalendarBucket{}
	for start.Before(until) {
		if len(out) == 1000 {
			return nil, problem("calendar_bucket_limit")
		}
		next := nextCalendar(start, unit)
		if start.Year() < 1 || next.Year() > 9999 {
			return nil, problem("calendar_boundary_out_of_range")
		}
		out = append(out, CalendarBucket{
			From: start.Format(time.DateOnly), To: next.Format(time.DateOnly),
			CoveredFrom: max(start.Format(time.DateOnly), period.From),
			CoveredTo:   min(next.Format(time.DateOnly), until.Format(time.DateOnly)),
		})
		start = next
	}
	return out, nil
}

type calendarAccumulator struct {
	name               string
	positive, negative big.Int
	count              int
}

func calendarAmounts(accounts map[string]*calendarAccumulator) CalendarAmounts {
	out := CalendarAmounts{Total: "0", Positive: "0", Negative: "0", Accounts: []CalendarAccountAmount{}}
	positive, negative := new(big.Int), new(big.Int)
	for id, account := range accounts {
		positive.Add(positive, &account.positive)
		negative.Add(negative, &account.negative)
		out.Accounts = append(out.Accounts, CalendarAccountAmount{
			AccountID: id, Name: account.name,
			Amount:   new(big.Int).Add(&account.positive, &account.negative).String(),
			Positive: account.positive.String(), Negative: account.negative.String(), PostingCount: account.count,
		})
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].AccountID < out.Accounts[j].AccountID })
	out.Total = new(big.Int).Add(positive, negative).String()
	out.Positive, out.Negative = positive.String(), negative.String()
	return out
}

func calendarPerformance(rows []recognitionDelta, period DashboardPeriod, unit, zone string) (RecognitionPerformance, error) {
	out := RecognitionPerformance{Period: period, Unit: unit, Timezone: zone, Status: "unavailable", Buckets: []CalendarBucket{}}
	if _, _, err := calendarBounds(period, unit); err != nil {
		return out, err
	}
	if zone == "" {
		out.Unavailable = "book_timezone_not_set"
		return out, nil
	}
	if _, err := time.LoadLocation(zone); err != nil || zone == "Local" {
		return out, problem("invalid_book_timezone")
	}
	buckets, err := calendarBuckets(period, unit)
	if err != nil {
		return out, err
	}
	// Per-bucket account maps retain opposing contributions even when the
	// account's net is zero. No account is truncated or folded into another.
	income := make([]map[string]*calendarAccumulator, len(buckets))
	expenses := make([]map[string]*calendarAccumulator, len(buckets))
	for i := range buckets {
		income[i], expenses[i] = map[string]*calendarAccumulator{}, map[string]*calendarAccumulator{}
	}
	var amount big.Int
	for index := range rows {
		row := &rows[index]
		kind := recognitionKind(row.Kind)
		if kind == "" || row.Date < period.From || row.Date > period.To || !row.Earned {
			continue
		}
		i := sort.Search(len(buckets), func(i int) bool { return row.Date < buckets[i].To })
		if i == len(buckets) || row.Date < buckets[i].CoveredFrom || row.Date >= buckets[i].CoveredTo {
			return out, problem("calendar_population_mismatch")
		}
		n := amount.Set(&row.Amount)
		accounts := expenses[i]
		if kind == "income" {
			accounts = income[i]
			n.Neg(n)
		}
		account := accounts[row.Code]
		if account == nil {
			account = &calendarAccumulator{name: row.Name}
			accounts[row.Code] = account
		}
		account.count++
		if n.Sign() < 0 {
			account.negative.Add(&account.negative, n)
		} else {
			account.positive.Add(&account.positive, n)
		}
	}
	incomeTotal, expenseTotal := new(big.Int), new(big.Int)
	for i := range buckets {
		buckets[i].Income, buckets[i].Expenses = calendarAmounts(income[i]), calendarAmounts(expenses[i])
		incomeTotal.Add(incomeTotal, integer(buckets[i].Income.Total))
		expenseTotal.Add(expenseTotal, integer(buckets[i].Expenses.Total))
	}
	out.Status, out.Buckets = "recorded", buckets
	out.Income, out.Expenses = incomeTotal.String(), expenseTotal.String()
	return out, nil
}

package reporting

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestCalendarBucketsAlignAndExposePartialCoverage(t *testing.T) {
	for _, test := range []struct {
		name, unit, from, to string
		boundaries           [][2]string
	}{
		{"day", "day", "2024-02-28", "2024-03-01", [][2]string{{"2024-02-28", "2024-02-29"}, {"2024-02-29", "2024-03-01"}, {"2024-03-01", "2024-03-02"}}},
		{"Monday across year", "week", "2025-12-31", "2026-01-06", [][2]string{{"2025-12-29", "2026-01-05"}, {"2026-01-05", "2026-01-12"}}},
		{"leap month", "month", "2024-02-20", "2024-03-02", [][2]string{{"2024-02-01", "2024-03-01"}, {"2024-03-01", "2024-04-01"}}},
		{"calendar quarter", "quarter", "2025-12-15", "2026-01-15", [][2]string{{"2025-10-01", "2026-01-01"}, {"2026-01-01", "2026-04-01"}}},
		{"calendar year", "year", "2025-12-31", "2026-01-01", [][2]string{{"2025-01-01", "2026-01-01"}, {"2026-01-01", "2027-01-01"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			buckets, err := calendarBuckets(Period{From: test.from, To: test.to}, test.unit)
			if err != nil || len(buckets) != len(test.boundaries) {
				t.Fatal("calendar boundaries missing", err)
			}
			until, _ := time.Parse(time.DateOnly, test.to)
			for i, bucket := range buckets {
				if [2]string{bucket.From, bucket.To} != test.boundaries[i] || bucket.CoveredFrom != max(test.from, bucket.From) || bucket.CoveredTo != min(until.AddDate(0, 0, 1).Format(time.DateOnly), bucket.To) {
					t.Fatal("calendar boundary or selected coverage changed", bucket)
				}
			}
		})
	}
}

func TestCalendarProjectionPreservesRefundsAndZeroNetGrossPerAccount(t *testing.T) {
	in := Input{Book: Book{ID: "example", Currency: "USD", Scale: 2, AccountingTimezone: "America/New_York"}, Accounts: []Account{{ID: "cash", Kind: "asset"}, {ID: "sale", Kind: "income", Name: "Example income"}, {ID: "cost", Kind: "expense", Name: "Example cost"}}}
	add := func(id, date, debit, credit, amount string) {
		in.Postings = append(in.Postings,
			Posting{ID: id + "d", EntryID: id, BookID: "example", AccountID: debit, Date: date, Side: "debit", Amount: amount, Currency: "USD", Scale: 2},
			Posting{ID: id + "c", EntryID: id, BookID: "example", AccountID: credit, Date: date, Side: "credit", Amount: amount, Currency: "USD", Scale: 2})
	}
	add("sale", "2026-01-31", "cash", "sale", "20")
	add("refund", "2026-01-31", "sale", "cash", "70")
	add("purchase", "2026-01-31", "cost", "cash", "100")
	add("reimbursement", "2026-01-31", "cash", "cost", "100")
	add("later", "2026-02-01", "cash", "sale", "9007199254740993")
	add("excluded", "2026-02-03", "cash", "sale", "11")
	for _, unit := range []string{"day", "week", "month", "quarter", "year"} {
		t.Run(unit, func(t *testing.T) {
			out, err := BuildDashboardProjection(in, "2026-01-31", "2026-02-01", unit)
			if err != nil || out.Contract.Version != 2 || out.Performance.Income != "9007199254740943" || out.Performance.Expenses != "0" || out.Performance.Timezone != "America/New_York" || out.Performance.Status != "recorded" {
				t.Fatal("unit changed exact recorded contributions", err)
			}
			var positive, negative string
			for _, bucket := range out.Performance.Buckets {
				for _, account := range bucket.Expenses.Accounts {
					if account.AccountID != "cost" || account.Amount != "0" || account.PostingCount != 2 {
						t.Fatal("zero-net activity disappeared or changed account")
					}
					positive, negative = account.Positive, account.Negative
				}
			}
			if positive != "100" || negative != "-100" {
				t.Fatal("gross opposing activity became inactivity")
			}
			if unit == "day" || unit == "month" {
				first := out.Performance.Buckets[0].Income
				if first.Total != "-50" || len(first.Accounts) != 1 || first.Accounts[0].Amount != "-50" || first.Positive != "20" || first.Negative != "-70" {
					t.Fatal("refund was dropped or made positive")
				}
			}
		})
	}
}

func TestCalendarProjectionRetainsEveryAccountAndStableOrder(t *testing.T) {
	rows := []recognitionDelta{}
	for _, id := range []string{"six", "five", "four", "three", "two", "one"} {
		rows = append(rows, recognitionDelta{Code: id, Name: "Same display name", Kind: "expense", Date: "2026-01-01", Amount: *integer("1"), Earned: true})
	}
	out, err := calendarPerformance(rows, Period{From: "2026-01-01", To: "2026-01-01"}, "day", "UTC")
	if err != nil || len(out.Buckets[0].Expenses.Accounts) != 6 || out.Expenses != "6" {
		t.Fatal("account contributions were truncated", err)
	}
	reverse := append([]recognitionDelta{}, rows...)
	for i := range reverse {
		reverse[i] = rows[len(rows)-1-i]
	}
	other, err := calendarPerformance(reverse, out.Period, "day", "UTC")
	if err != nil || !reflect.DeepEqual(other, out) {
		t.Fatal("input iteration order changed canonical calendar data", err)
	}
}

func TestCalendarTimezoneUnknownAndMalformedInputsStayExplicit(t *testing.T) {
	period := Period{From: "2026-01-01", To: "2026-01-31"}
	missing, err := calendarPerformance(nil, period, "week", "")
	if err != nil || missing.Status != "unavailable" || missing.Unavailable != "book_timezone_not_set" || len(missing.Buckets) != 0 || missing.Income != "" || missing.Expenses != "" {
		t.Fatal("unknown timezone asserted a calendar or zero", err)
	}
	data, _ := json.Marshal(missing)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if fields["income_minor"] != nil || fields["expenses_minor"] != nil {
		t.Fatal("unavailable calendar totals were encoded as known")
	}
	long := Input{Book: Book{ID: "example", Currency: "USD", Scale: 2}}
	longView, err := BuildDashboardProjection(long, "2000-01-01", "2026-01-31", "day")
	if err != nil || longView.Performance.Status != "unavailable" || longView.Financial.Assets != "0" || len(longView.Performance.Buckets) != 0 {
		t.Fatal("missing timezone imposed an unused calendar cap on financial figures", err)
	}
	for _, test := range []struct{ unit, zone string }{{"fortnight", "UTC"}, {"fiscal_year", "UTC"}, {"day", "Local"}, {"day", "Example/Nowhere"}} {
		if _, err := calendarPerformance(nil, period, test.unit, test.zone); err == nil {
			t.Fatal("malformed calendar input accepted", test)
		}
	}
	for _, bad := range []Period{{From: "2026-02-31", To: "2026-03-01"}, {From: "2026-02-01", To: "2026-01-01"}, {From: "2000-01-01", To: "2026-01-01"}, {From: "9999-12-31", To: "9999-12-31"}} {
		if _, err := calendarBuckets(bad, "day"); err == nil {
			t.Fatal("invalid or oversized calendar became arbitrary slices")
		}
	}
}

func TestCalendarMondayWeeksKeepCivilDatesAcrossDaylightSaving(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		from, to string
		hours    time.Duration
	}{{"2026-03-02", "2026-03-08", 167}, {"2026-10-26", "2026-11-01", 169}} {
		out, err := calendarPerformance([]recognitionDelta{{Code: "sale", Kind: "income", Date: test.to, Amount: *integer("-1"), Earned: true}}, Period{From: test.from, To: test.to}, "week", "America/New_York")
		if err != nil || len(out.Buckets) != 1 || out.Income != "1" {
			t.Fatal("DST lost or split a civil accounting date", err)
		}
		start, _ := time.ParseInLocation(time.DateOnly, out.Buckets[0].From, zone)
		end, _ := time.ParseInLocation(time.DateOnly, out.Buckets[0].To, zone)
		if end.Sub(start) != test.hours*time.Hour || start.Weekday() != time.Monday || end.Weekday() != time.Monday {
			t.Fatal("week became fixed elapsed hours or the wrong weekday")
		}
	}
}

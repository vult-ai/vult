package reporting

import (
	"reflect"
	"testing"
)

func TestMonthPeriodsPreserveInclusiveCalendarBoundaries(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     []Period
	}{
		{"2024-01-15", "2024-03-12", []Period{{"2024-01-15", "2024-01-31"}, {"2024-02-01", "2024-02-29"}, {"2024-03-01", "2024-03-12"}}},
		{"2026-12-31", "2027-01-01", []Period{{"2026-12-31", "2026-12-31"}, {"2027-01-01", "2027-01-01"}}},
	} {
		got, e := MonthPeriods(c.from, c.to)
		if e != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatal("calendar interval changed", got, e)
		}
	}
	for _, c := range [][2]string{{"2026-02-30", "2026-03-01"}, {"2026-02-01", "2026-01-01"}, {"2025-01-31", "2026-01-01"}, {"", "2026-01-01"}} {
		if _, e := MonthPeriods(c[0], c[1]); e == nil {
			t.Fatal("invalid/overlong months accepted", c)
		}
	}
}

func TestMonthRequestsUseCanonicalAmountsAndCommonInputIdentity(t *testing.T) {
	for _, kind := range []Kind{BalanceSheet, ProfitLoss} {
		req := coreRequest(kind)
		req.Comparison = "explicit"
		req.CompareTo = "2025-12-31"
		if kind == ProfitLoss {
			req.CompareFrom = "2025-01-01"
		}
		in := coreFixture()
		base, e := Build(in, req)
		if e != nil {
			t.Fatal(e)
		}
		periods, e := MonthPeriods("2024-01-01", "2024-12-31")
		if e != nil {
			t.Fatal(e)
		}
		requests, e := MonthRequests(base, periods)
		if e != nil || len(requests) != 12 {
			t.Fatal(e)
		}
		batch, e := BuildMonthComparisons(t.Context(), in, requests)
		if e != nil || len(batch) != len(requests) {
			t.Fatal("shared monthly calculation failed", e)
		}
		for i, q := range requests {
			got, e := Build(in, q)
			if e != nil {
				t.Fatal(e)
			}
			if got.Scope.PopulationDigest != base.Scope.PopulationDigest || got.Scope.Comparison.To != periods[i].To || got.Scope.Period.To != "2025-12-31" || q.Comparison != "explicit" {
				t.Fatal("captured input or anchor changed")
			}
			if kind == BalanceSheet && q.CompareFrom != "" || kind == ProfitLoss && q.CompareFrom != periods[i].From {
				t.Fatal("position and flow dates conflated")
			}
			if !CheckResultDigest(got) {
				t.Fatal("month lost canonical result identity")
			}
			if !reflect.DeepEqual(batch[i], got) {
				t.Fatal("monthly calculation changed the standalone report or its digest", kind, i)
			}
		}
		if _, e := MonthRequests(base, []Period{{From: "2024-01-01", To: "2024-12-30"}}); e == nil {
			t.Fatal("different ending date accepted")
		}
	}
}

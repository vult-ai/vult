package reporting

import (
	"encoding/json"
	"math/big"
	"slices"
	"testing"
)

func TestDashboardSnapshotTrendsAgreeWithRecordedPositionsAndStatements(t *testing.T) {
	in := dashboardFixture()
	// Reverse the population to exercise the same result for unordered inputs.
	slices.Reverse(in.Postings)
	from, to := "2025-12-01", "2027-03-31"
	out, err := BuildDashboardSnapshot(in, from, to, "month")
	if err != nil {
		t.Fatal(err)
	}
	if out.Cash == nil || *out.Cash != "11180" || out.CashStatus != "partial" || len(out.CashAccounts) != 2 || out.Financial.Through != to {
		t.Fatal("snapshot omitted the recorded carrying position")
	}
	report, err := BuildCore(in, Request{BookID: in.Book.ID, Report: ProfitLoss, From: from, To: to, Comparison: "previous_year"})
	if err != nil || out.PriorYear == nil || out.PriorYear.Period != *report.Scope.Comparison {
		t.Fatal("snapshot and statement used different comparison dates", err)
	}
	if out.Financial.Result != report.Reconciliation["result"] || out.PriorYear.Result != report.Rows[2].Values["prior"] ||
		out.Performance.Income != out.Financial.Income || out.Performance.Expenses != out.Financial.Expenses {
		t.Fatal("snapshot changed statement or chart totals")
	}
	rows, accountByID := recognitionRows(in, to)
	for _, series := range [][]RecognitionPoint{out.PositionTrend.Current, out.PositionTrend.Previous} {
		for _, point := range series {
			want, err := recognitionPosition(rows, accountByID, Period{To: point.Date})
			if err != nil || point.Assets != want.Assets || point.Liabilities != want.Liabilities || point.Equity != want.Equity {
				t.Fatal("sample changed the position on its date", point, err)
			}
		}
	}
	for _, series := range []struct {
		from   string
		points []DashboardTrendPoint
	}{{from, out.Trends.Current}, {out.PriorYear.Period.From, out.Trends.Previous}} {
		if len(series.points) < 2 {
			t.Fatal("recorded trend was not sampled")
		}
		for _, point := range series.points {
			want, err := recognitionPosition(rows, accountByID, Period{From: series.from, To: point.Date})
			if err != nil || point.Date < "2025-12-31" || point.Income != want.Income || point.Expenses != want.Expenses || point.Result != want.Result ||
				point.Cash == nil || *point.Cash != want.Assets {
				t.Fatal("sample changed the activity or cash on its date", point.Date, err)
			}
		}
	}
	for i := range in.Accounts {
		in.Accounts[i].Role = ""
	}
	out, err = BuildDashboardSnapshot(in, from, to, "month")
	if err != nil || out.Cash != nil || out.CashStatus != "unavailable" || len(out.CashAccounts) != 0 || out.Trends.Current[len(out.Trends.Current)-1].Cash != nil {
		t.Fatal("snapshot invented a cash amount with no cash scope", err)
	}
}

func dashboardFixture() Input {
	in := Input{Book: Book{ID: "example", Currency: "USD", Scale: 2, AccountingTimezone: "Europe/Warsaw"}, Accounts: []Account{
		{ID: "cash", Name: "Services", Kind: "asset", Role: "cash"},
		{ID: "cash2", Name: "Income", Kind: "asset", Role: "cash"},
		{ID: "capital", Name: "Expense", Kind: "equity"},
		{ID: "card", Name: "Revenue", Kind: "liability"},
		{ID: "income", Name: "Transfers", Kind: "income"},
		{ID: "cost", Name: "Opening equity", Kind: "cost_of_goods_sold"},
	}}
	add := func(id, date, debit, credit, amount string) {
		in.Postings = append(in.Postings,
			Posting{ID: id + "d", EntryID: id, BookID: "example", AccountID: debit, Date: date, Side: "debit", Amount: amount, Currency: "USD", Scale: 2},
			Posting{ID: id + "c", EntryID: id, BookID: "example", AccountID: credit, Date: date, Side: "credit", Amount: amount, Currency: "USD", Scale: 2})
	}
	add("prior", "2025-12-31", "cash", "capital", "10000")
	add("contribution", "2026-01-01", "cash", "capital", "1000")
	add("purchase", "2026-01-02", "cost", "card", "250")
	add("payment", "2026-01-03", "card", "cash", "100")
	add("transfer", "2026-01-04", "cash2", "cash", "500")
	add("sale", "2026-01-05", "cash", "income", "200")
	add("refund", "2026-01-06", "cash", "cost", "50")
	add("valuation", "2026-01-07", "cash", "income", "30")
	knownZero, knownOne := "0", "1"
	in.Postings[len(in.Postings)-2].NativeAmount = &knownZero
	add("rounded", "2026-01-08", "cash", "cost", "0")
	in.Postings[len(in.Postings)-2].NativeAmount = &knownOne
	return in
}

func TestDashboardRecordedEffectsConserveReportsAndExcludeOwnTransfers(t *testing.T) {
	in := dashboardFixture()
	out, err := BuildDashboardProjection(in, "2026-01-01", "2026-01-31", "day")
	if err != nil || out.SchemaVersion != 1 || out.Profit != "30" || out.Cash == nil || *out.Cash != "11180" || out.CashFlow == nil || *out.CashFlow != "1150" || *out.CashEffects != "30" || *out.MoneyIn != "1250" || *out.MoneyOut != "100" || *out.MovementCount != 5 || out.Transactions != 8 {
		t.Fatal("recorded cash and recognition effects changed", err)
	}
	if out.Financial.Assets != "11180" || out.Financial.Liabilities != "150" || out.Financial.Equity != "11030" || out.Financial.Income != "230" || out.Financial.Expenses != "200" || out.Contract.EventKind != "accounting_recognition" || out.Contract.Precision != "date" || out.Contract.Coverage != "unknown" {
		t.Fatal("projection inferred classification, source coverage or timing")
	}
	report, err := Build(in, Request{BookID: "example", Report: ProfitLoss, From: "2026-01-01", To: "2026-01-31"})
	if err != nil || report.Reconciliation["income"] != out.Financial.Income || report.Reconciliation["expenses"] != out.Financial.Expenses || report.Reconciliation["result"] != out.Profit {
		t.Fatal("dashboard and reports use different contributions", err)
	}
	profit, flow := zero(), zero()
	for _, point := range out.Series {
		profit.Add(profit, integer(point.Profit))
		flow.Add(flow, integer(*point.CashFlow))
	}
	if profit.String() != out.Profit || flow.String() != *out.CashFlow {
		t.Fatal("series does not conserve its recorded total")
	}
	if out.Performance.Income != "230" || out.Performance.Expenses != "200" {
		t.Fatal("calendar allocation changed an accounting total")
	}
	if len(out.Findings) != 1 || out.Findings[0].Code != "opening_treatment_unconfirmed" || len(out.Findings[0].EntryIDs) != 1 || out.Findings[0].EntryIDs[0] != "contribution" || out.Findings[0].AmountMinor != "1000" {
		t.Fatal("unknown opening treatment disappeared or excluded money")
	}
}

func TestDashboardMissingCashScopeStaysUnavailable(t *testing.T) {
	in := dashboardFixture()
	for i := range in.Accounts {
		in.Accounts[i].Role = ""
	}
	out, err := BuildDashboard(in, "2026-01-01", "2026-01-31")
	if err != nil || out.Profit != "30" || out.Cash != nil || out.CashFlow != nil || out.MoneyIn != nil || out.MoneyOut != nil || out.MovementCount != nil || out.CashStatus != "unavailable" {
		t.Fatal("unavailable cash was converted to zero", err)
	}
	encoded, _ := json.Marshal(out)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	for _, name := range []string{"cash_minor", "cash_flow_minor", "cash_effects_minor", "money_in_minor", "money_out_minor", "movement_count"} {
		if _, exists := fields[name]; exists {
			t.Fatal("missing cash measurement was asserted", name)
		}
	}
}

func TestDashboardOpeningContributionAndUnknownRemainDifferent(t *testing.T) {
	for _, treatment := range []string{"opening", "contribution", "unknown"} {
		t.Run(treatment, func(t *testing.T) {
			in := dashboardFixture()
			in.Postings = in.Postings[2:4]
			for i := range in.Postings {
				in.Postings[i].Treatment = treatment
			}
			out, err := BuildDashboard(in, "2026-01-01", "2026-01-31")
			if err != nil || out.Cash == nil || *out.Cash != "1000" {
				t.Fatal("position changed with reporting treatment", err)
			}
			want, count := "1000", 1
			if treatment == "opening" {
				want, count = "0", 0
			}
			if *out.MoneyIn != want || *out.MovementCount != count || (len(out.Findings) > 0) != (treatment == "unknown") {
				t.Fatal("opening, contribution or unspecified treatment was conflated")
			}
		})
	}
}

func TestDashboardAmbiguousCashExchangeDoesNotInventMoneyIn(t *testing.T) {
	in := dashboardFixture()
	in.Postings = []Posting{
		{ID: "in", EntryID: "exchange", BookID: "example", AccountID: "cash", Date: "2026-01-01", Side: "debit", Amount: "120", Currency: "USD", Scale: 2},
		{ID: "out", EntryID: "exchange", BookID: "example", AccountID: "cash2", Date: "2026-01-01", Side: "credit", Amount: "110", Currency: "USD", Scale: 2},
		{ID: "gain", EntryID: "exchange", BookID: "example", AccountID: "income", Date: "2026-01-01", Side: "credit", Amount: "10", Currency: "USD", Scale: 2},
	}
	out, err := BuildDashboard(in, "2026-01-01", "2026-01-31")
	if err != nil || out.Cash == nil || *out.Cash != "10" || out.Profit != "10" || out.CashFlow != nil || out.MoneyIn != nil || out.CashFlowState != "unavailable" || len(out.Findings) != 1 || out.Findings[0].Code != "cash_transfer_treatment_unconfirmed" {
		t.Fatal("transfer valuation became invented income of cash", err)
	}
}

func TestDashboardNetZeroMixedCashIsNotAssumedToBeAnInternalTransfer(t *testing.T) {
	in := dashboardFixture()
	in.Postings = []Posting{
		{ID: "cash-in", EntryID: "mixed", BookID: "example", AccountID: "cash", Date: "2026-01-01", Side: "debit", Amount: "100", Currency: "USD", Scale: 2},
		{ID: "cash-out", EntryID: "mixed", BookID: "example", AccountID: "cash2", Date: "2026-01-01", Side: "credit", Amount: "100", Currency: "USD", Scale: 2},
		{ID: "cost", EntryID: "mixed", BookID: "example", AccountID: "cost", Date: "2026-01-01", Side: "debit", Amount: "100", Currency: "USD", Scale: 2},
		{ID: "income", EntryID: "mixed", BookID: "example", AccountID: "income", Date: "2026-01-01", Side: "credit", Amount: "100", Currency: "USD", Scale: 2},
	}
	out, err := BuildDashboard(in, "2026-01-01", "2026-01-31")
	if err != nil || out.Cash == nil || *out.Cash != "0" || out.Profit != "0" || out.CashFlow != nil || out.MoneyIn != nil || out.MoneyOut != nil || out.MovementCount != nil || len(out.Findings) != 1 || out.Findings[0].AmountMinor != "200" {
		t.Fatal("zero-net mixed postings invented zero gross cash movement", err)
	}
	r, err := Build(in, Request{BookID: "example", Report: CashMovement, From: "2026-01-01", To: "2026-01-31"})
	if err != nil || r.Status != "partial" || len(r.Rows) != 1 || r.Rows[0].ID != "cash:closing" {
		t.Fatal("cash report invented gross totals from an ambiguous zero net", err)
	}
}

func TestDashboardChartPlansKeepWholeDatesAndExactContributions(t *testing.T) {
	for _, period := range []Period{{From: "2026-10-01", To: "2026-12-31"}, {From: "2024-02-01", To: "2024-02-29"}, {From: "0001-01-01", To: "0001-01-31"}} {
		in := Input{Book: Book{ID: "empty", Currency: "JPY", Scale: 0, AccountingTimezone: "Asia/Tokyo"}}
		out, err := BuildDashboardProjection(in, period.From, period.To, "month")
		if err != nil || out.Financial.Assets != "0" || len(out.Comparison.Current) != 0 {
			t.Fatal("empty journal invented observed positions", err)
		}
		if period.From == "0001-01-01" && (out.Comparison.PreviousRange != nil || out.Comparison.PreviousState != "unavailable") {
			t.Fatal("comparison invented a year zero")
		}
		if out.Performance.Status != "recorded" || len(out.Performance.Buckets) == 0 {
			t.Fatal("calendar plan missing for established timezone")
		}
		for i, bucket := range out.Performance.Buckets {
			if !dateValid(bucket.From) || !dateValid(bucket.To) || bucket.From >= bucket.To || i > 0 && bucket.From != out.Performance.Buckets[i-1].To {
				t.Fatal("calendar chart introduced a gap or invalid date")
			}
		}
	}
	in := dashboardFixture()
	large := "9007199254740993"
	in.Postings = in.Postings[2:4]
	for i := range in.Postings {
		in.Postings[i].Amount = large
	}
	out, err := BuildDashboardProjection(in, "2026-01-01", "2026-01-31", "day")
	if err != nil || *out.Cash != large || *out.MoneyIn != large {
		t.Fatal("dashboard rounded an exact integer", err)
	}
	if n, _ := new(big.Int).SetString(out.Financial.Assets, 10); n == nil || n.String() != large {
		t.Fatal("financial position lost precision")
	}
}

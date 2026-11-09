package ledger_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerActivityFiltersAndTotalsMatchRecordedReports(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	income, expense := db.Account(t, scope, "4000", "income"), db.Account(t, scope, "5000", "expense")
	add := func(key, date, debit, credit, amount, description string) ledger.Entry {
		t.Helper()
		q := post(debit, credit, key)
		q.AccountingDate, q.EconomicDate, q.Description = date, date, description
		if key == "sale" {
			q.EconomicDate = "2025-12-31"
		}
		q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = amount, amount
		var entry ledger.Entry
		if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
			t.Fatal("invalid activity fixture")
		}
		return entry
	}
	add("funding", "2026-01-01", cash.ID, equity.ID, "10000", "Example funding")
	add("sale", "2026-01-02", cash.ID, income.ID, "9007199254740993", "Example recorded income")
	cost := add("cost", "2026-02-01", expense.ID, cash.ID, "125", "Service 100% complete")
	add("opening-cost", "2026-02-02", expense.ID, equity.ID, "50", "Opening balance equity")
	db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: cost.ID, AccountingDate: "2026-03-01", Reason: "Example correction"})
	foreign := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(foreign, expense.ID, "rounded", "1", "0")
	q.AccountingDate, q.EconomicDate = "2026-04-01", "2026-04-01"
	quoteOn(&q, "0.001")
	q.Lines[0].Valuation.Rate.RequestedDate, q.Lines[0].Valuation.Rate.ObservationDate = q.EconomicDate, q.EconomicDate
	db.Call(t, scope, "post_journal", q)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	filters := ledger.ActivityFilters{From: "2026-01-01", To: "2026-12-31"}
	summary, err := owner.ActivitySummary(db.Context, book.ID, filters, "")
	if err != nil || summary.Count != 6 || summary.Income != "9007199254740993" || summary.Expenses != "50" || summary.Net != "9007199254740943" || summary.FirstDate != "2026-01-01" || summary.LastDate != "2026-04-01" || len(summary.Series) != 12 || summary.Series[1].Expenses != "175" || summary.Series[2].Expenses != "-125" || summary.Series[3].Count != 1 || summary.CounterpartyStatus != "not_recorded" || len(summary.Counterparties) != 0 {
		t.Fatal("activity summary changed exact effects, dates or missing counterparties", err)
	}
	report, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: filters.From, To: filters.To}, false)
	if err != nil || report.Reconciliation["income"] != summary.Income || report.Reconciliation["expenses"] != summary.Expenses || report.Reconciliation["result"] != summary.Net {
		t.Fatal("Activity and reports use different contribution rules", err)
	}
	for _, category := range summary.Categories {
		if category.Key != income.ID && category.Key != expense.ID {
			t.Fatal("category identity is an editable code or inferred label")
		}
	}
	seen := map[string]bool{}
	for offset := 0; offset < summary.Count; offset += 2 {
		page, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{ActivityFilters: filters, Limit: 2, Offset: offset, Revision: summary.Revision})
		if err != nil || page.Revision != summary.Revision || page.Coverage.Count != 6 || page.Coverage.NextOffset != offset+2 || len(page.Transactions) != 2 || page.Summary.Income != summary.Income || page.Summary.Expenses != summary.Expenses || page.Summary.Net != summary.Net {
			t.Fatal("list page and summary select different populations", err)
		}
		for _, entry := range page.Transactions {
			if seen[entry.ID] || entry.AmountRole != "book_debit_total" || entry.MeasurementStatus != "entry_lines" || entry.Currency != "USD" || entry.Scale != 2 || entry.Date != entry.AccountingDate {
				t.Fatal("paged entry identity or measurement meaning changed")
			}
			seen[entry.ID] = true
		}
	}
	if len(seen) != 6 {
		t.Fatal("list pagination dropped a zero-Book entry or duplicated a row")
	}
	for _, fixture := range []struct {
		filters ledger.ActivityFilters
		count   int
		income  string
		expense string
	}{
		// Book search: a query of only punctuation filters nothing (Book search), and "%" is
		// never a pattern wildcard.
		{ledger.ActivityFilters{Search: "%"}, 6, "9007199254740993", "50"},
		{ledger.ActivityFilters{Search: "Opening balance equity"}, 1, "0", "50"},
		{ledger.ActivityFilters{Kind: "expense"}, 4, "0", "50"},
		{ledger.ActivityFilters{Kind: "reversal"}, 1, "0", "-125"},
		{ledger.ActivityFilters{Kind: "journal"}, 5, "9007199254740993", "175"},
		{ledger.ActivityFilters{Account: cash.ID}, 4, "9007199254740993", "0"},
		{ledger.ActivityFilters{From: "2025-12-01", To: "2025-12-31"}, 0, "0", "0"},
	} {
		sum, err := owner.ActivitySummary(db.Context, book.ID, fixture.filters, "")
		if err != nil || sum.Count != fixture.count || sum.Income != fixture.income || sum.Expenses != fixture.expense {
			t.Fatal("filter changed authoritative class/date/search rules", fixture.filters, err)
		}
		page, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{ActivityFilters: fixture.filters, Limit: 100, Revision: sum.Revision})
		if err != nil || len(page.Transactions) != sum.Count || page.Summary.Income != sum.Income || page.Summary.Expenses != sum.Expenses {
			t.Fatal("filtered table and summary differ", err)
		}
	}
}

func TestOwnerActivityRejectsStaleMalformedAndForeignScopes(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	q := ledger.ActivityFilters{}
	empty, err := owner.ActivitySummary(db.Context, book.ID, q, "")
	if err != nil || empty.Count != 0 || empty.FirstDate != "" || empty.LastDate != "" || empty.Series == nil || empty.Categories == nil || empty.Counterparties == nil {
		t.Fatal("empty Activity is not a valid explicit empty result", err)
	}
	db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "new-entry"))
	if got, err := owner.ActivitySummary(db.Context, book.ID, q, empty.Revision); !errors.Is(err, ledger.ErrConflict) || got.BookID != "" {
		t.Fatal("stale summary silently used a different ledger", err)
	}
	if got, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{Limit: 25, Revision: empty.Revision}); !errors.Is(err, ledger.ErrConflict) || got.Book.ID != "" {
		t.Fatal("stale list silently used a different ledger", err)
	}
	_, other, otherScope := tenantOwner(t, db)
	foreign := db.Account(t, otherScope, "1000", "asset")
	if _, err := owner.ActivitySummary(db.Context, other.ID, q, ""); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("summary crossed tenant scope", err)
	}
	if _, err := owner.ActivitySummary(db.Context, book.ID, ledger.ActivityFilters{Account: foreign.ID}, ""); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("foreign account was accepted as a selector", err)
	}
	for _, filters := range []ledger.ActivityFilters{{From: "2026-02-31"}, {From: "2026-02-02", To: "2026-02-01"}, {Account: "1000"}, {Kind: "guessed-purpose"}, {Search: strings.Repeat("x", 513)}, {Search: "x\x00y"}} {
		if got, err := owner.ActivitySummary(db.Context, book.ID, filters, ""); !errors.Is(err, ledger.ErrInvalid) || got.BookID != "" {
			t.Fatal("malformed filters returned financial data", err)
		}
	}
	for _, revision := range []string{"short", strings.Repeat("z", 64)} {
		if got, err := owner.ActivitySummary(db.Context, book.ID, q, revision); !errors.Is(err, ledger.ErrInvalid) || got.BookID != "" {
			t.Fatal("malformed revision was accepted", err)
		}
	}
}

// A Book kept from card statements has no bank account; its charges are still
// expenses and its cash back still income, and each tab counts them.
func TestActivityTabsCountCardOnlyIncomeAndExpenses(t *testing.T) {
	db := testutil.New(t)
	book, s := db.Book(t, "Example card Book")
	card := db.Account(t, s, "2100", "liability")
	food := db.Account(t, s, "6100", "expense")
	reward := db.Account(t, s, "4100", "income")
	opening := db.Account(t, s, "3000", "equity")
	line := func(account ledger.Account, side, minor string) ledger.PostLine {
		return ledger.PostLine{AccountID: account.ID, Side: side, AmountMinor: minor}
	}
	for i, p := range []struct {
		date  string
		lines []ledger.PostLine
	}{
		{"2026-01-01", []ledger.PostLine{line(opening, "debit", "1000"), line(card, "credit", "1000")}},
		{"2026-01-02", []ledger.PostLine{line(food, "debit", "500"), line(card, "credit", "500")}},
		{"2026-01-03", []ledger.PostLine{line(food, "debit", "700"), line(card, "credit", "700")}},
		{"2026-01-04", []ledger.PostLine{line(card, "debit", "40"), line(reward, "credit", "40")}},
	} {
		db.Call(t, s, "post_journal", ledger.PostJournal{RequestKey: fmt.Sprintf("card-%d", i), EconomicDate: p.date, AccountingDate: p.date, Description: "Example card entry", Lines: p.lines})
	}
	// A charge in zloty billed in dollars keeps its original amount.
	two := 2
	charged := ledger.CurrencyAmount{Currency: "PLN", Scale: &two, AmountMinor: "21430"}
	foreign := line(food, "debit", "5382")
	foreign.Contexts = []ledger.ContextMeasurement{{Role: "transaction_amount", Amount: charged}}
	db.Call(t, s, "post_journal", ledger.PostJournal{RequestKey: "card-foreign", EconomicDate: "2026-01-05", AccountingDate: "2026-01-05", Description: "Example grocer", Lines: []ledger.PostLine{foreign, line(card, "credit", "5382")}})
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	originals := 0
	for _, e := range all.Transactions {
		if e.OriginalAmount != nil {
			originals++
			if e.OriginalAmount.Currency != "PLN" || e.OriginalAmount.AmountMinor != "21430" || *e.OriginalAmount.Scale != 2 {
				t.Fatalf("original amount %+v", *e.OriginalAmount)
			}
		}
	}
	if originals != 1 {
		t.Fatalf("%d entries carried an original amount, want 1", originals)
	}
	want := map[string]int{"": 5, "income": 1, "expense": 3}
	for _, kind := range []string{"", "income", "expense"} {
		page, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Kind: kind}, Limit: 25})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Transactions) != want[kind] {
			t.Fatalf("tab %q shows %d entries, want %d", kind, len(page.Transactions), want[kind])
		}
		for k, n := range want {
			if page.Counts[k] != n {
				t.Fatalf("on tab %q, count %q is %d, want %d", kind, k, page.Counts[k], n)
			}
		}
	}
}

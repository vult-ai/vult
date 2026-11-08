package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type searchBook struct {
	db      *testutil.Database
	book    ledger.Book
	scope   ledger.Scope
	owner   ledger.TenantAdmin
	account map[string]ledger.Account
	entry   map[string]string // label → entry ID
	label   map[string]string // entry ID → label
}

const searchToday = "2026-11-06"

// A synthetic Book whose entries each exist to prove one search rule.
func newSearchBook(t testing.TB) *searchBook {
	t.Helper()
	db := testutil.New(t)
	book, s := db.Book(t, "Synthetic search Book")
	owner, e := db.Owner.ForTenant(db.Context, book.TenantID)
	if e != nil {
		t.Fatal(e)
	}
	b := &searchBook{db: db, book: book, scope: s, owner: owner, account: map[string]ledger.Account{}, entry: map[string]string{}, label: map[string]string{}}
	for _, a := range []struct{ code, class, name string }{
		{"1000", "asset", "Example Bank #1234 (checking)"},
		{"1010", "asset", "Example Bank #5678 (savings)"},
		{"2000", "liability", "Card 0140"},
		{"3000", "equity", "Owner capital"},
		{"4000", "income", "Sales"},
		{"6000", "expense", "Office Expense"},
		{"6010", "expense", "Contract Labor"},
		{"6100", "expense", "Payroll:Salaries & Wages"},
		{"6110", "expense", "Payroll taxes"},
		{"6200", "expense", "Advertising/Marketing"},
		{"6300", "expense", "Meals"},
		{"6400", "expense", "Rent"},
		{"6500", "expense", "Internet"},
		{"6600", "expense", "Amazon"},
	} {
		out := db.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: "account-" + a.code, Class: a.class, Code: a.code, Name: a.name})
		var acct ledger.Account
		if json.Unmarshal(out, &acct) != nil {
			t.Fatal("account")
		}
		b.account[a.code] = acct
	}
	return b
}

type line struct{ code, side, amount, memo string }

func (b *searchBook) post(t testing.TB, label, date, description string, lines ...line) {
	t.Helper()
	q := ledger.PostJournal{RequestKey: "entry-" + label, EconomicDate: date, AccountingDate: date, Description: description}
	for _, l := range lines {
		q.Lines = append(q.Lines, ledger.PostLine{AccountID: b.account[l.code].ID, Side: l.side, AmountMinor: l.amount, Memo: l.memo})
	}
	var v ledger.Entry
	if json.Unmarshal(b.db.Call(t, b.scope, "post_journal", q), &v) != nil {
		t.Fatal("post")
	}
	b.entry[label], b.label[v.ID] = v.ID, label
}

func expense(code, amount, memo string) []line {
	return []line{{code, "debit", amount, memo}, {"2000", "credit", amount, ""}}
}

func (b *searchBook) populate(t testing.TB) {
	b.post(t, "amazon-oct24", "2026-10-24", "Amazon", expense("6000", "1012", "AMZN Mktp US*RA7WX1XY1")...)
	b.post(t, "amazon-oct23", "2026-10-23", "Amazon", expense("6000", "8924", "")...)
	b.post(t, "central-office", "2017-03-13", "Central Office cleaning", expense("6010", "19100", "")...)
	b.post(t, "wise-transfer", "2026-10-23", "WISE US INC WISE transfer", line{"1010", "debit", "150255", ""}, line{"1000", "credit", "150255", ""})
	b.post(t, "payroll", "2026-10-09", "ADP wage pay", line{"6100", "debit", "100000", ""}, line{"6110", "debit", "7650", ""}, line{"1000", "credit", "107650", ""})
	b.post(t, "rent", "2026-01-05", "Monthly rent", expense("6400", "100000", "")...)
	b.post(t, "cafe", "2023-10-20", "Café Réveille", expense("6300", "3210", "")...)
	b.post(t, "godaddy", "2026-10-24", "GODADDY.COM", expense("6500", "869", "")...)
	b.post(t, "parking", "2026-02-02", "Parking", expense("6300", "800", "")...)
	b.post(t, "digital-marketing", "2026-03-01", "Digital Marketing ads", expense("6200", "25000", "")...)
	b.post(t, "bitly", "2026-03-15", "BITLY.COM", expense("6200", "3722", "")...)
	b.post(t, "spectrum", "2026-03-10", "Spectrum internet", expense("6500", "19500", "")...)
	b.post(t, "check-2019", "2019-10-24", "Check 1042", expense("6000", "24700", "")...)
	b.post(t, "uber-may", "2026-05-30", "Uber Eats", expense("6300", "7334", "")...)
	b.post(t, "hank-may", "2024-02-01", "Example May Services", expense("6010", "50000", "")...)
	b.post(t, "sale", "2026-09-30", "Stripe payout", line{"1000", "debit", "88009", ""}, line{"4000", "credit", "88009", ""})
	b.post(t, "funding", "2026-01-02", "Owner funding", line{"1000", "debit", "500000", ""}, line{"3000", "credit", "500000", ""})
}

func (b *searchBook) search(t testing.TB, q string) ledger.SearchResult {
	t.Helper()
	out, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: q, Today: searchToday, Limit: 20})
	if e != nil {
		t.Fatalf("search %q: %v", q, e)
	}

	return out
}

func (b *searchBook) labels(r ledger.SearchResult) []string {
	out := []string{}
	for _, v := range r.Transactions.Items {
		out = append(out, b.label[v.ID])
	}
	return out
}

func TestSearchGoldenCases(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	cases := []struct {
		query string
		want  []string // exact order
	}{
		{"godaddy", []string{"godaddy"}},
		// Account names rank first; then other text. Newest first within a rank.
		{"office expense", []string{"amazon-oct24", "amazon-oct23", "check-2019"}},
		{`"office expense"`, []string{"amazon-oct24", "amazon-oct23", "check-2019"}},
		{"office-expense", []string{"amazon-oct24", "amazon-oct23", "check-2019"}},
		{"office", []string{"amazon-oct24", "amazon-oct23", "check-2019", "central-office"}},
		{"$1,502.55", []string{"wise-transfer"}},
		{"1502.55", []string{"wise-transfer"}},
		{"-1502.55", []string{"wise-transfer"}},
		{"wise transfer", []string{"wise-transfer"}},
		{"transfer wise", []string{"wise-transfer"}},
		{`"wise transfer"`, []string{"wise-transfer"}},
		{`"transfer wise"`, []string{}},
		{"Oct 24", []string{"godaddy", "amazon-oct24"}}, // same date: last posted first
		{"Oct 24 2019", []string{"check-2019"}},
		{"amazn", []string{}},
		{"offce", []string{"amazon-oct24", "amazon-oct23", "check-2019"}},
		{"amazonamazon", []string{}},
		{"8", []string{"parking"}},
		{"cafe", []string{"cafe"}},
		{"CAFÉ", []string{"cafe"}},
		{"payroll salaries", []string{"payroll"}},
		{"salaries payroll", []string{"payroll"}},
		{"Payroll:Salaries", []string{"payroll"}},
		{"0", []string{}},
		{"a", []string{}},
		{"marketing", []string{"bitly", "digital-marketing"}},
		{"may", []string{"uber-may", "hank-may"}},
		{"feb 30", []string{}},
		{"*", []string{}},
		{`"`, []string{}},
		// An entry of $1,000 ranks above an entry with a $1,000 line.
		{"1,000", []string{"rent", "payroll"}},
		{"amzn", []string{"amazon-oct24"}},
		{"stripe 880.09", []string{"sale"}},
	}
	for _, c := range cases {
		got := b.labels(b.search(t, c.query))
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: got %v, want %v", c.query, got, c.want)
		}
	}
	// A recorded account name can match a typo. A merchant-looking description
	// does not become a counterparty or a name index.
	if r := b.search(t, "amazn"); r.Accounts.Total != 1 || r.Accounts.Items[0].Name != "Amazon" || r.Transactions.Total != 0 {
		t.Fatal("typo matching inferred a name from a transaction description")
	}
	octoberMonth := b.labels(b.search(t, "october"))
	if strings.Join(octoberMonth, ",") != "godaddy,amazon-oct24,wise-transfer,amazon-oct23,payroll" {
		t.Errorf("october is the most recent October: %v", octoberMonth)
	}
	year := b.search(t, "2026")
	if year.Transactions.Total != 13 {
		t.Errorf("2026 matched %d entries", year.Transactions.Total)
	}
	card := b.labels(b.search(t, "#1234"))
	if strings.Join(card, ",") != "wise-transfer,payroll,sale,funding" {
		t.Errorf("#1234 finds entries on Example Bank #1234: %v", card)
	}
}

func TestSearchGroupsAndAmounts(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	r := b.search(t, "office")
	if r.Accounts.Total != 1 || r.Accounts.Items[0].Name != "Office Expense" || r.Accounts.Items[0].Code != "6000" {
		t.Fatalf("accounts group: %+v", r.Accounts)
	}
	if r.Counterparties.Items == nil || r.Sources.Items == nil || r.Counterparties.Total != 0 || r.Counterparties.Status != "not_recorded" {
		t.Fatal("every group is present; counterparties are not recorded")
	}
	if r.StateRevision == "" || r.StateKnowledgeCutoff == "" {
		t.Fatal("results describe a recorded state")
	}
	// Amounts are the unsigned Book debit total, as in Activity; no flow is inferred.
	first := r.Transactions.Items[0]
	if first.AmountMinor != "1012" || first.AmountRole != "book_debit_total" || first.Kind != "journal" || first.Date != "2026-10-24" ||
		first.Description != "Amazon" || strings.Join(first.Accounts, ",") != "Office Expense,Card 0140" {
		t.Fatalf("entry item: %+v", first)
	}
	if v := b.search(t, "stripe").Transactions.Items[0]; v.AmountMinor != "88009" || v.AmountRole != "book_debit_total" {
		t.Fatalf("income entry: %+v", v)
	}
	if acc := b.search(t, "salaries").Accounts; acc.Total != 1 || acc.Items[0].Name != "Payroll:Salaries & Wages" {
		t.Fatalf("account by a word inside a colon name: %+v", acc)
	}
	if acc := b.search(t, "markting").Accounts; acc.Total != 1 || acc.Items[0].Code != "6200" {
		t.Fatalf("account name typo: %+v", acc)
	}
	if acc := b.search(t, "Oct 24").Accounts; acc.Total != 0 {
		t.Fatal("a date names no account")
	}
	limited, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "office", Today: searchToday})
	if e != nil || len(limited.Transactions.Items) != 4 || limited.Transactions.Total != 4 {
		t.Fatal("default limit is 5 per group", e)
	}
	two, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "office", Today: searchToday, Limit: 2})
	if e != nil || len(two.Transactions.Items) != 2 || two.Transactions.Total != 4 {
		t.Fatal("limit narrows items, not the total", e)
	}
	only, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: "office", Today: searchToday, Types: []string{"accounts"}})
	if e != nil || only.Transactions.Total != 0 || only.Accounts.Total != 1 {
		t.Fatal("types selects groups", e)
	}
}

func TestSearchBounds(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	for _, q := range []string{strings.Repeat("x", 201), strings.Repeat("x", 65), "amazon " + strings.Repeat("x", 65), "one two three four five six seven eight nine"} {
		if _, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: q}); !errors.Is(e, ledger.ErrQueryTooLong) {
			t.Fatalf("%q: %v", q, e)
		}
	}
	if _, e := b.owner.SearchBook(b.db.Context, b.book.ID, ledger.SearchQuery{Query: `"one two three four five six seven eight nine"`}); e != nil {
		t.Fatal("a phrase is one term", e)
	}
	for _, q := range []ledger.SearchQuery{{Query: "x", Limit: 21}, {Query: "x", Limit: -1}, {Query: "x", Types: []string{"people"}}, {Query: "x", Today: "Nov 6"}} {
		if _, e := b.owner.SearchBook(b.db.Context, b.book.ID, q); !errors.Is(e, ledger.ErrInvalid) {
			t.Fatalf("%+v accepted", q)
		}
	}
	empty := b.search(t, "   ")
	if empty.Transactions.Items == nil || empty.Transactions.Total != 0 {
		t.Fatal("an empty query returns empty groups")
	}
}

func TestSearchFollowsRenamesReversalsAndBookScope(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	office := b.account["6000"]
	b.db.Call(t, b.scope, "revise_account", ledger.ReviseAccount{RequestKey: "rename-office", AccountID: office.ID, ExpectedRevision: office.Revision, Code: "6000", Name: "Office Supplies"})
	if got := b.labels(b.search(t, "supplies")); strings.Join(got, ",") != "amazon-oct24,amazon-oct23,check-2019" {
		t.Fatalf("a renamed account matches by its new name: %v", got)
	}
	if got := b.labels(b.search(t, "office expense")); len(got) != 0 {
		t.Fatalf("the old account name no longer matches: %v", got)
	}
	var reversal ledger.Entry
	json.Unmarshal(b.db.Call(t, b.scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: b.entry["amazon-oct23"], AccountingDate: "2026-10-25", Reason: "Duplicate charge"}), &reversal)
	b.label[reversal.ID] = "reversal"
	if got := b.labels(b.search(t, "89.24")); strings.Join(got, ",") != "reversal,amazon-oct23" {
		t.Fatalf("a reversal is found by amount: %v", got)
	}
	if got := b.labels(b.search(t, "duplicate")); strings.Join(got, ",") != "reversal" {
		t.Fatalf("a reversal is found by its reason: %v", got)
	}
	if v := b.search(t, "duplicate").Transactions.Items[0]; v.Kind != "reversal" || v.ReversalOf != b.entry["amazon-oct23"] {
		t.Fatalf("reversal item: %+v", v)
	}

	// Another Book in the same tenant never leaks into this Book's results.
	other, s := b.db.Book(t, "Other synthetic Book")
	cash := b.db.Account(t, s, "1000", "asset")
	equity := b.db.Account(t, s, "3000", "equity")
	b.db.Call(t, s, "post_journal", ledger.PostJournal{RequestKey: "other", EconomicDate: "2026-10-24", AccountingDate: "2026-10-24", Description: "Amazon elsewhere",
		Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "1012"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "1012"}}})
	if got := b.labels(b.search(t, "amazon")); strings.Join(got, ",") != "amazon-oct24,amazon-oct23" {
		t.Fatalf("this Book only: %v", got)
	}
	_ = other
}

// Agents search through their connection, as the restricted runtime role:
// the index's grants and row policies, not the owner's bypass.
func TestSearchThroughAConnection(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	reader, e := b.owner.CreateConnection(b.db.Context, b.book.ID, "synthetic reader", "read")
	if e != nil {
		t.Fatal(e)
	}
	s := ledger.Scope{TenantID: b.book.TenantID, BookID: b.book.ID, ActorID: reader.ActorID, ConnectionID: reader.ID, Access: "read"}
	out, e := b.db.Store.Call(b.db.Context, s, "search_book", value(map[string]any{"query": "office expense", "limit": 2}))
	if e != nil {
		t.Fatal(e)
	}
	var r ledger.SearchResult
	if json.Unmarshal(out, &r) != nil || r.Transactions.Total != 3 || len(r.Transactions.Items) != 2 || r.Accounts.Total != 1 {
		t.Fatalf("connection search: %s", out)
	}
	for _, bad := range []map[string]any{{"query": strings.Repeat("x", 201)}, {"query": "x", "limit": 50}, {"query": "x", "today": "2020-01-01"}} {
		if _, e := b.db.Store.Call(b.db.Context, s, "search_book", value(bad)); e == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if e = b.owner.Revoke(b.db.Context, reader.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = b.db.Store.Call(b.db.Context, s, "search_book", value(map[string]any{"query": "office"})); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("a revoked connection searched", e)
	}
}

func TestBookTodayUsesTheBooksTimezone(t *testing.T) {
	at := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC) // Nov 6 in New York
	if got := ledger.BookToday(ledger.Book{AccountingTimezone: "America/New_York"}, at); got != "2026-11-06" {
		t.Fatal(got)
	}
	if got := ledger.BookToday(ledger.Book{}, at); got != "2026-11-07" {
		t.Fatal(got)
	}
}

// Activity's list and summary filter with the same rules, so their counts are
// the finder's totals for every query.
func TestActivitySearchSharesTheFindersPopulation(t *testing.T) {
	b := newSearchBook(t)
	b.populate(t)
	ctx := b.db.Context
	for _, q := range []string{"payroll salaries", "office-expense", `"office expense"`, "amazn", "$1,502.55", `"wise transfer"`, "2026", "cafe", "zzqq", "Oct 24", "october", "may", "0", "1,000", "transfer wise", "amzn", "stripe 880.09"} {
		found, e := b.owner.SearchBook(ctx, b.book.ID, ledger.SearchQuery{Query: q})
		if e != nil {
			t.Fatal(q, e)
		}
		page, e := b.owner.Activity(ctx, b.book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Search: q}, Limit: 25})
		if e != nil {
			t.Fatal(q, e)
		}
		summary, e := b.owner.ActivitySummary(ctx, b.book.ID, ledger.ActivityFilters{Search: q}, page.Revision)
		if e != nil {
			t.Fatal(q, e)
		}
		if page.Coverage.Count != found.Transactions.Total || summary.Count != found.Transactions.Total {
			t.Errorf("%q: list %d, summary %d, finder %d", q, page.Coverage.Count, summary.Count, found.Transactions.Total)
		}
	}
	// Punctuation alone filters nothing; an over-long search is a bad request.
	all, _ := b.owner.Activity(ctx, b.book.ID, ledger.ActivityPageQuery{Limit: 25})
	if page, e := b.owner.Activity(ctx, b.book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Search: "*"}, Limit: 25}); e != nil || page.Coverage.Count != all.Coverage.Count {
		t.Fatal("punctuation filtered Activity", e)
	}
	if _, e := b.owner.Activity(ctx, b.book.ID, ledger.ActivityPageQuery{ActivityFilters: ledger.ActivityFilters{Search: strings.Repeat("x", 201)}, Limit: 25}); !errors.Is(e, ledger.ErrInvalid) {
		t.Fatal("an over-long search was not a bad request", e)
	}
}

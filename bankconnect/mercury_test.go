package bankconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var _ TokenConnector = (*Mercury)(nil)

// fakeTx is one synthetic Mercury transaction, its amount in cents signed
// the way Mercury signs it: negative is money out.
type fakeTx struct {
	id, status, kind, created, posted, name string
	cents                                   int64
}

// fakeMercury answers the Mercury endpoints Vult reads with synthetic data.
// Each balance is worked out from the account's own transactions, as a bank
// would, so tests can make the numbers disagree on purpose. It serves at
// most two transactions a page so paging is exercised.
type fakeMercury struct {
	mu       sync.Mutex
	token    string
	accounts []map[string]any
	credit   []map[string]any
	txs      map[string][]*fakeTx // account ID -> transactions
	status   map[string]int       // path -> status to answer with
	queries  map[string][]string  // path -> query strings seen
	auth     []string
	// balanceCountsPending makes balances include pending transactions.
	balanceCountsPending bool
	// hidden transactions count in the balance but are never listed.
	hidden map[string]bool
	// flipListed lists amounts with the opposite sign for these accounts;
	// flipBalance reports these accounts' balances with the opposite sign.
	flipListed, flipBalance map[string]bool
	// carried is balance an account holds with no transaction behind it, as
	// when a bank carries a balance across to a new account.
	carried map[string]int64
	// moveAfter changes a balance after this many reads of /accounts.
	moveAfter, accountReads int
}

func newFakeMercury() *fakeMercury {
	return &fakeMercury{
		token: "secret-token:mercury_example_read_only",
		accounts: []map[string]any{
			{"id": "acct-checking", "accountNumber": "000012340001", "name": "Example Checking", "status": "active", "type": "mercury", "kind": "checking", "createdAt": "2023-03-01T00:00:00Z"},
			{"id": "acct-savings", "accountNumber": "000012340002", "name": "Example Savings", "nickname": "Reserve", "status": "active", "type": "mercury", "kind": "savings", "createdAt": "2025-01-01T00:00:00Z"},
			{"id": "acct-treasury", "accountNumber": "000012340003", "name": "Example Treasury", "status": "active", "type": "mercury", "kind": "treasury", "createdAt": "2025-01-01T00:00:00Z"},
			{"id": "acct-closed", "accountNumber": "000012340004", "name": "Old Checking", "status": "archived", "type": "mercury", "kind": "checking", "createdAt": "2023-01-01T00:00:00Z"},
		},
		credit: []map[string]any{{"id": "acct-credit", "status": "active", "createdAt": "2025-06-01T00:00:00Z"}},
		txs: map[string][]*fakeTx{
			"acct-checking": {
				{"tx-opening", "sent", "externalTransfer", "2024-01-15T12:00:00Z", "2024-01-15T13:00:00Z", "Example Founder", 500000},
				{"tx-supplies", "sent", "debitCardTransaction", "2026-11-02T15:00:00Z", "2026-11-03T09:00:00Z", "Example Office Supply", -4500},
				{"tx-deposit", "sent", "externalTransfer", "2026-11-04T15:00:00Z", "2026-11-04T16:00:00Z", "Example Customer", 120000},
				{"tx-pending", "pending", "debitCardTransaction", "2026-11-01T12:00:00Z", "", "Example Software", -1999},
				{"tx-cancelled", "cancelled", "outgoingPayment", "2026-11-05T12:00:00Z", "", "Example Cancelled", -500},
			},
			"acct-savings": {
				{"tx-interest", "sent", "interestPayment", "2026-10-31T00:00:00Z", "2026-10-31T00:00:00Z", "Example interest", 125},
			},
			"acct-credit": {
				{"tx-card-buy", "sent", "creditCardTransaction", "2026-11-06T10:00:00Z", "2026-11-07T10:00:00Z", "Example Cafe", -12340},
				{"tx-card-pay", "sent", "other", "2026-11-08T10:00:00Z", "2026-11-08T10:00:00Z", "Example card payment", 5000},
			},
		},
		status:      map[string]int{},
		queries:     map[string][]string{},
		hidden:      map[string]bool{},
		flipListed:  map[string]bool{},
		flipBalance: map[string]bool{},
		carried:     map[string]int64{},
	}
}

func dollars(cents int64) json.Number {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return json.Number(fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100))
}

func (f *fakeMercury) balance(account string) json.Number {
	sum := f.carried[account]
	for _, t := range f.txs[account] {
		if t.status == "sent" || (t.status == "pending" && f.balanceCountsPending) {
			sum += t.cents
		}
	}
	if f.flipBalance[account] {
		sum = -sum
	}
	if f.moveAfter > 0 && f.accountReads > f.moveAfter && account == "acct-checking" {
		sum += 100
	}
	return dollars(sum)
}

func (f *fakeMercury) withBalances(list []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, a := range list {
		v := map[string]any{"currentBalance": f.balance(a["id"].(string))}
		for k, x := range a {
			v[k] = x
		}
		out = append(out, v)
	}
	return out
}

func (f *fakeMercury) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.queries[r.URL.Path] = append(f.queries[r.URL.Path], r.URL.RawQuery)
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":{"message":"synthetic unauthorized"}}`))
		return
	}
	if code := f.status[r.URL.Path]; code != 0 {
		w.WriteHeader(code)
		w.Write([]byte(`{"errors":{"message":"synthetic"}}`))
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/accounts":
		f.accountReads++
		reply(map[string]any{"accounts": f.withBalances(f.accounts), "page": map[string]any{}})
	case r.URL.Path == "/credit":
		reply(map[string]any{"accounts": f.withBalances(f.credit)})
	case strings.HasPrefix(r.URL.Path, "/account/") && strings.HasSuffix(r.URL.Path, "/transactions"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/account/"), "/transactions")
		all, ok := f.txs[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Mercury's start and end are taken here as creation dates.
		start, end := r.URL.Query().Get("start"), r.URL.Query().Get("end")
		var list []map[string]any
		for _, t := range all {
			if f.hidden[t.id] || t.created[:10] < start || t.created[:10] > end {
				continue
			}
			cents := t.cents
			if f.flipListed[id] {
				cents = -cents
			}
			tx := map[string]any{"id": t.id, "amount": dollars(cents), "status": t.status, "kind": t.kind,
				"createdAt": t.created, "counterpartyName": t.name, "postedAt": nil}
			if t.posted != "" {
				tx["postedAt"] = t.posted
			}
			list = append(list, tx)
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		page := []map[string]any{}
		if offset < len(list) {
			page = list[offset:min(offset+2, len(list))]
		}
		reply(map[string]any{"total": len(list), "transactions": page})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// change edits the fake under its lock.
func (f *fakeMercury) change(edit func(f *fakeMercury)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

func (f *fakeMercury) tx(account, id string) *fakeTx {
	for _, t := range f.txs[account] {
		if t.id == id {
			return t
		}
	}
	panic("no synthetic transaction " + id)
}

func (f *fakeMercury) lastQuery(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.queries[path]
	if len(q) == 0 {
		return ""
	}
	return q[len(q)-1]
}

const mercuryToken = "secret-token:mercury_example_read_only"

func mercuryAt(t *testing.T, f *fakeMercury, day string) (*Mercury, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	now, err := time.Parse(time.RFC3339, day+"T18:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	return &Mercury{Client: srv.Client(), BaseURL: srv.URL, Now: func() time.Time { return now }}, &now
}

func byID(list []Transaction) map[string]Transaction {
	out := map[string]Transaction{}
	for _, v := range list {
		out[v.ProviderTransactionID] = v
	}
	return out
}

func checks(c Changes) map[string]BalanceCheck {
	out := map[string]BalanceCheck{}
	for _, v := range c.Checks {
		out[v.ProviderAccountID] = v
	}
	return out
}

func accountsIn(list []Transaction) map[string]bool {
	out := map[string]bool{}
	for _, v := range list {
		out[v.ProviderAccountID] = true
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestMercuryConnectToken(t *testing.T) {
	f := newFakeMercury()
	m, _ := mercuryAt(t, f, "2026-11-10")
	// Owners paste the token with or without Mercury's prefix.
	for _, pasted := range []string{mercuryToken, "  mercury_example_read_only\n"} {
		linked, err := m.ConnectToken(context.Background(), pasted)
		if err != nil {
			t.Fatalf("%q: %v", pasted, err)
		}
		if linked.InstitutionName != "Mercury" || linked.ItemID != "mercury:acct-checking" || linked.AccessToken != strings.TrimSpace(pasted) {
			t.Fatalf("linked %+v", linked)
		}
	}
	if got := f.auth[len(f.auth)-1]; got != "Bearer "+mercuryToken {
		t.Fatalf("authorization %q", got)
	}
	if _, err := m.ConnectToken(context.Background(), "secret-token:wrong"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("a refused token: %v", err)
	}
	if _, err := m.ConnectToken(context.Background(), " "); !errors.Is(err, ErrCredentials) {
		t.Fatalf("an empty token: %v", err)
	}
	if _, err := m.StartLink(context.Background(), Credentials{}, "owner", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("StartLink: %v", err)
	}
}

func TestMercuryFirstSync(t *testing.T) {
	f := newFakeMercury()
	m, _ := mercuryAt(t, f, "2026-11-10")
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, a := range got.Accounts {
		kinds[a.ProviderAccountID] = a.Kind + "/" + a.Subtype + "/" + a.Mask + "/" + a.Name
		if a.Currency != "USD" || a.Scale != 2 {
			t.Fatalf("account %+v", a)
		}
	}
	want := map[string]string{"acct-checking": "cash/checking/0001/Example Checking", "acct-savings": "cash/savings/0002/Reserve", "acct-credit": "card/credit card//Mercury Credit"}
	if len(kinds) != len(want) {
		t.Fatalf("accounts %v", kinds)
	}
	for k, v := range want {
		if kinds[k] != v {
			t.Fatalf("account %s = %q, want %q", k, kinds[k], v)
		}
	}
	// The whole history is read to check the balance; only the last two
	// years are reported.
	if q := f.lastQuery("/account/acct-checking/transactions"); !strings.Contains(q, "start=2023-03-01") || !strings.Contains(q, "end=2026-11-11") {
		t.Fatalf("query %q", q)
	}
	added := byID(got.Added)
	if _, ok := added["tx-opening"]; ok || len(added) != 6 || len(got.Modified) != 0 {
		t.Fatalf("added %d %v, modified %d", len(added), got.Added, len(got.Modified))
	}
	for id, amount := range map[string]int64{"tx-supplies": -4500, "tx-deposit": 120000, "tx-pending": -1999, "tx-interest": 125,
		// On the card a purchase raises what is owed and a payment lowers it.
		"tx-card-buy": 12340, "tx-card-pay": -5000} {
		if added[id].AmountMinor != amount {
			t.Fatalf("%s amount %d, want %d", id, added[id].AmountMinor, amount)
		}
	}
	if v := added["tx-supplies"]; v.Date != "2026-11-03" || v.AuthorizedDate != "2026-11-02" || v.Pending || v.Description != "Example Office Supply" {
		t.Fatalf("posted %+v", v)
	}
	if v := added["tx-pending"]; !v.Pending || v.Date != "2026-11-01" {
		t.Fatalf("pending %+v", v)
	}
	if len(got.Removed) != 1 || got.Removed[0] != "tx-cancelled" {
		t.Fatalf("removed %v", got.Removed)
	}
	// The summary the owner sees after connecting.
	c := checks(got)
	for id, want := range map[string]BalanceCheck{
		"acct-checking": {Transactions: 3, BalanceMinor: 615500},
		"acct-savings":  {Transactions: 1, BalanceMinor: 125},
		"acct-credit":   {Transactions: 2, BalanceMinor: 7340},
	} {
		v := c[id]
		if !v.Passed || v.Problem != "" || v.Transactions != want.Transactions || v.BalanceMinor != want.BalanceMinor || v.Currency != "USD" || v.Scale != 2 {
			t.Fatalf("check %s = %+v", id, v)
		}
	}
	// Each account's next sync starts from its oldest pending day, or a week
	// back, with the balance before what it will read again.
	if got.NextCursor != "mercury-v2;2026-11-10;acct-checking=2026-11-01,500000,501999,615500;acct-credit=2026-11-03,0,0,-7340;acct-savings=2026-11-03,125,125,125" {
		t.Fatalf("cursor %q", got.NextCursor)
	}
}

func TestMercuryIncrementalSyncPostsPending(t *testing.T) {
	for _, counts := range []bool{false, true} {
		t.Run(fmt.Sprintf("balance counts pending %v", counts), func(t *testing.T) {
			f := newFakeMercury()
			f.balanceCountsPending = counts
			m, now := mercuryAt(t, f, "2026-11-10")
			first, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range first.Checks {
				if !c.Passed {
					t.Fatalf("first sync %+v", c)
				}
			}
			// Two days later the pending charge posts for a different amount,
			// the card purchase is reversed and a new deposit arrives.
			*now = now.AddDate(0, 0, 2)
			f.change(func(f *fakeMercury) {
				p := f.tx("acct-checking", "tx-pending")
				p.status, p.posted, p.cents = "sent", "2026-11-11T08:00:00Z", -2150
				f.tx("acct-credit", "tx-card-buy").status = "reversed"
				f.txs["acct-checking"] = append(f.txs["acct-checking"], &fakeTx{"tx-new", "sent", "externalTransfer", "2026-11-11T10:00:00Z", "2026-11-11T11:00:00Z", "Example New Customer", 30000})
			})
			got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
			if err != nil {
				t.Fatal(err)
			}
			if q := f.lastQuery("/account/acct-checking/transactions"); !strings.Contains(q, "start=2026-11-01") {
				t.Fatalf("the next sync must reread from the oldest pending day: %q", q)
			}
			for _, c := range got.Checks {
				if !c.Passed {
					t.Fatalf("incremental sync %+v", c)
				}
			}
			added, modified := byID(got.Added), byID(got.Modified)
			if v, ok := added["tx-new"]; !ok || v.AmountMinor != 30000 || len(added) != 1 {
				t.Fatalf("added %+v", got.Added)
			}
			posted, ok := modified["tx-pending"]
			if !ok || posted.Pending || posted.Date != "2026-11-11" || posted.AuthorizedDate != "2026-11-01" || posted.AmountMinor != -2150 {
				t.Fatalf("pending to posted %+v", posted)
			}
			if !contains(got.Removed, "tx-card-buy") || !contains(got.Removed, "tx-cancelled") {
				t.Fatalf("removed %v", got.Removed)
			}
			if c := checks(got)["acct-credit"]; c.BalanceMinor != -5000 {
				t.Fatalf("card after the reversal %+v", c)
			}
			// Nothing is pending now, so the following sync reads only the
			// last week, and still adds up.
			if !strings.HasPrefix(got.NextCursor, "mercury-v2;2026-11-12;acct-checking=2026-11-05,") {
				t.Fatalf("cursor %q", got.NextCursor)
			}
			third, err := m.Changes(context.Background(), Credentials{}, mercuryToken, got.NextCursor)
			if err != nil {
				t.Fatal(err)
			}
			if q := f.lastQuery("/account/acct-checking/transactions"); !strings.Contains(q, "start=2026-11-05") || !strings.Contains(q, "end=2026-11-13") {
				t.Fatalf("query %q", q)
			}
			for _, c := range third.Checks {
				if !c.Passed {
					t.Fatalf("third sync %+v", c)
				}
			}
		})
	}
}

func TestMercuryHoldsAnAccountThatDoesNotAddUp(t *testing.T) {
	held := func(t *testing.T, got Changes, account string) {
		t.Helper()
		c := checks(got)[account]
		if c.Passed || !strings.Contains(c.Problem, "don't add up") || !strings.Contains(c.Problem, "nothing was recorded") {
			t.Fatalf("%s check %+v", account, c)
		}
		in := accountsIn(append(append([]Transaction{}, got.Added...), got.Modified...))
		if in[account] {
			t.Fatalf("%s activity was reported", account)
		}
		for id, other := range map[string]bool{"acct-checking": true, "acct-savings": true, "acct-credit": true} {
			if other && id != account && (!in[id] || !checks(got)[id].Passed) {
				t.Fatalf("%s was held with %s", id, account)
			}
		}
		if strings.Contains(got.NextCursor, account+"=") {
			t.Fatalf("a first sync that didn't add up left a base: %q", got.NextCursor)
		}
	}
	t.Run("a missing transaction", func(t *testing.T) {
		f := newFakeMercury()
		f.hidden["tx-deposit"] = true
		m, _ := mercuryAt(t, f, "2026-11-10")
		got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
		if err != nil {
			t.Fatal(err)
		}
		held(t, got, "acct-checking")
		if contains(got.Removed, "tx-cancelled") {
			t.Fatal("a held account's removals were reported")
		}
	})
	t.Run("amounts signed the other way", func(t *testing.T) {
		f := newFakeMercury()
		f.flipListed["acct-checking"] = true
		m, _ := mercuryAt(t, f, "2026-11-10")
		got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
		if err != nil {
			t.Fatal(err)
		}
		held(t, got, "acct-checking")
	})
	t.Run("a card whose amounts and balance are both signed the other way", func(t *testing.T) {
		// The sums agree, but a purchase that puts money in shows the signs
		// aren't the ones Vult reads.
		f := newFakeMercury()
		f.flipListed["acct-credit"], f.flipBalance["acct-credit"] = true, true
		m, _ := mercuryAt(t, f, "2026-11-10")
		got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
		if err != nil {
			t.Fatal(err)
		}
		held(t, got, "acct-credit")
	})
	t.Run("a transaction missed after the first sync", func(t *testing.T) {
		f := newFakeMercury()
		m, now := mercuryAt(t, f, "2026-11-10")
		first, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
		if err != nil {
			t.Fatal(err)
		}
		*now = now.AddDate(0, 0, 1)
		f.change(func(f *fakeMercury) {
			f.txs["acct-checking"] = append(f.txs["acct-checking"], &fakeTx{"tx-late", "sent", "externalTransfer", "2026-11-11T10:00:00Z", "2026-11-11T11:00:00Z", "Example Late", 7000})
			f.hidden["tx-late"] = true
		})
		got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		c := checks(got)["acct-checking"]
		if c.Passed || accountsIn(got.Modified)["acct-checking"] {
			t.Fatalf("check %+v", c)
		}
		// The last base that added up is kept, so the account recovers once
		// Mercury lists the transaction.
		base := "acct-checking=2026-11-01,500000,501999,615500"
		if !strings.Contains(got.NextCursor, base) {
			t.Fatalf("cursor %q", got.NextCursor)
		}
		f.change(func(f *fakeMercury) { delete(f.hidden, "tx-late") })
		again, err := m.Changes(context.Background(), Credentials{}, mercuryToken, got.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		if c := checks(again)["acct-checking"]; !c.Passed || !accountsIn(again.Added)["acct-checking"] {
			t.Fatalf("after the transaction appeared %+v", c)
		}
	})
}

func TestMercuryRetriesWhenABalanceMovesDuringTheSync(t *testing.T) {
	f := newFakeMercury()
	f.moveAfter = 1
	m, _ := mercuryAt(t, f, "2026-11-10")
	if _, err := m.Changes(context.Background(), Credentials{}, mercuryToken, ""); !errors.Is(err, ErrProvider) {
		t.Fatalf("moved balance: %v", err)
	}
}

func TestMercuryRevokedTokenNeedsReconnect(t *testing.T) {
	f := newFakeMercury()
	m, _ := mercuryAt(t, f, "2026-11-10")
	first, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
	if err != nil {
		t.Fatal(err)
	}
	f.change(func(f *fakeMercury) { f.token = "secret-token:a-new-token" })
	if _, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor); !errors.Is(err, ErrReconnect) {
		t.Fatalf("revoked token: %v", err)
	}
	f.change(func(f *fakeMercury) { f.status["/accounts"] = http.StatusForbidden })
	if _, err := m.Changes(context.Background(), Credentials{}, "secret-token:a-new-token", first.NextCursor); !errors.Is(err, ErrReconnect) {
		t.Fatalf("forbidden: %v", err)
	}
}

func TestMercuryOutageIsRetried(t *testing.T) {
	for path, code := range map[string]int{
		"/accounts":                           http.StatusServiceUnavailable,
		"/account/acct-checking/transactions": http.StatusInternalServerError,
		"/credit":                             http.StatusBadGateway,
		"/account/acct-savings/transactions":  http.StatusTooManyRequests,
	} {
		f := newFakeMercury()
		m, _ := mercuryAt(t, f, "2026-11-10")
		f.status[path] = code
		if _, err := m.Changes(context.Background(), Credentials{}, mercuryToken, ""); !errors.Is(err, ErrProvider) {
			t.Fatalf("%s %d: %v", path, code, err)
		}
	}
	m := &Mercury{BaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}}
	if _, err := m.Changes(context.Background(), Credentials{}, mercuryToken, ""); !errors.Is(err, ErrProvider) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestMercuryWithoutCredit(t *testing.T) {
	// A business without Mercury credit.
	f := newFakeMercury()
	m, _ := mercuryAt(t, f, "2026-11-10")
	f.status["/credit"] = http.StatusNotFound
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
	if err != nil || len(got.Accounts) != 2 || len(got.Checks) != 2 {
		t.Fatalf("accounts %+v, %v", got.Accounts, err)
	}
	// A credit account whose transactions can't be read is left out.
	f = newFakeMercury()
	m, _ = mercuryAt(t, f, "2026-11-10")
	delete(f.txs, "acct-credit")
	got, err = m.Changes(context.Background(), Credentials{}, mercuryToken, "")
	if err != nil || len(got.Accounts) != 2 || accountsIn(got.Added)["acct-credit"] {
		t.Fatalf("accounts %+v, %v", got.Accounts, err)
	}
}

func TestMercuryRejectsForeignCursor(t *testing.T) {
	m, _ := mercuryAt(t, newFakeMercury(), "2026-11-10")
	for _, cursor := range []string{"other-cursor", "mercury-v2;2026-11-10;acct-checking=bad"} {
		if _, err := m.Changes(context.Background(), Credentials{}, mercuryToken, cursor); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
	}
}

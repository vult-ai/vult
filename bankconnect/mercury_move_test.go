package bankconnect

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These tests follow a business whose bank moves its accounts to a new
// partner bank: Mercury opens new accounts with new numbers, the money
// moves across, and the old accounts are archived. All data is synthetic.

const (
	oldRouting = "000000011"
	newRouting = "000000022"
)

// movingFake is the usual fake with routing numbers, synced once.
func movingFake(t *testing.T) (*Mercury, *fakeMercury, *time.Time, Changes) {
	t.Helper()
	f := newFakeMercury()
	for _, a := range f.accounts {
		a["routingNumber"] = oldRouting
	}
	m, now := mercuryAt(t, f, "2026-11-10")
	first, err := m.Changes(context.Background(), Credentials{}, mercuryToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Replacements) != 0 || len(first.Closed) != 0 {
		t.Fatalf("a first sync proposed %+v, closed %v", first.Replacements, first.Closed)
	}
	return m, f, now, first
}

func openNewChecking(f *fakeMercury, id, name, created string) {
	f.accounts = append(f.accounts, map[string]any{"id": id, "accountNumber": "000098760009", "routingNumber": newRouting,
		"name": name, "status": "active", "type": "mercury", "kind": "checking", "createdAt": created})
	f.txs[id] = nil
}

func archive(f *fakeMercury, id string) {
	for _, a := range f.accounts {
		if a["id"] == id {
			a["status"] = "archived"
		}
	}
}

func noProblems(t *testing.T, got Changes) {
	t.Helper()
	for _, c := range got.Checks {
		if !c.Passed || c.Problem != "" {
			t.Fatalf("check %+v", c)
		}
	}
}

func TestMercuryMoveWithATransferBothAccountsShow(t *testing.T) {
	m, f, now, first := movingFake(t)
	// Ten days later Mercury opens the new checking account and moves the
	// posted balance across with a transfer both accounts show.
	*now = now.AddDate(0, 0, 10)
	f.change(func(f *fakeMercury) {
		openNewChecking(f, "acct-checking-next", "Example Checking", "2026-11-20T09:00:00Z")
		f.txs["acct-checking"] = append(f.txs["acct-checking"], &fakeTx{"tx-move-out", "sent", "internalTransfer", "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z", "Transfer to new account", -615500})
		f.txs["acct-checking-next"] = append(f.txs["acct-checking-next"], &fakeTx{"tx-move-in", "sent", "internalTransfer", "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z", "Transfer from old account", 615500})
	})
	moving, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	noProblems(t, moving)
	if len(moving.Replacements) != 1 {
		t.Fatalf("replacements %+v", moving.Replacements)
	}
	r := moving.Replacements[0]
	if r.OldProviderAccountID != "acct-checking" || r.NewProviderAccountID != "acct-checking-next" ||
		!strings.Contains(r.Reason, "Example Checking ••0009") || !strings.Contains(r.Reason, "take over Example Checking ••0001") {
		t.Fatalf("replacement %+v", r)
	}
	// Both legs are reported, so recorded to one Book account they net to
	// zero.
	added := byID(moving.Added)
	if added["tx-move-out"].AmountMinor+added["tx-move-in"].AmountMinor != 0 || added["tx-move-in"].ProviderAccountID != "acct-checking-next" {
		t.Fatalf("move legs %+v %+v", added["tx-move-out"], added["tx-move-in"])
	}
	if c := checks(moving)["acct-checking-next"]; c.BalanceMinor != 615500 || c.Transactions != 1 {
		t.Fatalf("new account check %+v", c)
	}

	// Five days later the old account is archived: it is read one last time,
	// closed without a problem, and then left out.
	*now = now.AddDate(0, 0, 5)
	f.change(func(f *fakeMercury) { archive(f, "acct-checking") })
	closing, err := m.Changes(context.Background(), Credentials{}, mercuryToken, moving.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	noProblems(t, closing)
	if len(closing.Closed) != 1 || closing.Closed[0] != "acct-checking" || len(closing.Replacements) != 0 {
		t.Fatalf("closed %v replacements %+v", closing.Closed, closing.Replacements)
	}
	if strings.Contains(closing.NextCursor, "acct-checking=") || !strings.Contains(closing.NextCursor, "acct-checking-next=") {
		t.Fatalf("cursor %q", closing.NextCursor)
	}
	*now = now.AddDate(0, 0, 1)
	after, err := m.Changes(context.Background(), Credentials{}, mercuryToken, closing.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	noProblems(t, after)
	if len(after.Closed) != 0 || checks(after)["acct-checking"].ProviderAccountID != "" {
		t.Fatalf("the closed account came back: %+v", after)
	}
}

func TestMercuryMoveThatCarriesTheBalanceOverSilently(t *testing.T) {
	m, f, now, first := movingFake(t)
	// Mercury archives the old account, stops showing its transactions, and
	// opens the new one already holding the money, with no transfer.
	*now = now.AddDate(0, 0, 10)
	f.change(func(f *fakeMercury) {
		archive(f, "acct-checking")
		delete(f.txs, "acct-checking")
		openNewChecking(f, "acct-checking-next", "Example Checking", "2026-11-20T09:00:00Z")
		f.carried["acct-checking-next"] = 615500
		f.txs["acct-checking-next"] = append(f.txs["acct-checking-next"], &fakeTx{"tx-after", "sent", "externalTransfer", "2026-11-21T10:00:00Z", "2026-11-21T11:00:00Z", "Example Customer", 1000})
	})
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	// Alone the new account doesn't add up from zero; with the old account's
	// last balance it does.
	noProblems(t, got)
	if len(got.Replacements) != 1 || got.Replacements[0].NewProviderAccountID != "acct-checking-next" {
		t.Fatalf("replacements %+v", got.Replacements)
	}
	if len(got.Closed) != 1 || got.Closed[0] != "acct-checking" {
		t.Fatalf("closed %v", got.Closed)
	}
	if v, ok := byID(got.Added)["tx-after"]; !ok || v.AmountMinor != 1000 {
		t.Fatalf("added %+v", got.Added)
	}
	// From now on the new account adds up on its own.
	*now = now.AddDate(0, 0, 1)
	f.change(func(f *fakeMercury) {
		f.txs["acct-checking-next"] = append(f.txs["acct-checking-next"], &fakeTx{"tx-later", "sent", "debitCardTransaction", "2026-11-21T15:00:00Z", "2026-11-21T16:00:00Z", "Example Office Supply", -2500})
	})
	later, err := m.Changes(context.Background(), Credentials{}, mercuryToken, got.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	noProblems(t, later)
	if len(later.Replacements) != 0 || len(later.Closed) != 0 {
		t.Fatalf("later %+v", later)
	}
}

func TestMercuryMoveThatDoesNotAddUpIsHeld(t *testing.T) {
	m, f, now, first := movingFake(t)
	// The new account holds less than the old one did, and no transaction
	// says where the rest went.
	*now = now.AddDate(0, 0, 10)
	f.change(func(f *fakeMercury) {
		archive(f, "acct-checking")
		delete(f.txs, "acct-checking")
		openNewChecking(f, "acct-checking-next", "Example Checking", "2026-11-20T09:00:00Z")
		f.carried["acct-checking-next"] = 600000
	})
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	c := checks(got)
	if c["acct-checking-next"].Passed || !strings.Contains(c["acct-checking-next"].Problem, "don't add up") {
		t.Fatalf("new account %+v", c["acct-checking-next"])
	}
	if !strings.Contains(c["acct-checking"].Problem, "before Vult could read its last activity") {
		t.Fatalf("old account %+v", c["acct-checking"])
	}
	// The replacement is still proposed: the owner decides what it is.
	if len(got.Replacements) != 1 {
		t.Fatalf("replacements %+v", got.Replacements)
	}
}

func TestMercuryClosedAccountWithoutReplacement(t *testing.T) {
	m, f, now, first := movingFake(t)
	// Savings is emptied, then archived and no longer readable: it closes
	// quietly.
	*now = now.AddDate(0, 0, 2)
	f.change(func(f *fakeMercury) {
		f.txs["acct-savings"] = append(f.txs["acct-savings"], &fakeTx{"tx-empty", "sent", "internalTransfer", "2026-11-12T10:00:00Z", "2026-11-12T10:00:00Z", "Example sweep", -125})
	})
	emptied, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.AddDate(0, 0, 3)
	f.change(func(f *fakeMercury) {
		archive(f, "acct-savings")
		delete(f.txs, "acct-savings")
	})
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, emptied.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	noProblems(t, got)
	if len(got.Closed) != 1 || got.Closed[0] != "acct-savings" || len(got.Replacements) != 0 {
		t.Fatalf("closed %v replacements %+v", got.Closed, got.Replacements)
	}
	// An account that disappears while it still held money is a problem.
	m2, f2, now2, first2 := movingFake(t)
	*now2 = now2.AddDate(0, 0, 1)
	f2.change(func(f *fakeMercury) {
		var kept []map[string]any
		for _, a := range f.accounts {
			if a["id"] != "acct-savings" {
				kept = append(kept, a)
			}
		}
		f.accounts = kept
		delete(f.txs, "acct-savings")
	})
	gone, err := m2.Changes(context.Background(), Credentials{}, mercuryToken, first2.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if c := checks(gone)["acct-savings"]; c.Passed || !strings.Contains(c.Problem, "before Vult could read its last activity") {
		t.Fatalf("vanished savings %+v", c)
	}
}

func TestMercuryReplacementOnlyWhenUnambiguous(t *testing.T) {
	m, f, now, first := movingFake(t)
	// Two new checking accounts appear; only the one with the old account's
	// name is proposed.
	*now = now.AddDate(0, 0, 10)
	f.change(func(f *fakeMercury) {
		openNewChecking(f, "acct-checking-next", "Example Checking", "2026-11-20T09:00:00Z")
		openNewChecking(f, "acct-payroll", "Example Payroll", "2026-11-20T09:00:00Z")
	})
	got, err := m.Changes(context.Background(), Credentials{}, mercuryToken, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Replacements) != 1 || got.Replacements[0].NewProviderAccountID != "acct-checking-next" {
		t.Fatalf("replacements %+v", got.Replacements)
	}
	// A new account at the same bank as the old ones is just a new account.
	m2, f2, now2, first2 := movingFake(t)
	*now2 = now2.AddDate(0, 0, 10)
	f2.change(func(f *fakeMercury) {
		openNewChecking(f, "acct-second", "Example Checking", "2026-11-20T09:00:00Z")
		f.accounts[len(f.accounts)-1]["routingNumber"] = oldRouting
	})
	plain, err := m2.Changes(context.Background(), Credentials{}, mercuryToken, first2.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Replacements) != 0 {
		t.Fatalf("replacements %+v", plain.Replacements)
	}
}

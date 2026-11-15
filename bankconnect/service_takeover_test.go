package bankconnect

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// takeoverFixture is a Book with an owner, a scope that records bank
// activity, and a service whose Mercury is the fake. Data is synthetic.
type takeoverFixture struct {
	db      *testutil.Database
	service *Service
	owner   ledger.TenantAdmin
	book    ledger.Book
	scope   ledger.Scope
	actor   string
}

func newTakeoverFixture(t *testing.T, m *Mercury) *takeoverFixture {
	t.Helper()
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureBookOwner(db.Context, book.ID); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.Admin.QueryRow(db.Context, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", book.TenantID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db.Store, Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "keys.json")}},
		Connectors: map[string]Connector{"mercury": m}}
	return &takeoverFixture{db: db, service: service, owner: owner, book: book, scope: scope, actor: actor}
}

func (f *takeoverFixture) sync(t *testing.T, connection string) {
	t.Helper()
	if _, err := f.service.Sync(f.db.Context, ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: connection}); err != nil {
		t.Fatal("sync", err)
	}
}

func (f *takeoverFixture) view(t *testing.T) ledger.BankConnectionView {
	t.Helper()
	views, err := f.owner.BankConnections(f.db.Context, f.book.ID)
	if err != nil || len(views) != 1 {
		t.Fatal(views, err)
	}
	return views[0]
}

func bankAccountNamed(t *testing.T, v ledger.BankConnectionView, name, mask string) ledger.BankAccount {
	t.Helper()
	for _, a := range v.Accounts {
		if a.Name == name && a.Mask == mask {
			return a
		}
	}
	t.Fatalf("no bank account %s ••%s in %+v", name, mask, v.Accounts)
	return ledger.BankAccount{}
}

func (f *takeoverFixture) item(t *testing.T, description string) ledger.BankTransaction {
	t.Helper()
	page, _, err := f.owner.BankActivity(f.db.Context, f.book.ID, ledger.BankActivityQuery{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range page.Items {
		if v.Description == description {
			return v
		}
	}
	t.Fatalf("no item %q", description)
	return ledger.BankTransaction{}
}

func TestServiceFollowsMercuryToANewPartnerBank(t *testing.T) {
	fake := newFakeMercury()
	for _, a := range fake.accounts {
		a["routingNumber"] = oldRouting
	}
	m, now := mercuryAt(t, fake, "2026-11-10")
	f := newTakeoverFixture(t, m)
	ctx := f.db.Context
	connection, err := f.service.ConnectToken(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, "mercury", mercuryToken)
	if err != nil {
		t.Fatal(err)
	}
	f.sync(t, connection)
	cash := f.db.Account(t, f.scope, "1010", "asset")
	old := bankAccountNamed(t, f.view(t), "Example Checking", "0001")
	if err := f.owner.LinkBankAccount(ctx, f.book.ID, f.actor, old.ID, cash.ID); err != nil {
		t.Fatal(err)
	}

	// Mercury opens the new checking account and moves the money across.
	*now = now.AddDate(0, 0, 10)
	fake.change(func(f *fakeMercury) {
		openNewChecking(f, "acct-checking-next", "Example Checking", "2026-11-20T09:00:00Z")
		f.txs["acct-checking"] = append(f.txs["acct-checking"], &fakeTx{"tx-move-out", "sent", "internalTransfer", "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z", "Transfer to new account", -615500})
		f.txs["acct-checking-next"] = append(f.txs["acct-checking-next"], &fakeTx{"tx-move-in", "sent", "internalTransfer", "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z", "Transfer from old account", 615500})
	})
	f.sync(t, connection)
	view := f.view(t)
	next := bankAccountNamed(t, view, "Example Checking", "0009")
	if next.TakesOver != old.ID || !strings.Contains(next.TakeoverReason, "take over Example Checking") ||
		next.AccountID != "" || next.SuggestedAccountID != cash.ID || view.StatusReason != "" {
		t.Fatalf("the takeover proposal %+v (status %q)", next, view.StatusReason)
	}
	// Another bank account can't share the Book account.
	savings := bankAccountNamed(t, view, "Reserve", "0002")
	if err := f.owner.LinkBankAccount(ctx, f.book.ID, f.actor, savings.ID, cash.ID); err == nil {
		t.Fatal("two bank accounts shared a Book account without a takeover")
	}

	// One Confirm: link the new account to the old one's Book account.
	var entries int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entries WHERE book_id=$1", f.book.ID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.LinkBankAccount(ctx, f.book.ID, f.actor, next.ID, cash.ID); err != nil {
		t.Fatal("confirm", err)
	}
	view = f.view(t)
	if a := bankAccountNamed(t, view, "Example Checking", "0001"); a.TakenOverBy != next.ID || a.AccountID != cash.ID {
		t.Fatalf("old account after confirming %+v", a)
	}
	if a := bankAccountNamed(t, view, "Example Checking", "0009"); a.AccountID != cash.ID {
		t.Fatalf("new account after confirming %+v", a)
	}

	// The money moving across is suggested as a transfer and records with
	// no entry: it never left the Book account.
	out, in := f.item(t, "Transfer to new account"), f.item(t, "Transfer from old account")
	_, suggestions, err := f.owner.BankActivity(ctx, f.book.ID, ledger.BankActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var pair []string
	for _, s := range suggestions {
		if s.Kind == "transfer" && strings.Contains(s.Reason, "no entry") {
			pair = s.TransactionIDs
		}
	}
	if len(pair) != 2 || !(pair[0] == out.ID && pair[1] == in.ID || pair[0] == in.ID && pair[1] == out.ID) {
		t.Fatalf("move suggestion %+v", suggestions)
	}
	var result ledger.BankLinkResult
	if err := json.Unmarshal(f.db.Call(t, f.scope, "record_bank_transfer", ledger.RecordBankTransfer{RequestKey: "move", TransactionIDs: pair}), &result); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entries WHERE book_id=$1", f.book.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !result.MovedWithinAccount || result.Entry != nil || after != entries {
		t.Fatalf("the move posted an entry: %+v, %d entries before, %d after", result, entries, after)
	}
	if f.item(t, "Transfer to new account").Status != "recorded" || f.item(t, "Transfer from old account").Status != "recorded" {
		t.Fatal("the move's items are not recorded")
	}
	if _, err := f.db.Store.Call(ctx, f.scope, "record_bank_transfer", json.RawMessage(`{"request_key":"move-again","transaction_ids":["`+pair[0]+`","`+pair[1]+`"]}`)); err == nil {
		t.Fatal("the move recorded twice")
	}

	// Mercury archives the old account: its last pending charge will never
	// post, and closing needs nothing from the owner.
	*now = now.AddDate(0, 0, 5)
	fake.change(func(f *fakeMercury) { archive(f, "acct-checking") })
	f.sync(t, connection)
	view = f.view(t)
	if a := bankAccountNamed(t, view, "Example Checking", "0001"); !a.Closed || view.StatusReason != "" || view.Status != "active" {
		t.Fatalf("closed account %+v, status %q %q", a, view.Status, view.StatusReason)
	}
	if f.item(t, "Example Software").Status != "removed" {
		t.Fatal("a closed account's pending charge is still pending")
	}
	*now = now.AddDate(0, 0, 1)
	f.sync(t, connection)
}

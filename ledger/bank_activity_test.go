package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// sealedForTest stands in for a value the application sealed. The ledger
// stores it without ever opening it.
var sealedForTest = bytes.Repeat([]byte{0x5a}, 48)

type bankFixture struct {
	db                     *testutil.Database
	book                   ledger.Book
	s                      ledger.Scope
	owner                  ledger.TenantAdmin
	actor                  string
	target                 ledger.BankSyncTarget
	checking, card, office ledger.Account
	accounts               map[string]string // provider account ID -> bank account ID
}

func newBankFixture(t *testing.T) *bankFixture {
	t.Helper()
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	actor, _ := proposalOwner(t, db, s)
	f := &bankFixture{db: db, book: book, s: s, owner: owner, actor: actor,
		checking: byCode["1110"], card: byCode["2100"], office: byCode["6100"], accounts: map[string]string{}}
	if f.office.ID == "" {
		for code, a := range byCode {
			if a.Class == "expense" && !a.Heading && strings.HasPrefix(code, "6") {
				f.office = a
				break
			}
		}
	}
	id, err := owner.AddBankConnection(db.Context, book.ID, actor, ledger.NewBankConnection{Provider: "plaid", Environment: "sandbox",
		ProviderItemID: "item-" + ledger.ID(), InstitutionName: "Example Bank", TokenSealed: sealedForTest, KeyVersion: 1})
	succeeded(t, err)
	f.target = ledger.BankSyncTarget{TenantID: book.TenantID, BookID: book.ID, ConnectionID: id}
	_, err = db.Store.ApplyBankSync(db.Context, f.target, ledger.BankSyncBatch{NextCursor: "c1", Complete: true, Accounts: []ledger.BankSyncAccount{
		{ProviderAccountID: "acc-checking", Name: "Example Checking", Mask: "0001", Type: "depository", Subtype: "checking", Kind: "cash", Currency: book.Currency, Scale: book.Scale},
		{ProviderAccountID: "acc-card", Name: "Example Card", Mask: "0002", Type: "credit", Subtype: "credit card", Kind: "card", Currency: book.Currency, Scale: book.Scale},
	}})
	succeeded(t, err)
	views, err := owner.BankConnections(db.Context, book.ID)
	succeeded(t, err)
	for _, a := range views[0].Accounts {
		if a.Kind == "cash" {
			f.accounts["acc-checking"] = a.ID
		} else {
			f.accounts["acc-card"] = a.ID
		}
	}
	succeeded(t, owner.LinkBankAccount(db.Context, book.ID, actor, f.accounts["acc-checking"], f.checking.ID))
	succeeded(t, owner.LinkBankAccount(db.Context, book.ID, actor, f.accounts["acc-card"], f.card.ID))
	return f
}

func (f *bankFixture) sync(t *testing.T, from, next string, added, modified []ledger.BankSyncTransaction, removed ...string) ledger.BankSyncResult {
	t.Helper()
	out, err := f.db.Store.ApplyBankSync(f.db.Context, f.target, ledger.BankSyncBatch{FromCursor: from, NextCursor: next, Added: added, Modified: modified, Removed: removed, Complete: true})
	succeeded(t, err)
	return out
}

func (f *bankFixture) tx(account, id, date string, amount int64, description string) ledger.BankSyncTransaction {
	return ledger.BankSyncTransaction{ProviderAccountID: account, ProviderTransactionID: id, Date: date, AmountMinor: amount,
		Currency: f.book.Currency, Scale: f.book.Scale, Description: description}
}

func (f *bankFixture) inbox(t *testing.T, s ledger.Scope, status string) ledger.BankActivityPage {
	t.Helper()
	var page ledger.BankActivityPage
	if json.Unmarshal(f.db.Call(t, s, "list_bank_activity", ledger.BankActivityQuery{Status: status}), &page) != nil {
		t.Fatal("invalid bank activity page")
	}
	return page
}

func byDescription(page ledger.BankActivityPage, description string) ledger.BankTransaction {
	for _, v := range page.Items {
		if v.Description == description {
			return v
		}
	}
	return ledger.BankTransaction{}
}

func bankRefused(t *testing.T, err error, words string) {
	t.Helper()
	var r *ledger.RefusedError
	if !errors.As(err, &r) || !strings.Contains(strings.ToLower(r.Reason), strings.ToLower(words)) {
		t.Fatalf("expected a refusal about %q, got %v", words, err)
	}
}

func TestBankSyncContinuesFromItsCursorAndKeepsActivityOutOfTheLedger(t *testing.T) {
	f := newBankFixture(t)
	got := f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "t1", "2026-03-02", -4500, "Example Office Supply"),
		f.tx("acc-checking", "t2", "2026-03-03", 120000, "Example Customer payment"),
		{ProviderAccountID: "acc-card", ProviderTransactionID: "t3", Date: "2026-03-04", AmountMinor: 900, Currency: f.book.Currency, Scale: f.book.Scale, Description: "Example Cafe", Pending: true},
		f.tx("acc-unknown", "t4", "2026-03-04", 100, "Not described"),
	}, nil)
	if got.Added != 3 {
		t.Fatalf("added = %+v; activity on an undescribed account must be left out", got)
	}
	// A repeated page from the same cursor is refused rather than applied twice.
	if _, err := f.db.Store.ApplyBankSync(f.db.Context, f.target, ledger.BankSyncBatch{FromCursor: "c1", NextCursor: "c2"}); !errors.Is(err, ledger.ErrBankCursorMoved) {
		t.Fatalf("stale cursor = %v", err)
	}
	page := f.inbox(t, f.s, "all")
	if page.Summary.Unrecorded != 2 || page.Summary.Pending != 1 || len(page.Items) != 3 {
		t.Fatalf("summary = %+v, items %d", page.Summary, len(page.Items))
	}
	var lines int
	succeeded(t, f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.lines WHERE tenant_id=$1 AND book_id=$2", f.book.TenantID, f.book.ID).Scan(&lines))
	if lines != 0 {
		t.Fatal("synced activity posted to the ledger")
	}
	// Changes and removals update the same transaction.
	changed := f.tx("acc-checking", "t1", "2026-03-02", -4700, "Example Office Supply")
	f.sync(t, "c2", "c3", nil, []ledger.BankSyncTransaction{changed}, "t2")
	page = f.inbox(t, f.s, "all")
	if v := byDescription(page, "Example Office Supply"); v.AmountMinor != "-4700" || v.Status != "unrecorded" {
		t.Fatalf("modified = %+v", v)
	}
	if v := byDescription(page, "Example Customer payment"); v.Status != "removed" {
		t.Fatalf("removed = %+v", v)
	}
}

func TestBankActivityIsRecordedOnceWithTheRightAmount(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "t1", "2026-03-02", -4500, "Example Office Supply")}, nil)
	item := byDescription(f.inbox(t, f.s, ""), "Example Office Supply")
	wrong := ledger.RecordBankActivity{RequestKey: "wrong", TransactionID: item.ID, Description: "Office supplies",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4000"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "4000"}}}
	_, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(wrong))
	bankRefused(t, err, "transaction amount")
	other := wrong
	other.RequestKey, other.Lines = "elsewhere", []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4500"}, {AccountID: f.card.ID, Side: "credit", AmountMinor: "4500"}}
	_, err = f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(other))
	bankRefused(t, err, "bank account's book account")

	right := wrong
	right.RequestKey, right.Lines = "right", []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4500"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "4500"}}
	first := f.db.Call(t, f.s, "record_bank_activity", right)
	if !bytes.Equal(first, f.db.Call(t, f.s, "record_bank_activity", right)) {
		t.Fatal("an exact retry changed the result")
	}
	var result ledger.BankLinkResult
	if json.Unmarshal(first, &result) != nil || result.Entry == nil || result.Entry.EconomicDate != "2026-03-02" || len(result.Links) != 1 {
		t.Fatalf("result = %s", first)
	}
	// The same transaction can't be recorded a second time, by a new entry or
	// by linking another.
	again := right
	again.RequestKey = "again"
	_, err = f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(again))
	bankRefused(t, err, "already recorded")
	extra := f.db.Call(t, f.s, "post_journal", ledger.PostJournal{RequestKey: "extra", EconomicDate: "2026-03-02", AccountingDate: "2026-03-02", Description: "Duplicate",
		Lines: right.Lines})
	var entry ledger.Entry
	json.Unmarshal(extra, &entry)
	_, err = f.db.Store.Call(f.db.Context, f.s, "link_bank_activity", value(ledger.LinkBankActivity{RequestKey: "dup-link", TransactionID: item.ID, EntryID: entry.ID}))
	bankRefused(t, err, "already recorded")
	if page := f.inbox(t, f.s, ""); page.Summary.Unrecorded != 0 {
		t.Fatalf("still unrecorded: %+v", page.Summary)
	}
	// Unlinking with a reason makes it recordable again, and the other
	// entry can then be linked.
	f.db.Call(t, f.s, "unlink_bank_activity", ledger.UnlinkBankActivity{RequestKey: "unlink", LinkID: result.Links[0].ID, Reason: "Recorded on the wrong date"})
	f.db.Call(t, f.s, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "relink", TransactionID: item.ID, EntryID: entry.ID})
	if v := byDescription(f.inbox(t, f.s, "recorded"), "Example Office Supply"); v.Link == nil || v.Link.EntryID != entry.ID {
		t.Fatalf("relinked = %+v", v)
	}
	// Reversing the recording entry puts the activity back in the inbox.
	f.db.Call(t, f.s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: entry.ID, AccountingDate: "2026-03-05", Reason: "Example mistake"})
	if page := f.inbox(t, f.s, ""); page.Summary.Unrecorded != 1 {
		t.Fatalf("a reversed recording still counts: %+v", page.Summary)
	}
}

func TestPendingRemovedAndUnlinkedActivityIsNotRecorded(t *testing.T) {
	f := newBankFixture(t)
	pending := f.tx("acc-checking", "p1", "2026-03-02", -100, "Pending example")
	pending.Pending = true
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{pending, f.tx("acc-checking", "r1", "2026-03-02", -200, "Removed example")}, nil, "r1")
	page := f.inbox(t, f.s, "all")
	lines := func(amount string) []ledger.PostLine {
		return []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: amount}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: amount}}
	}
	_, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: "p", TransactionID: byDescription(page, "Pending example").ID, Description: "x", Lines: lines("100")}))
	bankRefused(t, err, "pending")
	_, err = f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: "r", TransactionID: byDescription(page, "Removed example").ID, Description: "x", Lines: lines("200")}))
	bankRefused(t, err, "removed")
	// A bank account can only link to a Book account of the matching class.
	err = f.owner.LinkBankAccount(f.db.Context, f.book.ID, f.actor, f.accounts["acc-checking"], f.card.ID)
	bankRefused(t, err, "cash account is linked to an asset")
	err = f.owner.LinkBankAccount(f.db.Context, f.book.ID, f.actor, f.accounts["acc-card"], f.office.ID)
	bankRefused(t, err, "card account to a liability")
}

func TestTransfersBetweenOwnAccountsAreSuggestedAndRecordedOnce(t *testing.T) {
	f := newBankFixture(t)
	// A card payment: money leaves checking (asset falls) and the card's
	// balance owed falls by the same amount.
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "pay-out", "2026-03-10", -25000, "Example card payment"),
		f.tx("acc-card", "pay-in", "2026-03-11", -25000, "Payment received"),
		f.tx("acc-checking", "fee", "2026-03-11", -500, "Example bank fee"),
	}, nil)
	var suggestions []ledger.BankSuggestion
	json.Unmarshal(f.db.Call(t, f.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 1 || suggestions[0].Kind != "transfer" || suggestions[0].AmountMinor != "25000" {
		t.Fatalf("suggestions = %+v", suggestions)
	}
	var result ledger.BankLinkResult
	json.Unmarshal(f.db.Call(t, f.s, "record_bank_transfer", ledger.RecordBankTransfer{RequestKey: "transfer", TransactionIDs: suggestions[0].TransactionIDs}), &result)
	if result.Entry == nil || len(result.Links) != 2 || result.Entry.AccountingDate != "2026-03-11" {
		t.Fatalf("transfer = %+v", result)
	}
	debits := map[string]string{}
	for _, l := range result.Entry.Lines {
		debits[l.AccountID] = l.Side
	}
	if debits[f.card.ID] != "debit" || debits[f.checking.ID] != "credit" {
		t.Fatalf("a card payment debits the card and credits checking: %+v", result.Entry.Lines)
	}
	page := f.inbox(t, f.s, "")
	if page.Summary.Unrecorded != 1 || page.Items[0].Description != "Example bank fee" {
		t.Fatalf("after transfer: %+v", page.Summary)
	}
	// Two movements in the same direction are never a transfer.
	fee := page.Items[0]
	_, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_transfer", value(ledger.RecordBankTransfer{RequestKey: "bad", TransactionIDs: []string{fee.ID, suggestions[0].TransactionIDs[0]}}))
	bankRefused(t, err, "already recorded")
	// An existing entry is suggested for the fee once it's posted.
	f.db.Call(t, f.s, "post_journal", ledger.PostJournal{RequestKey: "fee", EconomicDate: "2026-03-12", AccountingDate: "2026-03-12", Description: "Bank fee",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "500"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "500"}}})
	json.Unmarshal(f.db.Call(t, f.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 1 || suggestions[0].Kind != "existing_entry" || suggestions[0].TransactionIDs[0] != fee.ID {
		t.Fatalf("existing entry suggestions = %+v", suggestions)
	}
}

func TestAmbiguousTransfersAreLeftForAPerson(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "a", "2026-03-10", -1000, "Out one"),
		f.tx("acc-checking", "b", "2026-03-10", -1000, "Out two"),
		f.tx("acc-card", "c", "2026-03-11", -1000, "In"),
	}, nil)
	var suggestions []ledger.BankSuggestion
	json.Unmarshal(f.db.Call(t, f.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 0 {
		t.Fatalf("an ambiguous pairing was suggested: %+v", suggestions)
	}
}

func TestBankActivityFollowsConnectionLevels(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "t1", "2026-03-02", -4500, "Example Office Supply")}, nil)
	reader := connectionScope(t, f.db, f.book.ID, "read")
	proposer := connectionScope(t, f.db, f.book.ID, "propose")
	item := byDescription(f.inbox(t, reader, ""), "Example Office Supply")
	if item.ID == "" {
		t.Fatal("a read connection can't see the inbox")
	}
	q := ledger.RecordBankActivity{RequestKey: "record", TransactionID: item.ID, Description: "Office supplies",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4500"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "4500"}}}
	for _, s := range []ledger.Scope{reader, proposer} {
		if _, err := f.db.Store.Call(f.db.Context, s, "record_bank_activity", value(q)); !errors.Is(err, ledger.ErrDenied) {
			t.Fatalf("%s connection recorded directly: %v", s.Access, err)
		}
	}
	// A proposing agent prepares it for approval; nothing is recorded until
	// it is approved and run.
	p := prepareOperation(t, f.db, proposer, "propose-record", "record_bank_activity", q)
	if p.Preview.BankChange == nil || p.Preview.BankChange.Change != "record" || len(p.Preview.Lines) != 2 || p.Preview.EconomicDate != "2026-03-02" {
		t.Fatalf("preview = %+v", p.Preview)
	}
	if page := f.inbox(t, f.s, ""); page.Summary.Unrecorded != 1 {
		t.Fatal("preparing a proposal recorded the activity")
	}
	approveOperation(t, f.db, proposer, p, f.s)
	executeOperation(t, f.db, f.s, p)
	if page := f.inbox(t, f.s, ""); page.Summary.Unrecorded != 0 {
		t.Fatal("the approved proposal didn't record the activity")
	}
	// Agents never see provider keys or tokens, sealed or not.
	var visible int
	tx, err := f.db.Runtime.Begin(f.db.Context)
	succeeded(t, err)
	defer tx.Rollback(f.db.Context)
	_, err = tx.Exec(f.db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`,
		reader.TenantID, reader.BookID, reader.ActorID, reader.ConnectionID)
	succeeded(t, err)
	succeeded(t, tx.QueryRow(f.db.Context, "SELECT count(*) FROM public.bank_provider_settings").Scan(&visible))
	if visible != 0 {
		t.Fatal("an agent can read provider settings")
	}
}

func TestOnlyTheSyncWritesActivityAndOnlyForItsLogin(t *testing.T) {
	f := newBankFixture(t)
	// An agent can't write bank activity.
	tx, err := f.db.Runtime.Begin(f.db.Context)
	succeeded(t, err)
	_, err = tx.Exec(f.db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`,
		f.s.TenantID, f.s.BookID, f.s.ActorID, f.s.ConnectionID)
	succeeded(t, err)
	_, err = tx.Exec(f.db.Context, `INSERT INTO public.bank_transactions(tenant_id,book_id,id,bank_account_id,provider_transaction_id,occurred_on,amount_minor,currency,scale,pending)
 VALUES($1,$2,$3,$4,'forged','2026-03-01',100,$5,$6,false)`, f.s.TenantID, f.s.BookID, ledger.ID(), f.accounts["acc-checking"], f.book.Currency, f.book.Scale)
	tx.Rollback(f.db.Context)
	if err == nil {
		t.Fatal("an agent wrote bank activity")
	}
	// The sync for another login can't write this login's accounts.
	other, err := f.owner.AddBankConnection(f.db.Context, f.book.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "sandbox", ProviderItemID: "item-other", TokenSealed: sealedForTest, KeyVersion: 1})
	succeeded(t, err)
	wrong := ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: other}
	_, err = f.db.Store.ApplyBankSync(f.db.Context, wrong, ledger.BankSyncBatch{NextCursor: "x", Modified: []ledger.BankSyncTransaction{f.tx("acc-checking", "t9", "2026-03-01", 1, "x")}})
	if err != nil {
		t.Fatal(err)
	}
	if page := f.inbox(t, f.s, "all"); len(page.Items) != 0 {
		t.Fatal("one login's sync wrote another login's activity")
	}
	// Disconnecting forgets the token and keeps the history.
	gone, err := f.owner.DisconnectBankConnection(f.db.Context, f.book.ID, f.actor, f.target.ConnectionID)
	succeeded(t, err)
	if gone.Provider != "plaid" || gone.ProviderItemID == "" {
		t.Fatalf("disconnected = %+v", gone)
	}
	// The token stays sealed until the provider confirms the access ended.
	release, err := f.db.Store.BankConnectionsToRelease(f.db.Context, 10)
	succeeded(t, err)
	if len(release) != 1 || release[0].ConnectionID != f.target.ConnectionID {
		t.Fatalf("to release = %+v", release)
	}
	succeeded(t, f.db.Store.BankAccessEnded(f.db.Context, f.target))
	var token []byte
	succeeded(t, f.db.Admin.QueryRow(f.db.Context, "SELECT token_sealed FROM public.bank_connections WHERE id=$1", f.target.ConnectionID).Scan(&token))
	if token != nil {
		t.Fatal("a released login kept its token")
	}
	if _, err := f.db.Store.ApplyBankSync(f.db.Context, f.target, ledger.BankSyncBatch{FromCursor: "c1", NextCursor: "c2"}); err == nil {
		t.Fatal("a disconnected login synced")
	}
}

func TestProviderNotificationsFindOnlyTheirLogin(t *testing.T) {
	f := newBankFixture(t)
	var item string
	succeeded(t, f.db.Admin.QueryRow(f.db.Context, "SELECT provider_item_id FROM public.bank_connections WHERE id=$1", f.target.ConnectionID).Scan(&item))
	got, ok, err := f.db.Store.FindBankConnectionForItem(f.db.Context, "plaid", item)
	succeeded(t, err)
	if !ok || got != f.target {
		t.Fatalf("found %+v %v", got, ok)
	}
	if _, ok, _ := f.db.Store.FindBankConnectionForItem(f.db.Context, "plaid", "unknown-item"); ok {
		t.Fatal("an unknown item found a login")
	}
	succeeded(t, f.db.Store.RequestBankSync(f.db.Context, got))
	due, err := f.db.Store.BankConnectionsDue(f.db.Context, 0, 10)
	succeeded(t, err)
	if len(due) != 1 || due[0] != f.target {
		t.Fatalf("due = %+v", due)
	}
}

// A server operator may hold a login's access itself: the Book stores no
// token, open's own sync leaves it alone, the operator syncs it through the
// same inbox, and it can restart on a new provider login keeping the Book's
// accounts and records.
func TestOperatorHeldLoginsSyncThroughTheSameInbox(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	if _, err := f.owner.AddBankConnection(ctx, f.book.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "production",
		ProviderItemID: "item-shared", OperatorHeld: true, TokenSealed: sealedForTest, KeyVersion: 1}); err == nil {
		t.Fatal("an operator login stored a token")
	}
	id, err := f.owner.AddBankConnection(ctx, f.book.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "production", ProviderItemID: "item-shared", OperatorHeld: true})
	succeeded(t, err)
	// The same login can feed a second Book.
	other, _ := f.db.Book(t, "Example second Book")
	succeeded(t, f.db.EnsureBookOwner(ctx, other.ID))
	if _, err := f.owner.AddBankConnection(ctx, other.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "production", ProviderItemID: "item-shared", OperatorHeld: true}); err != nil {
		t.Fatal("one login can't feed two Books", err)
	}
	target := ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: id}
	// Open's sync loop and notifications don't see operator logins.
	if _, found, _ := f.db.Store.FindBankConnectionForItem(ctx, "plaid", "item-shared"); found {
		t.Fatal("a notification routed to an operator login")
	}
	f.sync(t, "c1", "c2", nil, nil) // completes the Book's own login so it isn't due
	due, err := f.db.Store.BankConnectionsDue(ctx, 0, 50)
	succeeded(t, err)
	for _, d := range due {
		if d.ConnectionID == id {
			t.Fatal("open's sync loop picked up an operator login")
		}
	}
	state, err := f.db.Store.BankSyncState(ctx, target)
	if err != nil || state.Access != "operator" || state.TokenSealed != nil {
		t.Fatalf("state = %+v, %v", state, err)
	}
	// The operator syncs it like any login.
	_, err = f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{NextCursor: "o1", Complete: true,
		Accounts: []ledger.BankSyncAccount{{ProviderAccountID: "op-checking", Name: "Operator Checking", Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale}},
		Added:    []ledger.BankSyncTransaction{f.tx("op-checking", "o-t1", "2026-03-09", -700, "Example Bakery")}})
	succeeded(t, err)
	if byDescription(f.inbox(t, f.s, "all"), "Example Bakery").ID == "" {
		t.Fatal("operator activity missing from the inbox")
	}
	// A paused login restarts on a new provider login from the start.
	succeeded(t, f.db.Store.RecordBankSyncProblem(ctx, target, "Paused after a long time away. Reconnect to resume.", true))
	succeeded(t, f.db.Store.RestartBankConnection(ctx, target, "item-shared-2"))
	state, _ = f.db.Store.BankSyncState(ctx, target)
	if state.ProviderItemID != "item-shared-2" || state.Cursor != "" || state.Status != "active" {
		t.Fatalf("restarted = %+v", state)
	}
	// A Book's own login can't be moved to another provider login.
	if err := f.db.Store.RestartBankConnection(ctx, f.target, "item-x"); err == nil {
		t.Fatal("a Book's own login restarted")
	}
}

func (f *bankFixture) ownerChange(t *testing.T, method string, args any) (json.RawMessage, error) {
	t.Helper()
	return f.owner.AccountChange(f.db.Context, f.book.ID, f.actor, method, value(args))
}

// Bank management is the owner's: taken directly, or prepared by an agent
// for approval. An agent never takes it on its own.
func TestBankManagementIsTheOwnersDecision(t *testing.T) {
	f := newBankFixture(t)
	q := ledger.PauseBankConnection{RequestKey: "pause", ConnectionID: f.target.ConnectionID, Reason: "Away for a while"}
	_, err := f.db.Store.Call(f.db.Context, f.s, "pause_bank_connection", value(q))
	bankRefused(t, err, "only the owner")
	proposer := connectionScope(t, f.db, f.book.ID, "propose")
	p := prepareOperation(t, f.db, proposer, "prepare-pause", "pause_bank_connection", q)
	if p.Preview.BankChange == nil || p.Preview.BankChange.Change != "pause" || p.Preview.Description != "Pause a bank's sync" {
		t.Fatalf("preview = %+v", p.Preview)
	}
	if _, err := f.ownerChange(t, "pause_bank_connection", q); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Store.ApplyBankSync(f.db.Context, f.target, ledger.BankSyncBatch{FromCursor: "c1", NextCursor: "c2"}); !errors.Is(err, ledger.ErrBankPaused) {
		t.Fatalf("a paused login synced: %v", err)
	}
	// Owner keys: the provider's access stays while paused.
	if list, _ := f.db.Store.BankConnectionsToRelease(f.db.Context, 10); len(list) != 0 {
		t.Fatal("pausing an owner-keys login ended its access")
	}
	if _, err := f.ownerChange(t, "resume_bank_connection", ledger.ResumeBankConnection{RequestKey: "resume", ConnectionID: f.target.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	f.sync(t, "c1", "c2", nil, nil)
	// Unlinking a bank account goes through the same operation.
	if _, err := f.ownerChange(t, "link_bank_account", ledger.LinkBankAccountChange{RequestKey: "unlink-acct", BankAccountID: f.accounts["acc-card"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ownerChange(t, "link_bank_account", ledger.LinkBankAccountChange{RequestKey: "bad-acct", BankAccountID: f.accounts["acc-card"], AccountID: f.office.ID}); err == nil {
		t.Fatal("a card account linked to an expense")
	}
}

// Undo is a reversing entry, and every link says why it exists.
func TestEntriesSayWhyTheyAreThereAndUndoIsAReversal(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "t1", "2026-03-02", -4500, "Example Office Supply")}, nil)
	item := byDescription(f.inbox(t, f.s, ""), "Example Office Supply")
	var result ledger.BankLinkResult
	json.Unmarshal(f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "rec", TransactionID: item.ID, Description: "Office supplies",
		Reason: "Monthly paper order, by its merchant and amount",
		Lines:  []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4500"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "4500"}}}), &result)
	var origin ledger.EntryOrigin
	json.Unmarshal(f.db.Call(t, f.s, "get_entry_origin", ledger.EntryQuery{EntryID: result.Entry.ID}), &origin)
	if len(origin.Bank) != 1 || origin.Bank[0].TransactionID != item.ID || origin.Bank[0].Basis != "judgement" || origin.Bank[0].Reason == "" {
		t.Fatalf("origin = %+v", origin)
	}
	if owner, err := f.owner.EntryOrigin(f.db.Context, f.book.ID, result.Entry.ID); err != nil || len(owner.Bank) != 1 {
		t.Fatalf("owner origin = %+v, %v", owner, err)
	}
	// Undo is a reversal; the activity returns to the inbox. (The owner's
	// own undo runs through a signed-in session in the server tests.)
	f.db.Call(t, f.s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: result.Entry.ID, AccountingDate: "2026-03-05", Reason: "Recorded to the wrong account"})
	json.Unmarshal(f.db.Call(t, f.s, "get_entry_origin", ledger.EntryQuery{EntryID: result.Entry.ID}), &origin)
	if origin.ReversedBy == "" || f.inbox(t, f.s, "").Summary.Unrecorded != 1 {
		t.Fatalf("undo = %+v", origin)
	}
}

// A login that restarts on a new provider login is reconciled with what
// the Book holds: nothing doubled, nothing late lost, ambiguity left to a
// person.
func TestReconnectingKeepsRecordsAndFindsLateActivity(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	id, err := f.owner.AddBankConnection(ctx, f.book.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "production", ProviderItemID: "item-op", OperatorHeld: true})
	succeeded(t, err)
	op := ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: id}
	savings := f.db.Account(t, f.s, "1150", "asset")
	_, err = f.db.Store.ApplyBankSync(ctx, op, ledger.BankSyncBatch{NextCursor: "o1", Complete: true,
		Accounts: []ledger.BankSyncAccount{{ProviderAccountID: "old-acc", Name: "Savings", Mask: "7777", Subtype: "savings", Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale}},
		Added: []ledger.BankSyncTransaction{f.tx("old-acc", "old-1", "2026-03-01", 1000, "Interest"),
			f.tx("old-acc", "old-2", "2026-03-04", -200, "Fee A"), f.tx("old-acc", "old-3", "2026-03-04", -200, "Fee B"),
			{ProviderAccountID: "old-acc", ProviderTransactionID: "old-pending", Date: "2026-03-05", AmountMinor: -800, Currency: f.book.Currency, Scale: f.book.Scale, Description: "Pending charge", Pending: true}}})
	succeeded(t, err)
	views, _ := f.owner.BankConnections(ctx, f.book.ID)
	var savingsBank string
	for _, v := range views {
		if v.ID == id {
			savingsBank = v.Accounts[0].ID
		}
	}
	succeeded(t, f.owner.LinkBankAccount(ctx, f.book.ID, f.actor, savingsBank, savings.ID))
	interest := byDescription(f.inbox(t, f.s, ""), "Interest")
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "interest", TransactionID: interest.ID, Description: "Interest",
		Lines: []ledger.PostLine{{AccountID: savings.ID, Side: "debit", AmountMinor: "1000"}, {AccountID: f.office.ID, Side: "credit", AmountMinor: "1000"}}})
	// Pause automatically, then reconnect on a new provider login.
	succeeded(t, f.db.Store.PauseBankConnection(ctx, op, "Paused after a long time away."))
	succeeded(t, f.db.Store.RestartBankConnection(ctx, op, "item-op-2"))
	_, err = f.db.Store.ApplyBankSync(ctx, op, ledger.BankSyncBatch{NextCursor: "n1", Complete: true,
		Accounts: []ledger.BankSyncAccount{{ProviderAccountID: "new-acc", Name: "Savings", Mask: "7777", Subtype: "savings", Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale}},
		Added: []ledger.BankSyncTransaction{f.tx("new-acc", "new-1", "2026-03-01", 1000, "Interest"),
			f.tx("new-acc", "new-2", "2026-03-04", -200, "Fee"),
			f.tx("new-acc", "late", "2026-02-10", -3000, "Late posting")}})
	succeeded(t, err)
	page := f.inbox(t, f.s, "all")
	if v := byDescription(page, "Interest"); v.Status != "recorded" {
		t.Fatalf("the recorded item lost its record: %+v", v)
	}
	if byDescription(page, "Late posting").ID == "" {
		t.Fatal("late activity was dropped")
	}
	if v := byDescription(page, "Pending charge"); v.Status != "removed" {
		t.Fatalf("the old login's pending item still shows: %+v", v)
	}
	fee := byDescription(page, "Fee")
	if fee.ID == "" || fee.PossibleRepeatOf == "" {
		t.Fatalf("an ambiguous repeat wasn't flagged: %+v", fee)
	}
	views, _ = f.owner.BankConnections(ctx, f.book.ID)
	for _, v := range views {
		if v.ID == id && (len(v.Accounts) != 1 || v.Accounts[0].AccountID != savings.ID) {
			t.Fatalf("the account wasn't kept: %+v", v.Accounts)
		}
	}
	// A later change under the new ID reaches the original item.
	_, err = f.db.Store.ApplyBankSync(ctx, op, ledger.BankSyncBatch{FromCursor: "n1", NextCursor: "n2", Complete: true, Removed: []string{"new-1"}})
	succeeded(t, err)
	if v := byDescription(f.inbox(t, f.s, "all"), "Interest"); v.Status != "recorded" {
		// recorded stays recorded; the removal is visible in the summary
		t.Fatalf("status = %s", v.Status)
	}
	if f.inbox(t, f.s, "all").Summary.RecordedChanged != 1 {
		t.Fatal("a removal under the new ID didn't reach the original item")
	}
	// The repeat is settled by a recorded decision, never dropped silently.
	f.db.Call(t, f.s, "mark_bank_duplicate", ledger.MarkBankDuplicate{RequestKey: "repeat", TransactionID: fee.ID, CanonicalTransactionID: fee.PossibleRepeatOf, Reason: "Same fee reported again after reconnecting"})
	if v := byDescription(f.inbox(t, f.s, "all"), "Fee"); v.Status != "repeat" {
		t.Fatalf("repeat status = %s", v.Status)
	}
}

// Statement lines feed the same inbox as bank activity. A line and the same
// transaction from the bank are one item, whichever arrives first; an import
// records nothing.
func TestStatementLinesJoinTheInboxWithoutDoubles(t *testing.T) {
	f := newBankFixture(t)
	// The bank reports the supplies purchase first.
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "b-supplies", "2026-04-02", -4500, "EXAMPLE OFFICE SUPPLY")}, nil)
	doc := statementDocument(t, f.db, f.s, "april")
	st := importStatement(t, f.db, f.s, ledger.ImportStatement{RequestKey: "april", AccountID: f.checking.ID, DocumentID: doc,
		PeriodStart: "2026-04-01", PeriodEnd: "2026-04-30", OpeningMinor: "0", ClosingMinor: "113500", Rows: []ledger.StatementRowInput{
			row(1, "2026-04-03", "-4500", "Office supply store"),
			row(2, "2026-04-10", "120000", "Example Customer deposit"),
			row(3, "2026-04-15", "-2000", "Example parking"),
		}})
	page := f.inbox(t, f.s, "all")
	if len(page.Items) != 3 {
		t.Fatalf("items = %d, want 3 (one joined)", len(page.Items))
	}
	joined := byDescription(page, "EXAMPLE OFFICE SUPPLY")
	if joined.Source != "bank" || joined.StatementRowID == "" || joined.StatementID != st.ID || joined.DocumentID != doc {
		t.Fatalf("joined = %+v", joined)
	}
	deposit := byDescription(page, "Example Customer deposit")
	if deposit.Source != "statement" || deposit.AccountID != f.checking.ID || deposit.Status != "unrecorded" || deposit.DocumentID != doc {
		t.Fatalf("statement item = %+v", deposit)
	}
	if page.Summary.Unrecorded != 3 {
		t.Fatal("an import recorded something")
	}
	// The bank reports the deposit later: it joins the statement's item.
	f.sync(t, "c2", "c3", []ledger.BankSyncTransaction{f.tx("acc-checking", "b-deposit", "2026-04-09", 120000, "ACH DEPOSIT")}, nil)
	page = f.inbox(t, f.s, "all")
	if len(page.Items) != 3 {
		t.Fatalf("a bank report doubled a statement line: %d items", len(page.Items))
	}
	// A statement item is prepared for review like any other: its entry
	// posts to the statement's Book account.
	prepared, err := f.db.Store.Call(f.db.Context, f.s, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "parking-review", Operation: "record_bank_activity",
		Arguments: value(ledger.RecordBankActivity{RequestKey: "parking-review", TransactionID: byDescription(page, "Example parking").ID, Description: "Parking", OtherAccountID: f.office.ID})}))
	succeeded(t, err)
	// Its preview rests on both accounts, so approval rechecks them.
	var proposal ledger.OperationProposal
	if json.Unmarshal(prepared, &proposal) != nil {
		t.Fatalf("prepared = %s", prepared)
	}
	effects := map[string]string{}
	for _, a := range proposal.Preview.Accounts {
		effects[a.AccountID] = a.ChangeMinor
	}
	if len(effects) != 2 || effects[f.checking.ID] != "-2000" || effects[f.office.ID] != "2000" {
		t.Fatalf("preview accounts = %+v", proposal.Preview.Accounts)
	}
	// A statement item is recorded like any other, through the same tool.
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "parking", TransactionID: byDescription(page, "Example parking").ID, Description: "Parking",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "2000"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "2000"}}})
	// Two equal bank charges and one matching statement line: the line is
	// added, marked as possibly repeating one, never merged by guess.
	f.sync(t, "c3", "c4", []ledger.BankSyncTransaction{f.tx("acc-checking", "fee-1", "2026-05-02", -300, "FEE"), f.tx("acc-checking", "fee-2", "2026-05-02", -300, "FEE")}, nil)
	doc2 := statementDocument(t, f.db, f.s, "may")
	importStatement(t, f.db, f.s, ledger.ImportStatement{RequestKey: "may", AccountID: f.checking.ID, DocumentID: doc2,
		PeriodStart: "2026-05-01", PeriodEnd: "2026-05-31", OpeningMinor: "113500", ClosingMinor: "113200", Rows: []ledger.StatementRowInput{row(1, "2026-05-02", "-300", "Monthly fee")}})
	fee := byDescription(f.inbox(t, f.s, "all"), "Monthly fee")
	if fee.Source != "statement" || fee.PossibleRepeatOf == "" {
		t.Fatalf("ambiguous line = %+v", fee)
	}
}

// A proposing connection the owner granted it records routine activity only
// by the owner's rules, as a transfer, or as a match; everything else stays
// a proposal. Only the owner makes or changes rules.
func TestRecordingWithinTheOwnersRules(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "s1", Date: "2026-03-01", AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM 0301", Counterparty: "Example Stream"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "s2", Date: "2026-04-01", AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM 0401", Counterparty: "Example Stream"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "s3", Date: "2026-04-03", AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM 0403", Counterparty: "Example Stream"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "s4", Date: "2026-05-01", AmountMinor: -9999, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM 0501", Counterparty: "Example Stream"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "o1", Date: "2026-05-02", AmountMinor: -700, Currency: f.book.Currency, Scale: f.book.Scale, Description: "Example Bakery", Counterparty: "Example Bakery"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "r1", Date: "2026-06-15", AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM #1187 06/15"},
		{ProviderAccountID: "acc-checking", ProviderTransactionID: "c1", Date: "2026-07-01", AmountMinor: 1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM CREDIT", Counterparty: "Example Stream"},
	}, nil)
	page := f.inbox(t, f.s, "all")
	find := func(description string) string { return byDescription(page, description).ID }
	// The owner's decision, recorded once.
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "first", TransactionID: find("EXAMPLE STREAM 0301"), Description: "Streaming subscription",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "1599"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "1599"}}})
	rule, err := f.owner.CreateRecordingRule(ctx, f.book.ID, f.actor, ledger.RuleFromActivity{TransactionID: find("EXAMPLE STREAM 0301"), MinMinor: "1000", MaxMinor: "2000", MinDaysBetween: 20, Description: "Example Stream subscription"})
	succeeded(t, err)
	if rule.Direction != "out" || rule.Counterparty != "example stream" || rule.OtherAccountID != f.office.ID || !rule.Active {
		t.Fatalf("rule = %+v", rule)
	}
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	byRule := func(key, item string) error {
		_, err := f.db.Store.Call(ctx, agent, "record_by_rule", value(ledger.RecordByRule{RequestKey: key, TransactionID: item, RuleID: rule.ID}))
		return err
	}
	// Without the owner's grant, nothing is recorded.
	if err := byRule("no-grant", find("EXAMPLE STREAM 0401")); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("recorded without a grant: %v", err)
	}
	succeeded(t, f.owner.GrantRuleRecording(ctx, f.book.ID, f.actor, agent.ConnectionID, true))
	succeeded(t, byRule("april", find("EXAMPLE STREAM 0401")))
	var origin ledger.EntryOrigin
	recorded := byDescription(f.inbox(t, f.s, "recorded"), "EXAMPLE STREAM 0401")
	json.Unmarshal(f.db.Call(t, agent, "get_entry_origin", ledger.EntryQuery{EntryID: recorded.Link.EntryID}), &origin)
	if len(origin.Bank) != 1 || origin.Bank[0].Basis != "rule" || origin.Bank[0].RuleID != rule.ID || !strings.Contains(origin.Bank[0].Reason, "Example Stream subscription") {
		t.Fatalf("origin = %+v", origin)
	}
	// Cadence, amount range and counterparty are the rule's.
	bankRefused(t, byRule("too-soon", find("EXAMPLE STREAM 0403")), "too recently")
	// A refusal names each condition the item fails.
	bankRefused(t, byRule("too-big", find("EXAMPLE STREAM 0501")), "isn't covered by the rule. Amount: the rule covers 10.00 to 20.00 and this item is 99.99.")
	bankRefused(t, byRule("other", find("Example Bakery")), "isn't covered by the rule. Counterparty: the rule's is \u201cexample stream\u201d and this item's is \u201cexample bakery\u201d; amount: the rule covers 10.00 to 20.00 and this item is 7.00.")
	bankRefused(t, byRule("credit", find("EXAMPLE STREAM CREDIT")), "Direction: the rule is for out (the account's balance falls: a payment from a bank account, a payment or refund on a card), and this item is in.")
	// A charge whose text carries a reference and a date has the same key.
	succeeded(t, byRule("reference", find("EXAMPLE STREAM #1187 06/15")))
	// Anything else from the granted connection stays a proposal.
	if _, err := f.db.Store.Call(ctx, agent, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: "free", TransactionID: find("Example Bakery"), Description: "Bakery",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "700"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "700"}}})); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("a granted connection recorded outside the rules: %v", err)
	}
	if _, err := f.db.Store.Call(ctx, agent, "post_journal", value(ledger.PostJournal{RequestKey: "free-post", EconomicDate: "2026-05-02", AccountingDate: "2026-05-02", Description: "x",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "1"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "1"}}})); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("a granted connection posted freely: %v", err)
	}
	entry := f.db.Call(t, f.s, "post_journal", ledger.PostJournal{RequestKey: "bakery", EconomicDate: "2026-05-02", AccountingDate: "2026-05-02", Description: "Bakery",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "700"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "700"}}})
	var e ledger.Entry
	json.Unmarshal(entry, &e)
	if _, err := f.db.Store.Call(ctx, agent, "link_bank_activity", value(ledger.LinkBankActivity{RequestKey: "judged", TransactionID: find("Example Bakery"), EntryID: e.ID})); err == nil {
		t.Fatal("a granted connection linked by its own judgement")
	}
	if _, err := f.db.Store.Call(ctx, agent, "link_bank_activity", value(ledger.LinkBankActivity{RequestKey: "matched", TransactionID: find("Example Bakery"), EntryID: e.ID, Basis: "existing_entry"})); err != nil {
		t.Fatalf("a match was refused: %v", err)
	}
	// Agents never make or change rules.
	tx, err := f.db.Runtime.Begin(ctx)
	succeeded(t, err)
	_, err = tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`,
		f.s.TenantID, f.s.BookID, f.s.ActorID, f.s.ConnectionID)
	succeeded(t, err)
	_, err = tx.Exec(ctx, "UPDATE public.recording_rules SET max_minor=999999 WHERE id=$1", rule.ID)
	tx.Rollback(ctx)
	if err == nil {
		var max int64
		f.db.Admin.QueryRow(ctx, "SELECT max_minor FROM public.recording_rules WHERE id=$1", rule.ID).Scan(&max)
		if max != 2000 {
			t.Fatal("an agent widened a rule")
		}
	}
	// Turned off, the rule records nothing.
	succeeded(t, f.owner.SetRecordingRule(ctx, f.book.ID, f.actor, rule.ID, false))
	f.sync(t, "c2", "c3", []ledger.BankSyncTransaction{{ProviderAccountID: "acc-checking", ProviderTransactionID: "s6", Date: "2026-06-01", AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: "EXAMPLE STREAM 0601", Counterparty: "Example Stream"}}, nil)
	page = f.inbox(t, f.s, "all")
	bankRefused(t, byRule("off", find("EXAMPLE STREAM 0601")), "turned off")
}

func TestMalformedBankArgumentsAreExplained(t *testing.T) {
	f := newBankFixture(t)
	_, err := f.db.Store.Call(f.db.Context, f.s, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "p", Operation: "record_bank_activity",
		Arguments: value(map[string]any{"request_key": "r", "transaction_id": ledger.ID(), "other_account_id": ledger.ID()})}))
	bankRefused(t, err, "needs description")
	_, err = f.db.Store.Call(f.db.Context, f.s, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "p2", Operation: "record_bank_activity",
		Arguments: value(map[string]any{"request_key": "r", "transaction_id": ledger.ID(), "description": "x"})}))
	bankRefused(t, err, "either other_account_id")
}

// The whole amount to one account: Vult writes both lines, sides by class.
func TestRecordingBankActivityToOneAccount(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "fee", "2026-04-02", -2500, "Example Office Supply")}, nil)
	item := byDescription(f.inbox(t, f.s, "all"), "Example Office Supply")
	if _, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: "self", TransactionID: item.ID, Description: "x", OtherAccountID: f.checking.ID})); err == nil {
		t.Fatal("recorded against its own account")
	}
	var out ledger.BankLinkResult
	json.Unmarshal(f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "one", TransactionID: item.ID, Description: "Office supplies", OtherAccountID: f.office.ID}), &out)
	if out.Entry == nil || len(out.Entry.Lines) != 2 {
		t.Fatalf("entry = %+v", out.Entry)
	}
	for _, l := range out.Entry.Lines {
		if l.AccountID == f.office.ID && l.Side != "debit" || l.AccountID == f.checking.ID && l.Side != "credit" || l.AmountMinor != "2500" {
			t.Fatalf("line = %+v", l)
		}
	}
}

// A proposal that moves accounts against their usual direction carries a
// plain warning, judged by account type alone; it isn't refused, because a
// refund can be right.
func TestProposalsWarnWhenMoneyGoesAgainstAccountTypes(t *testing.T) {
	f := newBankFixture(t)
	_, _, byCode := chartBookAccounts(t, f)
	sales := byCode["4100"]
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "out", "2026-04-02", -2500, "Example Customer refund"),
		f.tx("acc-checking", "in", "2026-04-03", 4000, "Example Supplier credit")}, nil)
	page := f.inbox(t, f.s, "all")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	prepare := func(key, item, other string) ledger.OperationProposal {
		t.Helper()
		var p ledger.OperationProposal
		json.Unmarshal(f.db.Call(t, agent, "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: "record_bank_activity",
			Arguments: value(ledger.RecordBankActivity{RequestKey: key + "-r", TransactionID: byDescription(page, item).ID, Description: "x", OtherAccountID: other})}), &p)
		return p
	}
	if w := prepare("out", "Example Customer refund", sales.ID).Preview.Warnings; len(w) != 1 || !strings.HasPrefix(w[0], "Money out recorded as income.") {
		t.Fatalf("warnings = %v", w)
	}
	if w := prepare("in", "Example Supplier credit", f.office.ID).Preview.Warnings; len(w) != 1 || !strings.HasPrefix(w[0], "Money in recorded as an expense.") {
		t.Fatalf("warnings = %v", w)
	}
	if w := prepare("usual", "Example Supplier credit", sales.ID).Preview.Warnings; len(w) != 0 {
		t.Fatalf("an ordinary entry was flagged: %v", w)
	}
}

// After the owner disconnects a bank and connects it again, the same bank
// accounts come back with their links and history: nothing arrives twice.
// An account no new login takes over frees its Book account for another.
func TestReconnectingKeepsTheBookAccountsAndHistory(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "a", "2026-04-01", -1200, "Example Cafe"),
		f.tx("acc-checking", "b", "2026-04-02", -3400, "Example Hardware"), f.tx("acc-card", "c", "2026-04-03", 990, "Example Books")}, nil)
	page := f.inbox(t, f.s, "all")
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "cafe", TransactionID: byDescription(page, "Example Cafe").ID, Description: "Coffee", OtherAccountID: f.office.ID})
	_, err := f.owner.AccountChange(ctx, f.book.ID, f.actor, "disconnect_bank", value(ledger.DisconnectBank{RequestKey: "off", ConnectionID: f.target.ConnectionID, Reason: "Changing banks"}))
	succeeded(t, err)
	// A new login for the same bank: the checking account matches by kind,
	// currency, mask and type; the card comes back with a new number.
	login, err := f.owner.AddBankConnection(ctx, f.book.ID, f.actor, ledger.NewBankConnection{Provider: "plaid", Environment: "sandbox",
		ProviderItemID: "item-" + ledger.ID(), InstitutionName: "Example Bank", TokenSealed: sealedForTest, KeyVersion: 1})
	succeeded(t, err)
	again := ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: login}
	_, err = f.db.Store.ApplyBankSync(ctx, again, ledger.BankSyncBatch{NextCursor: "n1", Complete: true, Accounts: []ledger.BankSyncAccount{
		{ProviderAccountID: "new-checking", Name: "Example Checking", Mask: "0001", Type: "depository", Subtype: "checking", Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale},
		{ProviderAccountID: "new-card", Name: "Example Card", Mask: "0003", Type: "credit", Subtype: "credit card", Kind: "card", Currency: f.book.Currency, Scale: f.book.Scale},
	}, Added: []ledger.BankSyncTransaction{f.tx("new-checking", "n-a", "2026-04-01", -1200, "Example Cafe"), f.tx("new-checking", "n-b", "2026-04-02", -3400, "Example Hardware"),
		f.tx("new-checking", "n-d", "2026-04-05", -800, "Example Parking")}})
	succeeded(t, err)
	views, err := f.owner.BankConnections(ctx, f.book.ID)
	succeeded(t, err)
	var checking, card ledger.BankAccount
	for _, v := range views {
		if v.ID != login {
			continue
		}
		for _, a := range v.Accounts {
			if a.Kind == "cash" {
				checking = a
			} else {
				card = a
			}
		}
	}
	if checking.AccountID != f.checking.ID || !checking.Kept || card.AccountID != "" || card.Kept {
		t.Fatalf("accounts = %+v %+v", checking, card)
	}
	page = f.inbox(t, f.s, "all")
	if len(page.Items) != 4 || byDescription(page, "Example Cafe").Status != "recorded" {
		t.Fatalf("history doubled or lost: %+v", page.Items)
	}
	// The gone card account frees its Book account for the new one.
	succeeded(t, f.owner.LinkBankAccount(ctx, f.book.ID, f.actor, card.ID, f.card.ID))
	if page = f.inbox(t, f.s, "all"); page.Summary.NeedsAccount != 0 {
		t.Fatalf("summary = %+v", page.Summary)
	}
}

// A statement that overlaps an earlier one adds only its new lines: a line
// already brought in by the earlier statement becomes its repeat, and the
// ledger refuses to record a repeat or the same money twice, whoever asks.
func TestOverlappingStatementsRecordTheSameMoneyOnce(t *testing.T) {
	f := newBankFixture(t)
	a := statementDocument(t, f.db, f.s, "october")
	importStatement(t, f.db, f.s, ledger.ImportStatement{RequestKey: "october", AccountID: f.checking.ID, DocumentID: a,
		PeriodStart: "2026-10-01", PeriodEnd: "2026-10-31", OpeningMinor: "100000", ClosingMinor: "18500", Rows: []ledger.StatementRowInput{
			row(1, "2026-10-15", "-20000", "Example invoice 1042"), row(2, "2026-10-18", "-80000", "Card payment"), row(3, "2026-10-20", "-1500", "Monthly fee")}})
	b := statementDocument(t, f.db, f.s, "overlap")
	importStatement(t, f.db, f.s, ledger.ImportStatement{RequestKey: "overlap", AccountID: f.checking.ID, DocumentID: b,
		PeriodStart: "2026-10-15", PeriodEnd: "2026-11-03", OpeningMinor: "120000", ClosingMinor: "13500", Rows: []ledger.StatementRowInput{
			row(1, "2026-10-15", "-20000", "EXAMPLE INV 1042 PAYMENT"), row(2, "2026-10-18", "-80000", "Card payment"), row(3, "2026-10-20", "-1500", "Monthly fee"),
			row(4, "2026-11-03", "-5000", "Example new charge")}})
	page := f.inbox(t, f.s, "all")
	unrecorded, repeats := 0, 0
	var repeat, first ledger.BankTransaction
	for _, item := range page.Items {
		switch item.Status {
		case "unrecorded":
			unrecorded++
			if item.Description == "Card payment" {
				first = item
			}
		case "repeat":
			repeats++
			if item.Description == "Card payment" {
				repeat = item
			}
		}
	}
	if unrecorded != 4 || repeats != 3 || repeat.ID == "" || first.ID == "" {
		t.Fatalf("unrecorded = %d, repeats = %d: %+v", unrecorded, repeats, page.Items)
	}
	record := func(key, item string) error {
		_, err := f.db.Store.Call(f.db.Context, f.s, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: key, TransactionID: item, Description: "Card payment", OtherAccountID: f.card.ID}))
		return err
	}
	bankRefused(t, record("repeat", repeat.ID), "marked as a repeat is not recorded")
	// An agent can't queue it for approval either.
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	_, err := f.db.Store.Call(f.db.Context, agent, "prepare_operation", value(ledger.PrepareOperation{RequestKey: "p-repeat", Operation: "record_bank_activity",
		Arguments: value(ledger.RecordBankActivity{RequestKey: "r-repeat", TransactionID: repeat.ID, Description: "Card payment", OtherAccountID: f.card.ID})}))
	bankRefused(t, err, "marked as a repeat is not recorded")
	succeeded(t, record("first", first.ID))
	bankRefused(t, record("repeat-again", repeat.ID), "marked as a repeat is not recorded")
}

// A Book that starts from the bank, with only the accounts its bank
// accounts were linked to, still takes a starter chart: the chart keeps
// those and leaves out its own account of each of their kinds.
func TestStarterChartBesideBankAccounts(t *testing.T) {
	db := testutil.New(t)
	book, s := db.Book(t, "Example Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	actor, _ := proposalOwner(t, db, s)
	var checking, savings ledger.Account
	json.Unmarshal(db.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: "checking", Class: "asset", Code: "1010", Name: "Example Checking (0001)", Role: "cash"}), &checking)
	json.Unmarshal(db.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: "savings", Class: "asset", Code: "1020", Name: "Example Reserve (0002)", Role: "cash"}), &savings)
	id, err := owner.AddBankConnection(db.Context, book.ID, actor, ledger.NewBankConnection{Provider: "plaid", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(), TokenSealed: sealedForTest, KeyVersion: 1})
	succeeded(t, err)
	_, err = db.Store.ApplyBankSync(db.Context, ledger.BankSyncTarget{TenantID: book.TenantID, BookID: book.ID, ConnectionID: id}, ledger.BankSyncBatch{NextCursor: "c1", Complete: true,
		Accounts: []ledger.BankSyncAccount{
			{ProviderAccountID: "acc", Name: "Example Checking", Mask: "0001", Type: "depository", Subtype: "checking", Kind: "cash", Currency: book.Currency, Scale: book.Scale},
			{ProviderAccountID: "acc-2", Name: "Example Reserve", Mask: "0002", Type: "depository", Subtype: "savings", Kind: "cash", Currency: book.Currency, Scale: book.Scale}}})
	succeeded(t, err)
	views, err := owner.BankConnections(db.Context, book.ID)
	succeeded(t, err)
	for _, a := range views[0].Accounts {
		target := checking.ID
		if a.Mask == "0002" {
			target = savings.ID
		}
		succeeded(t, owner.LinkBankAccount(db.Context, book.ID, actor, a.ID, target))
	}
	var applied ledger.AppliedStarterChart
	if json.Unmarshal(db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: "personal-v2"}), &applied) != nil {
		t.Fatal("no chart")
	}
	chart, _ := ledger.StarterChart("personal-v2")
	names := map[string]bool{}
	for _, a := range applied.Accounts {
		names[a.Name] = true
	}
	// A linked savings account takes Savings' place, not Cash's.
	if len(applied.Accounts) != len(chart.Accounts)-2 || names["Checking"] || names["Savings"] || !names["Cash"] || !names["Credit card"] {
		t.Fatalf("applied %d of %d: %v", len(applied.Accounts), len(chart.Accounts), names)
	}
}

// Activity on a bank account not yet linked suggests the Book account to
// link it to, by the bank's type and the account's role and code: a chart's
// Checking for a checking account, its Savings for savings, its Credit card
// for a card. An account already linked isn't suggested again.
func TestUnlinkedBankAccountsSuggestTheirBookAccount(t *testing.T) {
	db := testutil.New(t)
	book, s := db.Book(t, "Example Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	actor, _ := proposalOwner(t, db, s)
	var applied ledger.AppliedStarterChart
	if json.Unmarshal(db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: "personal-v2"}), &applied) != nil {
		t.Fatal("no chart")
	}
	byName := map[string]string{}
	for _, a := range applied.Accounts {
		byName[a.Name] = a.ID
	}
	id, err := owner.AddBankConnection(db.Context, book.ID, actor, ledger.NewBankConnection{Provider: "plaid", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(), TokenSealed: sealedForTest, KeyVersion: 1})
	succeeded(t, err)
	account := func(provider, subtype, kind, mask string) ledger.BankSyncAccount {
		return ledger.BankSyncAccount{ProviderAccountID: provider, Name: "Example " + subtype, Mask: mask, Type: "depository", Subtype: subtype, Kind: kind, Currency: book.Currency, Scale: book.Scale}
	}
	charge := func(provider, id string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: provider, ProviderTransactionID: id, Date: "2026-11-02", AmountMinor: -1200, Currency: book.Currency, Scale: book.Scale, Description: "EXAMPLE " + id, Counterparty: "Example " + id}
	}
	_, err = db.Store.ApplyBankSync(db.Context, ledger.BankSyncTarget{TenantID: book.TenantID, BookID: book.ID, ConnectionID: id}, ledger.BankSyncBatch{NextCursor: "c1", Complete: true,
		Accounts: []ledger.BankSyncAccount{account("chk", "checking", "cash", "0001"), account("sav", "savings", "cash", "0002"), account("crd", "credit card", "card", "0003")},
		Added:    []ledger.BankSyncTransaction{charge("chk", "a"), charge("sav", "b"), charge("crd", "c")}})
	succeeded(t, err)
	var page ledger.BankActivityPage
	json.Unmarshal(db.Call(t, s, "list_bank_activity", ledger.BankActivityQuery{Status: "needs_account"}), &page)
	got := map[string]string{}
	for _, item := range page.Items {
		got[item.BankType] = item.SuggestedAccountID
	}
	if len(page.Items) != 3 || got["checking"] != byName["Checking"] || got["savings"] != byName["Savings"] || got["credit card"] != byName["Credit card"] {
		t.Fatalf("suggestions = %v, chart = %v", got, byName)
	}
	views, err := owner.BankConnections(db.Context, book.ID)
	succeeded(t, err)
	for _, a := range views[0].Accounts {
		if a.SuggestedAccountID == "" || a.SuggestedAccountID != got[a.BankType] {
			t.Fatalf("overview suggestion = %+v", a)
		}
		if a.BankType == "checking" {
			succeeded(t, owner.LinkBankAccount(db.Context, book.ID, actor, a.ID, a.SuggestedAccountID))
		}
	}
	views, err = owner.BankConnections(db.Context, book.ID)
	succeeded(t, err)
	for _, a := range views[0].Accounts {
		if a.BankType != "checking" && a.SuggestedAccountID == byName["Checking"] {
			t.Fatal("a linked account was suggested again", a)
		}
	}
}

// An expense recorded from bank activity, with no receipt and no payee, is
// still asked about before the period closes, and the bank items left to
// record are counted too.
func TestTheCloseReviewAsksAboutRecordedBankActivity(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "t1", "2026-09-12", -4500, "Example Office Supply"),
		f.tx("acc-checking", "t2", "2026-09-20", -1599, "Example Streaming")}, nil)
	item := byDescription(f.inbox(t, f.s, ""), "Example Office Supply")
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "record", TransactionID: item.ID, Description: "Office supplies",
		Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: "4500"}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: "4500"}}})
	review, err := f.owner.PeriodReview(f.db.Context, f.book.ID, "2026-09-01", "2026-10-31")
	succeeded(t, err)
	if review.Clean || len(review.EntriesWithoutEvidence) != 1 || len(review.EntriesWithoutPayee) != 1 || review.OpenItems["unrecorded_bank_items"] != 1 {
		t.Fatalf("the close review: %+v", review)
	}
}

func TestMatchesNeedTheBanksWordsToAgreeNotJustTheAmount(t *testing.T) {
	f := newBankFixture(t)
	// Two charges of the same amount, a day apart, written as purchases: the
	// amount alone doesn't make them one card payment.
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "shop", "2026-03-10", -6000, "Example Hardware Store"),
		f.tx("acc-card", "cafe", "2026-03-11", -6000, "Example Coffee Roasters"),
	}, nil)
	var suggestions []ledger.BankSuggestion
	json.Unmarshal(f.db.Call(t, f.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 0 {
		t.Fatalf("two purchases of one amount were paired: %+v", suggestions)
	}

	// An entry already recorded as a card payment from checking is offered
	// for a checking item that says it paid the card, never for a wire.
	g := newBankFixture(t)
	g.db.Call(t, g.s, "post_journal", ledger.PostJournal{RequestKey: "paid", EconomicDate: "2026-04-02", AccountingDate: "2026-04-02", Description: "Card payment",
		Lines: []ledger.PostLine{{AccountID: g.card.ID, Side: "debit", AmountMinor: "15000"}, {AccountID: g.checking.ID, Side: "credit", AmountMinor: "15000"}}})
	g.sync(t, "c1", "c2", []ledger.BankSyncTransaction{g.tx("acc-checking", "wire", "2026-04-03", -15000, "Wire transfer outgoing Example Supplier")}, nil)
	json.Unmarshal(g.db.Call(t, g.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 0 {
		t.Fatalf("a wire to a supplier was offered as the card payment: %+v", suggestions)
	}
	h := newBankFixture(t)
	h.db.Call(t, h.s, "post_journal", ledger.PostJournal{RequestKey: "paid", EconomicDate: "2026-04-02", AccountingDate: "2026-04-02", Description: "Card payment",
		Lines: []ledger.PostLine{{AccountID: h.card.ID, Side: "debit", AmountMinor: "15000"}, {AccountID: h.checking.ID, Side: "credit", AmountMinor: "15000"}}})
	h.sync(t, "c1", "c2", []ledger.BankSyncTransaction{h.tx("acc-checking", "pay", "2026-04-03", -15000, "Example card autopay")}, nil)
	json.Unmarshal(h.db.Call(t, h.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 1 || suggestions[0].Kind != "existing_entry" {
		t.Fatalf("a card payment wasn't offered its entry: %+v", suggestions)
	}

	// A move to a wallet already recorded is offered for the checking item
	// even when the bank names whom the wallet paid, not the wallet.
	w := newBankFixture(t)
	var wallet ledger.Account
	json.Unmarshal(w.db.Call(t, w.s, "create_account", ledger.CreateAccount{RequestKey: "wallet", Class: "asset", Name: "Example Wallet"}), &wallet)
	w.db.Call(t, w.s, "post_journal", ledger.PostJournal{RequestKey: "funded", EconomicDate: "2026-04-02", AccountingDate: "2026-04-02", Description: "Wallet funding",
		Lines: []ledger.PostLine{{AccountID: wallet.ID, Side: "debit", AmountMinor: "8000"}, {AccountID: w.checking.ID, Side: "credit", AmountMinor: "8000"}}})
	w.sync(t, "c1", "c2", []ledger.BankSyncTransaction{w.tx("acc-checking", "named", "2026-04-04", -8000, "Example Person")}, nil)
	json.Unmarshal(w.db.Call(t, w.s, "suggest_bank_matches", struct{}{}), &suggestions)
	if len(suggestions) != 1 || suggestions[0].Kind != "existing_entry" {
		t.Fatalf("a wallet funding wasn't offered its entry: %+v", suggestions)
	}
}

// addCash connects one more cash account at the fixture's bank and links it
// to a new asset account of the Book with the same name.
func (f *bankFixture) addCash(t *testing.T, providerID, name, subtype string) {
	t.Helper()
	_, err := f.db.Store.ApplyBankSync(f.db.Context, f.target, ledger.BankSyncBatch{FromCursor: "c1", NextCursor: "c1", Complete: true, Accounts: []ledger.BankSyncAccount{
		{ProviderAccountID: providerID, Name: name, Mask: "0009", Type: "depository", Subtype: subtype, Kind: "cash", Currency: f.book.Currency, Scale: f.book.Scale},
	}})
	succeeded(t, err)
	var book ledger.Account
	if json.Unmarshal(f.db.Call(t, f.s, "create_account", ledger.CreateAccount{RequestKey: providerID, Class: "asset", Name: name}), &book) != nil || book.ID == "" {
		t.Fatal("the Book account wasn't created")
	}
	views, err := f.owner.BankConnections(f.db.Context, f.book.ID)
	succeeded(t, err)
	for _, a := range views[0].Accounts {
		if a.Name == name {
			f.accounts[providerID] = a.ID
		}
	}
	succeeded(t, f.owner.LinkBankAccount(f.db.Context, f.book.ID, f.actor, f.accounts[providerID], book.ID))
}

func (f *bankFixture) suggestions(t *testing.T) []ledger.BankSuggestion {
	t.Helper()
	var out []ledger.BankSuggestion
	if json.Unmarshal(f.db.Call(t, f.s, "suggest_bank_matches", struct{}{}), &out) != nil {
		t.Fatal("invalid suggestions")
	}
	return out
}

func TestWordsEveryBankWritesDoNotMakeATransfer(t *testing.T) {
	// A supplier paid by ACH from checking, and a customer's ACH receipt
	// of the same amount into savings the next day.
	f := newBankFixture(t)
	f.addCash(t, "acc-savings", "Example Savings", "savings")
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "supplier", "2026-05-04", -6000, "ACH debit Example Supplier online"),
		f.tx("acc-savings", "customer", "2026-05-05", 6000, "ACH credit Example Customer mobile"),
	}, nil)
	if got := f.suggestions(t); len(got) != 0 {
		t.Fatalf("two ACH items of one amount were paired: %+v", got)
	}

	// A customer's check deposited into checking, and an insurance premium
	// of the same amount paid from savings.
	g := newBankFixture(t)
	g.addCash(t, "acc-savings", "Example Savings", "savings")
	g.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		g.tx("acc-checking", "check", "2026-05-04", 25000, "Mobile deposit check 1042"),
		g.tx("acc-savings", "premium", "2026-05-06", -25000, "Example Insurance premium"),
	}, nil)
	if got := g.suggestions(t); len(got) != 0 {
		t.Fatalf("a check deposit was paired with a premium from savings: %+v", got)
	}
}

func TestADepositIntoAWalletIsAMove(t *testing.T) {
	// The wallet writes "Deposit" when checking funds it, and checking names
	// whom the wallet paid. Known by its name...
	f := newBankFixture(t)
	f.addCash(t, "acc-wallet", "Example Venmo", "checking")
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-wallet", "funded", "2026-05-04", 4000, "Deposit"),
		f.tx("acc-checking", "paid", "2026-05-06", -4000, "Example Person"),
	}, nil)
	if got := f.suggestions(t); len(got) != 1 || got[0].Kind != "transfer" {
		t.Fatalf("funding a wallet wasn't offered as a transfer: %+v", got)
	}
	// ...or by the type its bank reports.
	g := newBankFixture(t)
	g.addCash(t, "acc-wallet", "Example Balance", "paypal")
	g.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		g.tx("acc-wallet", "funded", "2026-05-04", 4000, "Deposit"),
		g.tx("acc-checking", "paid", "2026-05-06", -4000, "Example Person"),
	}, nil)
	if got := g.suggestions(t); len(got) != 1 || got[0].Kind != "transfer" {
		t.Fatalf("funding a wallet wasn't offered as a transfer: %+v", got)
	}
	// The same words between two bank accounts are not enough.
	h := newBankFixture(t)
	h.addCash(t, "acc-savings", "Example Reserve", "savings")
	h.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		h.tx("acc-savings", "funded", "2026-05-04", 4000, "Deposit"),
		h.tx("acc-checking", "paid", "2026-05-06", -4000, "Example Person"),
	}, nil)
	if got := h.suggestions(t); len(got) != 0 {
		t.Fatalf("a deposit into savings was paired with a payment to a person: %+v", got)
	}
}

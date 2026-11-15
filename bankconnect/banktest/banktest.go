// Package banktest is one set of bank activity cases that every path to a
// bank action must pass: the owner's screen, an agent over MCP, and any
// embedding server's own routes. Each path supplies a Driver; the cases are
// the same. Data is synthetic.
package banktest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// Driver takes bank actions one way: on the screen, over MCP, or through a
// server's own routes. Do names the ledger operation and its arguments; a
// driver whose path needs the owner's approval for an operation prepares it
// and approves it as the owner would.
type Driver interface {
	Name() string
	Inbox(t *testing.T, status string) ledger.BankActivityPage
	Suggestions(t *testing.T) []ledger.BankSuggestion
	Do(t *testing.T, operation string, args any) (json.RawMessage, error)
	Origin(t *testing.T, entry string) ledger.EntryOrigin
}

// Fixture is a Book with a linked checking account and card, synced with
// Activity, and a way to post an entry outside the path under test.
type Fixture struct {
	Connection string
	Checking   string // Book asset account linked to the checking account
	Card       string // Book liability account linked to the card
	Expense    string // Book expense account
	// Post records a journal outside the path under test, as setup.
	Post func(t *testing.T, q ledger.PostJournal) ledger.Entry
}

// Activity is the synthetic history every case starts from: a purchase an
// entry already records, a card payment that is one transfer, a deposit,
// and a fee.
func Activity(currency string, scale int) ledger.BankSyncBatch {
	tx := func(account, id, date string, amount int64, name string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: account, ProviderTransactionID: id, Date: date, AmountMinor: amount, Currency: currency, Scale: scale, Description: name}
	}
	return ledger.BankSyncBatch{NextCursor: "synthetic-1", Complete: true,
		Accounts: []ledger.BankSyncAccount{
			{ProviderAccountID: "checking", Name: "Example Checking", Mask: "0001", Subtype: "checking", Kind: "cash", Currency: currency, Scale: scale},
			{ProviderAccountID: "card", Name: "Example Card", Mask: "0002", Subtype: "credit card", Kind: "card", Currency: currency, Scale: scale},
		},
		Added: []ledger.BankSyncTransaction{
			tx("checking", "supplies", "2026-03-02", -4500, "Example Office Supply"),
			tx("checking", "payment-out", "2026-03-10", -25000, "Example card payment"),
			tx("card", "payment-in", "2026-03-11", -25000, "Payment received"),
			tx("checking", "deposit", "2026-03-12", 120000, "Example Customer"),
			tx("checking", "fee", "2026-03-13", -500, "Example bank fee"),
		}}
}

// Case is one action every path must take the same way.
type Case struct {
	Name string
	Run  func(t *testing.T, d Driver, f Fixture)
}

func item(t *testing.T, page ledger.BankActivityPage, description string) ledger.BankTransaction {
	t.Helper()
	for _, v := range page.Items {
		if v.Description == description {
			return v
		}
	}
	t.Fatalf("no item %q", description)
	return ledger.BankTransaction{}
}

// do takes an action the case expects to succeed.
func do(t *testing.T, d Driver, operation string, args any) json.RawMessage {
	t.Helper()
	out, err := d.Do(t, operation, args)
	if err != nil {
		t.Fatalf("%s %s: %v", d.Name(), operation, err)
	}
	return out
}

// Cases are the shared actions. Each starts from a fresh Fixture.
var Cases = []Case{
	{"link an existing entry and say why", func(t *testing.T, d Driver, f Fixture) {
		entry := f.Post(t, ledger.PostJournal{RequestKey: "supplies", EconomicDate: "2026-03-02", AccountingDate: "2026-03-02", Description: "Office supplies",
			Lines: []ledger.PostLine{{AccountID: f.Expense, Side: "debit", AmountMinor: "4500"}, {AccountID: f.Checking, Side: "credit", AmountMinor: "4500"}}})
		var match ledger.BankSuggestion
		for _, s := range d.Suggestions(t) {
			if s.Kind == "existing_entry" && s.EntryID == entry.ID {
				match = s
			}
		}
		if match.EntryID == "" {
			t.Fatalf("%s: no match for the posted entry", d.Name())
		}
		do(t, d, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "link", TransactionID: match.TransactionIDs[0], EntryID: entry.ID, Basis: "existing_entry", Reason: match.Reason})
		origin := d.Origin(t, entry.ID)
		if len(origin.Bank) != 1 || origin.Bank[0].Basis != "existing_entry" {
			t.Fatalf("%s: origin %+v", d.Name(), origin)
		}
		// The same transaction is never recorded twice.
		if _, err := d.Do(t, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "link-again", TransactionID: match.TransactionIDs[0], EntryID: entry.ID}); err == nil {
			t.Fatalf("%s: linked twice", d.Name())
		}
	}},
	{"record a transfer between own accounts", func(t *testing.T, d Driver, f Fixture) {
		var pair []string
		for _, s := range d.Suggestions(t) {
			if s.Kind == "transfer" {
				pair = s.TransactionIDs
			}
		}
		if len(pair) != 2 {
			t.Fatalf("%s: no transfer suggested", d.Name())
		}
		do(t, d, "record_bank_transfer", ledger.RecordBankTransfer{RequestKey: "transfer", TransactionIDs: pair})
		for _, v := range d.Inbox(t, "recorded").Items {
			if v.Link == nil || v.Link.Basis != "transfer" {
				t.Fatalf("%s: recorded item %+v", d.Name(), v)
			}
		}
	}},
	{"unlink, then undo by reversal", func(t *testing.T, d Driver, f Fixture) {
		fee := item(t, d.Inbox(t, ""), "Example bank fee")
		entry := f.Post(t, ledger.PostJournal{RequestKey: "fee", EconomicDate: "2026-03-13", AccountingDate: "2026-03-13", Description: "Bank fee",
			Lines: []ledger.PostLine{{AccountID: f.Expense, Side: "debit", AmountMinor: "500"}, {AccountID: f.Checking, Side: "credit", AmountMinor: "500"}}})
		do(t, d, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "fee-link", TransactionID: fee.ID, EntryID: entry.ID})
		linked := item(t, d.Inbox(t, "recorded"), "Example bank fee")
		do(t, d, "unlink_bank_activity", ledger.UnlinkBankActivity{RequestKey: "fee-unlink", LinkID: linked.Link.ID, Reason: "Linked to the wrong entry"})
		if item(t, d.Inbox(t, ""), "Example bank fee").Status != "unrecorded" {
			t.Fatalf("%s: unlink left it recorded", d.Name())
		}
		do(t, d, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "fee-relink", TransactionID: fee.ID, EntryID: entry.ID})
		do(t, d, "reverse_journal", ledger.ReverseJournal{RequestKey: "fee-undo", EntryID: entry.ID, AccountingDate: "2026-03-14", Reason: "Undo"})
		if d.Origin(t, entry.ID).ReversedBy == "" || item(t, d.Inbox(t, ""), "Example bank fee").Status != "unrecorded" {
			t.Fatalf("%s: undo didn't return the activity to the inbox", d.Name())
		}
	}},
	{"pause and resume a bank", func(t *testing.T, d Driver, f Fixture) {
		do(t, d, "pause_bank_connection", ledger.PauseBankConnection{RequestKey: "pause", ConnectionID: f.Connection, Reason: "Away for a while"})
		if _, err := d.Do(t, "pause_bank_connection", ledger.PauseBankConnection{RequestKey: "pause-again", ConnectionID: f.Connection, Reason: "Again"}); err == nil {
			t.Fatalf("%s: paused twice", d.Name())
		}
		do(t, d, "resume_bank_connection", ledger.ResumeBankConnection{RequestKey: "resume", ConnectionID: f.Connection})
	}},
	{"refuse what the rules refuse, in words", func(t *testing.T, d Driver, f Fixture) {
		deposit := item(t, d.Inbox(t, ""), "Example Customer")
		_, err := d.Do(t, "record_bank_transfer", ledger.RecordBankTransfer{RequestKey: "bad", TransactionIDs: []string{deposit.ID, item(t, d.Inbox(t, ""), "Example bank fee").ID}})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "transfer") {
			t.Fatalf("%s: a deposit and a fee recorded as a transfer: %v", d.Name(), err)
		}
	}},
}

// Run runs every case with a fresh fixture from setup.
func Run(t *testing.T, setup func(t *testing.T) (Driver, Fixture)) {
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) {
			d, f := setup(t)
			c.Run(t, d, f)
		})
	}
}

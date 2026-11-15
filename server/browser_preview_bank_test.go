//go:build browserpreview

package server

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

// previewBanks turns on bank connections for the preview as a Vult without
// a provider screen has them: seeded logins and their activity, with no
// screen to link more. Its keyring lives in the preview's own folder.
func previewBanks(t *testing.T, f *documentServerFixture, dir string) *bankconnect.Service {
	return &bankconnect.Service{Store: ledger.Store{Runtime: f.db.Runtime}, Connectors: map[string]bankconnect.Connector{},
		Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(dir, "bank-keyring.json")}}}
}

// seedPreviewBank adds a bank login with activity to Eightball Inc.: an
// expense an entry already records, a card payment that is one transfer, and
// items left to record. The bank's name is real; its data is made up.
func seedPreviewBank(t *testing.T, f *documentServerFixture) {
	t.Helper()
	ctx := f.db.Context
	var book string
	if err := f.db.Admin.QueryRow(ctx, "SELECT b.id::text FROM public.books b JOIN public.book_revisions r ON r.tenant_id=b.tenant_id AND r.book_id=b.id WHERE r.name='Eightball Inc.' LIMIT 1").Scan(&book); err != nil {
		t.Fatal(err)
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(ctx, book, "Example bank agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: book, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	var cash, software string
	if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.accounts WHERE book_id=$1 AND code='1000'", book).Scan(&cash); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.accounts WHERE book_id=$1 AND code='6200'", book).Scan(&software); err != nil {
		t.Fatal(err)
	}
	card := f.db.Account(t, scope, "2100", "liability")
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, book, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-synthetic-preview",
			InstitutionName: "Mercury", TokenSealed: bytes.Repeat([]byte{1}, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: login}
	tx := func(account, id, date string, amount int64, name, counterparty string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: account, ProviderTransactionID: id, Date: date, AmountMinor: amount, Currency: "USD", Scale: 2, Description: name, Counterparty: counterparty}
	}
	pending := tx("card", "p1", "2026-11-10", 1850, "CAFE EXAMPLE 0042", "Example Cafe")
	pending.Pending = true
	if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{NextCursor: "synthetic", Complete: true,
		Accounts: []ledger.BankSyncAccount{
			{ProviderAccountID: "checking", Name: "Mercury Checking", Mask: "0000", Type: "depository", Subtype: "checking", Kind: "cash", Currency: "USD", Scale: 2},
			{ProviderAccountID: "card", Name: "Mercury Credit", Mask: "1111", Type: "credit", Subtype: "credit card", Kind: "card", Currency: "USD", Scale: 2},
		},
		Added: []ledger.BankSyncTransaction{
			tx("checking", "t1", "2026-11-03", -4900, "LINEWISE SOFTWARE SUBSCR", "Linewise Software"),
			tx("checking", "t2", "2026-11-05", -60000, "ONLINE PAYMENT TO CARD 1111", ""),
			tx("card", "t3", "2026-11-06", -60000, "PAYMENT THANK YOU", ""),
			tx("checking", "t4", "2026-11-07", 420000, "HARBORLINE CLIENT ACH", "Harborline Client"),
			tx("card", "t5", "2026-11-08", 12999, "NORTHWIND CLOUD HOSTING", "Northwind Cloud"),
			tx("checking", "t6", "2026-11-09", -32500, "COMMON DESK COWORKING", "Common Desk"),
			pending,
		}}); err != nil {
		t.Fatal(err)
	}
	var overview []ledger.BankConnectionView
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		overview, err = o.BankConnections(ctx, book)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range overview[0].Accounts {
		account := cash
		if a.Kind == "card" {
			account = card.ID
		}
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			return o.LinkBankAccount(ctx, book, who, a.ID, account)
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An agent already recorded the design tools subscription.
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "design-tools", EconomicDate: "2026-11-03", AccountingDate: "2026-11-03", Description: "Example Design Tools subscription",
		Lines: []ledger.PostLine{{AccountID: software, Side: "debit", AmountMinor: "4900"}, {AccountID: cash, Side: "credit", AmountMinor: "4900"}}})
	// The owner recorded a client deposit and made it a rule that splits out
	// the sales tax inside it.
	income := f.db.Account(t, scope, "4150", "income")
	tax := f.db.Account(t, scope, "2400", "liability")
	var inbox ledger.BankActivityPage
	if err := json.Unmarshal(f.db.Call(t, scope, "list_bank_activity", ledger.BankActivityQuery{Status: "all"}), &inbox); err != nil {
		t.Fatal(err)
	}
	for _, item := range inbox.Items {
		if item.Description != "HARBORLINE CLIENT ACH" {
			continue
		}
		f.db.Call(t, scope, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "client", TransactionID: item.ID, Description: "Harborline Client sales", OtherAccountID: income.ID})
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			rule, err := o.CreateRecordingRule(ctx, book, who, ledger.RuleFromActivity{TransactionID: item.ID, MinMinor: "10000", MaxMinor: "1000000", Description: "Harborline Client sales"})
			if err != nil {
				return err
			}
			_, err = o.SetRuleParts(ctx, book, who, rule.ID, []ledger.RulePart{{AccountID: tax.ID, Kind: "ratio", Numerator: 825, Denominator: 10825}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// seedPersonalCardBank links Kit Blanche's personal card to her own Book, with
// activity left to record: groceries and travel, and Eightball Inc. costs
// paid on the personal card (software, the company's domain, coworking).
// The kind of mix a founder's card really has. Every amount is made up.
func seedPersonalCardBank(t *testing.T, f *documentServerFixture) {
	t.Helper()
	ctx := f.db.Context
	var book, card string
	if err := f.db.Admin.QueryRow(ctx, "SELECT b.id::text FROM public.books b JOIN public.book_revisions r ON r.tenant_id=b.tenant_id AND r.book_id=b.id WHERE r.name='Kit Blanche' LIMIT 1").Scan(&book); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.accounts WHERE book_id=$1 AND code='2100'", book).Scan(&card); err != nil {
		t.Fatal(err)
	}
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, book, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-synthetic-personal-card",
			InstitutionName: "Example Card Co", TokenSealed: bytes.Repeat([]byte{2}, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: login}
	tx := func(id, date string, amount int64, name, counterparty string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: "rewards", ProviderTransactionID: id, Date: date, AmountMinor: amount, Currency: "USD", Scale: 2, Description: name, Counterparty: counterparty}
	}
	if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{NextCursor: "synthetic", Complete: true,
		Accounts: []ledger.BankSyncAccount{
			{ProviderAccountID: "rewards", Name: "Rewards card", Mask: "1111", Type: "credit", Subtype: "credit card", Kind: "card", Currency: "USD", Scale: 2},
		},
		Added: []ledger.BankSyncTransaction{
			tx("k1", "2026-11-01", 1500, "LINEWISE SOFTWARE PRO", "Linewise Software"),
			tx("k2", "2026-11-02", 8432, "FRESH MARKET GROCERY", "Fresh Market"),
			tx("k3", "2026-11-03", 1999, "DOMAINLY EIGHTBALL.EXAMPLE", "Domainly"),
			tx("k4", "2026-11-04", 32500, "COMMON DESK COWORKING DAY PASSES", "Common Desk"),
			tx("k5", "2026-11-06", 24600, "COASTLINE AIR SFO-SEA", "Coastline Air"),
			tx("k6", "2026-11-08", 6210, "CORNER BAKERY & DELI", "Corner Bakery"),
			tx("k7", "2026-11-09", 4800, "NORTHWIND CLOUD STORAGE", "Northwind Cloud"),
			tx("k8", "2026-11-10", 18975, "HARBOR HOTEL SEATTLE", "Harbor Hotel"),
		}}); err != nil {
		t.Fatal(err)
	}
	var overview []ledger.BankConnectionView
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		overview, err = o.BankConnections(ctx, book)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range overview[0].Accounts {
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			return o.LinkBankAccount(ctx, book, who, a.ID, card)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

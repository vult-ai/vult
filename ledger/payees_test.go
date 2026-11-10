package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func newPayee(t *testing.T, db *testutil.Database, s ledger.Scope, key, name string) ledger.Payee {
	t.Helper()
	var p ledger.Payee
	if json.Unmarshal(db.Call(t, s, "create_payee", ledger.CreatePayee{RequestKey: key, DisplayName: name, Kind: "organization"}), &p) != nil || p.Revision != 1 || p.Status != "active" {
		t.Fatal("invalid payee result")
	}
	return p
}

func entryPayee(t *testing.T, db *testutil.Database, s ledger.Scope, entry string) ledger.EntryPayeeRead {
	t.Helper()
	var v ledger.EntryPayeeRead
	if json.Unmarshal(db.Call(t, s, "get_entry_payee", ledger.EntryQuery{EntryID: entry}), &v) != nil {
		t.Fatal("invalid entry payee read")
	}
	return v
}

func TestPayeesAreSeparateIdentitiesAndEntryPayeesAreSeparateDecisions(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	_, other := db.Book(t, "Example other Book")
	cafe := newPayee(t, db, s, "cafe", "Example Café")
	twin := newPayee(t, db, s, "twin", "Example Café")
	if cafe.ID == twin.ID {
		t.Fatal("same-name payees must stay separate")
	}
	if err := callError(db, s, "create_payee", ledger.CreatePayee{RequestKey: "blank", DisplayName: " padded ", Kind: "organization"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an untrimmed name was accepted", err)
	}
	entry := journal(t, db, s, "coffee", "2026-01-05", byCode["6500"].ID, byCode["1110"].ID, "450")
	before := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})
	if v := entryPayee(t, db, s, entry.ID); v.Status != "not_recorded" || v.Current != nil {
		t.Fatal("an entry with no decision reads as not recorded", v)
	}
	db.Call(t, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "unknown", EntryID: entry.ID, ExpectedRevision: 0, Unknown: true})
	if v := entryPayee(t, db, s, entry.ID); v.Status != "unknown" {
		t.Fatal("an explicit unknown differs from not recorded", v)
	}
	if err := callError(db, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "no-reason", EntryID: entry.ID, ExpectedRevision: 1, PayeeID: cafe.ID}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("changing a payee decision needs a reason", err)
	}
	if err := callError(db, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "stale", EntryID: entry.ID, ExpectedRevision: 0, PayeeID: cafe.ID}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("a stale decision was accepted", err)
	}
	foreign := newPayee(t, db, other, "foreign", "Example Café")
	if err := callError(db, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "foreign", EntryID: entry.ID, ExpectedRevision: 1, PayeeID: foreign.ID, Reason: "Receipt names it"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("another Book's payee was linked", err)
	}
	db.Call(t, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "link-cafe", EntryID: entry.ID, ExpectedRevision: 1, PayeeID: cafe.ID, Reason: "The receipt names the café"})
	v := entryPayee(t, db, s, entry.ID)
	if v.Status != "payee" || v.Current == nil || v.Current.PayeeID != cafe.ID || len(v.History) != 2 || v.History[0].Counterpart != "unknown" {
		t.Fatal("the payee decision and its history were not recorded", v)
	}
	// The entry, its receipt and source text are untouched.
	if !bytes.Equal(before, db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})) {
		t.Fatal("linking a payee changed the entry")
	}
	// A rename shows everywhere; the link stays with the identity.
	db.Call(t, s, "revise_payee", ledger.RevisePayee{RequestKey: "rename", PayeeID: cafe.ID, ExpectedRevision: 1, DisplayName: "Example Coffee House", Kind: "organization", Status: "active", Reason: "New sign"})
	if v := entryPayee(t, db, s, entry.ID); v.Current.PayeeName != "Example Coffee House" {
		t.Fatal("the entry must show the payee's current name", v.Current)
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	read, err := owner.Journal(db.Context, book.ID, entry.ID)
	if err != nil || read.Payee == nil || read.Payee.Status != "payee" || read.Payee.Current.PayeeName != "Example Coffee House" {
		t.Fatal("the owner's entry read must include the payee", err)
	}
	page, err := owner.Activity(db.Context, book.ID, ledger.ActivityPageQuery{Limit: 10})
	succeeded(t, err)
	found := false
	for _, row := range page.Transactions {
		if row.ID == entry.ID {
			found = row.Counterparty == "Example Coffee House" && row.PayeeID == cafe.ID && row.PayeeStatus == "payee"
		}
	}
	if !found {
		t.Fatal("Activity must show the entry's payee")
	}
	// A retired payee keeps its entries and takes no new links.
	db.Call(t, s, "revise_payee", ledger.RevisePayee{RequestKey: "retire", PayeeID: twin.ID, ExpectedRevision: 1, DisplayName: "Example Café", Kind: "organization", Status: "retired", Reason: "Closed"})
	second := journal(t, db, s, "coffee-2", "2026-01-06", byCode["6500"].ID, byCode["1110"].ID, "450")
	if err := callError(db, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "retired", EntryID: second.ID, ExpectedRevision: 0, PayeeID: twin.ID}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a retired payee took a new link", err)
	}
	var listed struct {
		Payees []ledger.Payee `json:"payees"`
	}
	if json.Unmarshal(db.Call(t, s, "list_payees", map[string]string{"status": "active"}), &listed) != nil || len(listed.Payees) != 1 || listed.Payees[0].Entries != 1 {
		t.Fatal("the active payee list must count current links", listed)
	}
}

func TestPayeeChangesFollowConnectionLevels(t *testing.T) {
	db := testutil.New(t)
	book, writer, byCode := chartBook(t, db, "business-v1")
	proposer, reader := connectionScope(t, db, book.ID, "propose"), connectionScope(t, db, book.ID, "read")
	entry := journal(t, db, writer, "coffee", "2026-01-05", byCode["6500"].ID, byCode["1110"].ID, "450")
	cafe := newPayee(t, db, writer, "cafe", "Example Café")
	for _, scope := range []ledger.Scope{proposer, reader} {
		if err := callError(db, scope, "create_payee", ledger.CreatePayee{RequestKey: "direct", DisplayName: "Example", Kind: "person"}); !errors.Is(err, ledger.ErrDenied) {
			t.Fatal("only write agents create payees directly", scope.Access, err)
		}
	}
	var listed struct {
		Payees []ledger.Payee `json:"payees"`
	}
	if json.Unmarshal(db.Call(t, reader, "list_payees", map[string]string{}), &listed) != nil || len(listed.Payees) != 1 {
		t.Fatal("read agents read payees")
	}
	p := prepareOperation(t, db, proposer, "propose-link", "set_entry_payee", ledger.SetEntryPayee{RequestKey: "link", EntryID: entry.ID, ExpectedRevision: 0, PayeeID: cafe.ID})
	if p.Preview.PayeeChange == nil || p.Preview.PayeeChange.Change != "link" || p.Preview.PayeeChange.DisplayName != "Example Café" || p.Preview.Description != "Link the payee Example Café" {
		t.Fatal("a payee proposal must preview its decision", p.Preview)
	}
	if v := entryPayee(t, db, writer, entry.ID); v.Status != "not_recorded" {
		t.Fatal("preparing a proposal recorded the decision")
	}
	approveOperation(t, db, writer, p, writer)
	executeOperation(t, db, writer, p)
	if v := entryPayee(t, db, writer, entry.ID); v.Status != "payee" || v.Current.PayeeID != cafe.ID {
		t.Fatal("the approved decision was not recorded", v)
	}
}

func TestRecurringChargesAreDerivedFromPayeesAndEntries(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	bank, software, rent := byCode["1110"].ID, byCode["6200"].ID, byCode["6100"].ID
	link := func(entry ledger.Entry, payee ledger.Payee) {
		db.Call(t, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "link-" + entry.ID, EntryID: entry.ID, PayeeID: payee.ID})
	}
	tools := newPayee(t, db, s, "tools", "Example Design Tools")
	for i, date := range []string{"2026-07-03", "2026-08-03", "2026-09-02", "2026-10-03"} {
		amount := "2900"
		if i == 3 {
			amount = "3100" // a small price change is still the same charge
		}
		link(journal(t, db, s, "tools-"+date, date, software, bank, amount), tools)
	}
	landlord := newPayee(t, db, s, "landlord", "Example Property Co")
	for _, date := range []string{"2026-04-01", "2026-05-01", "2026-06-01"} {
		link(journal(t, db, s, "rent-"+date, date, rent, bank, "150000"), landlord)
	}
	odd := newPayee(t, db, s, "odd", "Example Hardware Store")
	for _, p := range []struct{ date, amount string }{{"2026-08-01", "1000"}, {"2026-08-20", "45000"}, {"2026-10-02", "1200"}} {
		link(journal(t, db, s, "odd-"+p.date, p.date, software, bank, p.amount), odd)
	}
	var out struct {
		Charges []ledger.RecurringCharge `json:"charges"`
	}
	if json.Unmarshal(db.Call(t, s, "list_recurring_charges", ledger.RecurringQuery{Through: "2026-10-20"}), &out) != nil || len(out.Charges) != 1 {
		t.Fatal("only the current, regular, similar charge recurs", out.Charges)
	}
	c := out.Charges[0]
	if c.PayeeID != tools.ID || c.Cadence != "monthly" || c.Occurrences != 4 || c.TypicalAmountMinor != "3100" && c.TypicalAmountMinor != "2900" ||
		c.LastDate != "2026-10-03" || c.NextExpected != "2026-11-03" || len(c.EntryIDs) != 4 {
		t.Fatal("unexpected recurring charge", c)
	}
	// Nothing is stored: relinking an entry changes the derived result.
	var history ledger.EntryPayeeRead
	_ = json.Unmarshal(db.Call(t, s, "get_entry_payee", ledger.EntryQuery{EntryID: c.EntryIDs[3]}), &history)
	db.Call(t, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "relink", EntryID: c.EntryIDs[3], ExpectedRevision: 1, Unknown: true, Reason: "Charge was for a different service"})
	if json.Unmarshal(db.Call(t, s, "list_recurring_charges", ledger.RecurringQuery{Through: "2026-10-20"}), &out) != nil || len(out.Charges) != 1 || out.Charges[0].Occurrences != 3 {
		t.Fatal("the derived charges must follow the payee decisions", out.Charges)
	}
}

func TestAnnualChargesRemainVisibleBetweenRenewals(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	payee := newPayee(t, db, s, "annual-service", "Example Annual Service")
	for _, date := range []string{"2025-01-01", "2026-01-01"} {
		entry := journal(t, db, s, "annual-"+date, date, byCode["6200"].ID, byCode["1110"].ID, "10000")
		db.Call(t, s, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "payee-" + entry.ID, EntryID: entry.ID, PayeeID: payee.ID})
	}
	for _, through := range []string{"2026-06-01", "2026-12-31", "2027-07-19"} {
		var out struct {
			Charges []ledger.RecurringCharge `json:"charges"`
		}
		if err := json.Unmarshal(db.Call(t, s, "list_recurring_charges", ledger.RecurringQuery{Through: through}), &out); err != nil || len(out.Charges) != 1 {
			t.Fatalf("annual charge must stay visible while current at %s: %+v (%v)", through, out.Charges, err)
		}
		charge := out.Charges[0]
		if charge.PayeeID != payee.ID || charge.Cadence != "yearly" || charge.Occurrences != 2 || charge.TypicalAmountMinor != "10000" || charge.NextExpected != "2027-01-01" || len(charge.EntryIDs) != 2 {
			t.Fatal("unexpected annual charge", charge)
		}
	}
	var out struct {
		Charges []ledger.RecurringCharge `json:"charges"`
	}
	if err := json.Unmarshal(db.Call(t, s, "list_recurring_charges", ledger.RecurringQuery{Through: "2027-07-20"}), &out); err != nil || len(out.Charges) != 0 {
		t.Fatal("a charge beyond its currentness allowance must still disappear", out.Charges, err)
	}
}

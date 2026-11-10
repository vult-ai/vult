package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func accountChange(t *testing.T, s *http.Server, session, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := settingsRequest(session, body)
	r.URL.Path = accountChangesPath
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	return w
}

// The owner retires an account from the app in one step. A holding account
// first answers with its remaining balance; naming the destination records
// the transfer and the retirement together, with the owner as actor.
func TestOwnerRetiresAnAccountWithItsTransfer(t *testing.T) {
	f, s := browserServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	savings := f.db.Account(t, scope, "1010", "asset")
	checking := f.db.Account(t, scope, "1000", "asset")
	equity := f.db.Account(t, scope, "3000", "equity")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "fund", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example funding",
		Lines: []ledger.PostLine{{AccountID: savings.ID, Side: "debit", AmountMinor: "4000"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "4000"}}})
	retire := func(key, extra string) *httptest.ResponseRecorder {
		return accountChange(t, s, f.session, `{"book_id":"`+f.book.ID+`","operation":"retire_account","arguments":{"request_key":"`+key+`","account_id":"`+savings.ID+`","expected_revision":1`+extra+`}}`)
	}
	w := retire("owner-retire", "")
	var refusal struct {
		Error     string                    `json:"error"`
		Message   string                    `json:"message"`
		Remaining []ledger.RemainingBalance `json:"remaining"`
	}
	if w.Code != http.StatusUnprocessableEntity || json.Unmarshal(w.Body.Bytes(), &refusal) != nil || refusal.Error != "balance_remaining" ||
		len(refusal.Remaining) != 1 || refusal.Remaining[0].AmountMinor != "4000" || !strings.HasPrefix(refusal.Message, "This account still holds 40.00 USD.") {
		t.Fatal("the owner must be asked where the money went", w.Code, w.Body.String())
	}
	w = retire("owner-retire", `,"destination_account_id":"`+checking.ID+`","transfer_date":"2026-01-31"`)
	var retired ledger.RetiredAccount
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &retired) != nil || retired.Account.Status != "retired" || retired.Transfer == nil {
		t.Fatal("the owner's retirement was not recorded", w.Code, w.Body.String())
	}
	var owner, actor string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT id::text FROM access.identities WHERE subject=$1", f.identity.Subject).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT actor_id::text FROM public.entries WHERE id=$1", retired.Transfer.ID).Scan(&actor); err != nil || actor != owner {
		t.Fatal("the owner is the transfer's actor", err)
	}
	// The owner can't do it for another Book, or with an operation the app doesn't offer.
	if w := accountChange(t, s, f.session, `{"book_id":"`+f.book.ID+`","operation":"post_journal","arguments":{"request_key":"x"}}`); w.Code != http.StatusBadRequest {
		t.Fatal("the app endpoint accepted a journal", w.Code)
	}
	var charts struct {
		Charts []ledger.StarterChartTemplate `json:"charts"`
	}
	r := browserRequest(http.MethodGet, starterChartsPath, f.session)
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &charts) != nil || len(charts.Charts) != 2 {
		t.Fatal("starter charts unavailable", w.Code)
	}
}

// The owner approves an agent's proposed retirement; the transfer is recorded
// under its own receipt in the approving transaction.
func TestOwnerApprovesAProposedRetirement(t *testing.T) {
	f, s := browserServer(t)
	var writer, proposer ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		if writer, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write"); err != nil {
			return err
		}
		proposer, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example proposer", "propose")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: writer.ID, ActorID: writer.ActorID, Access: "write"}
	p := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: proposer.ID, ActorID: proposer.ActorID, Access: "propose"}
	savings := f.db.Account(t, w, "1010", "asset")
	checking := f.db.Account(t, w, "1000", "asset")
	equity := f.db.Account(t, w, "3000", "equity")
	f.db.Call(t, w, "post_journal", ledger.PostJournal{RequestKey: "fund", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example funding",
		Lines: []ledger.PostLine{{AccountID: savings.ID, Side: "debit", AmountMinor: "4000"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "4000"}}})
	args, _ := json.Marshal(ledger.RetireAccount{RequestKey: "retire", AccountID: savings.ID, ExpectedRevision: 1, DestinationAccountID: checking.ID, TransferDate: "2026-01-31"})
	var proposal ledger.OperationProposal
	if json.Unmarshal(f.db.Call(t, p, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "retire_account", Arguments: args}), &proposal) != nil {
		t.Fatal("invalid proposal")
	}
	rec := httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, proposalRequest(f.session, "/api/v1/agent-owner/approve", proposalDecisionBody(f.book.ID, proposal, "approve-retire", 0)))
	if rec.Code != http.StatusOK {
		t.Fatal("approval failed", rec.Code, rec.Body.String())
	}
	var status, transferActor string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT status FROM public.accounts WHERE id=$1", savings.ID).Scan(&status); err != nil || status != "retired" {
		t.Fatal("the approved retirement was not recorded", err)
	}
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT r.actor_id::text FROM public.requests r WHERE r.request_key='retire/transfer'").Scan(&transferActor); err != nil || transferActor != proposer.ActorID {
		t.Fatal("the transfer keeps the proposing agent as its actor", err)
	}
}

// The owner turns an account with history into a heading from the app: its
// balance stays, and new entries are refused.
func TestOwnerUsesAnAccountAsAHeading(t *testing.T) {
	f, s := browserServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	travel := f.db.Account(t, scope, "6400", "expense")
	bank := f.db.Account(t, scope, "1000", "asset")
	post := func(key string) error {
		b, _ := json.Marshal(ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example trip",
			Lines: []ledger.PostLine{{AccountID: travel.ID, Side: "debit", AmountMinor: "900"}, {AccountID: bank.ID, Side: "credit", AmountMinor: "900"}}})
		_, err := f.db.Store.Call(f.db.Context, scope, "post_journal", b)
		return err
	}
	if err := post("before"); err != nil {
		t.Fatal(err)
	}
	w := accountChange(t, s, f.session, `{"book_id":"`+f.book.ID+`","operation":"revise_account","arguments":{"request_key":"owner-heading","account_id":"`+travel.ID+`","expected_revision":1,"code":"6400","name":"Example 6400","heading":true}}`)
	var revised ledger.Account
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &revised) != nil || !revised.Heading || revised.Revision != 2 {
		t.Fatal("the owner's heading change was not recorded", w.Code, w.Body.String())
	}
	if err := post("after"); err == nil {
		t.Fatal("a heading took a new entry")
	}
	var balance string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT sum(amount_minor)::text FROM public.lines WHERE account_id=$1", travel.ID).Scan(&balance); err != nil || balance != "900" {
		t.Fatal("the heading's recorded lines must stay", balance, err)
	}
}

// The owner adds a payee and links it to an entry from the app; the payee
// list and the entry read show it.
func TestOwnerRecordsAnEntryPayee(t *testing.T) {
	f, s := browserServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	meals, bank := f.db.Account(t, scope, "6500", "expense"), f.db.Account(t, scope, "1000", "asset")
	var entry ledger.Entry
	if json.Unmarshal(f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "lunch", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "CARD 1234 EXAMPLE BISTRO",
		Lines: []ledger.PostLine{{AccountID: meals.ID, Side: "debit", AmountMinor: "1800"}, {AccountID: bank.ID, Side: "credit", AmountMinor: "1800"}}}), &entry) != nil {
		t.Fatal("invalid entry")
	}
	w := accountChange(t, s, f.session, `{"book_id":"`+f.book.ID+`","operation":"create_payee","arguments":{"request_key":"owner-payee","display_name":"Example Bistro","kind":"organization"}}`)
	var payee ledger.Payee
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &payee) != nil {
		t.Fatal("the owner's payee was not recorded", w.Code, w.Body.String())
	}
	w = accountChange(t, s, f.session, `{"book_id":"`+f.book.ID+`","operation":"set_entry_payee","arguments":{"request_key":"owner-link","entry_id":"`+entry.ID+`","expected_revision":0,"payee_id":"`+payee.ID+`"}}`)
	if w.Code != http.StatusOK {
		t.Fatal("the owner's payee link was not recorded", w.Code, w.Body.String())
	}
	r := browserRequest(http.MethodGet, payeesPath+"?book_id="+f.book.ID, f.session)
	rec := httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, r)
	var list struct {
		Payees []ledger.Payee `json:"payees"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Payees) != 1 || list.Payees[0].Entries != 1 {
		t.Fatal("the payee list must show the payee and its entry", rec.Code, rec.Body.String())
	}
	r = browserRequest(http.MethodGet, "/api/v1/transactions/"+entry.ID+"?book_id="+f.book.ID, f.session)
	rec = httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, r)
	var read ledger.JournalRead
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &read) != nil || read.Payee == nil || read.Payee.Current == nil ||
		read.Payee.Current.PayeeName != "Example Bistro" || read.Entry.Description != "CARD 1234 EXAMPLE BISTRO" {
		t.Fatal("the entry read must show the payee and keep its original text", rec.Code, rec.Body.String())
	}
}

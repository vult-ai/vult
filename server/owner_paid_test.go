package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// A business cost the owner paid on a personal card is recorded in both
// linked Books as one pair: the personal Book records the card charge as
// money for the business, the business Book the expense paid with the
// owner's money. The owner decides both at once; each Book balances.
func TestOwnerPaidCostIsDecidedAsOnePair(t *testing.T) {
	f, s, _ := bankServer(t)
	ctx, business := f.db.Context, f.book
	var personal ledger.Book
	var writerB, writerP, agentB, agentP ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		if personal, err = o.CreateBook(ctx, "Example Household", business.Currency, business.Scale); err != nil {
			return err
		}
		for _, c := range []struct {
			to     *ledger.Connection
			book   string
			access string
		}{{&writerB, business.ID, "write"}, {&writerP, personal.ID, "write"}, {&agentB, business.ID, "propose"}, {&agentP, personal.ID, "propose"}} {
			if *c.to, err = o.CreateConnection(ctx, c.book, "Example "+c.access+" agent", c.access); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scope := func(c ledger.Connection) ledger.Scope {
		return ledger.Scope{TenantID: f.tenant, BookID: c.BookID, ConnectionID: c.ID, ActorID: c.ActorID, Access: c.Access}
	}
	setProfile := func(b ledger.Book, profile string) {
		t.Helper()
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			current, err := o.Book(ctx, b.ID)
			if err != nil {
				return err
			}
			revision := int64(0)
			if current.MetadataRevision != nil {
				revision = *current.MetadataRevision
			}
			_, err = o.ReviseBook(ctx, who, ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "profile-" + b.ID, Revision: revision, Name: current.Name, Profile: &profile}, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setProfile(business, "business")
	setProfile(personal, "personal")
	account := func(c ledger.Connection, code, class, role string) ledger.Account {
		t.Helper()
		var a ledger.Account
		if json.Unmarshal(f.db.Call(t, scope(c), "create_account", ledger.CreateAccount{RequestKey: "account-" + code, Class: class, Code: code, Name: "Example " + code, Role: role}), &a) != nil {
			t.Fatal("account", code)
		}
		return a
	}
	contributions, hosting, income := account(writerB, "3100", "equity", ""), account(writerB, "6100", "expense", ""), account(writerB, "4100", "income", "")
	card, investment := account(writerP, "2100", "liability", "card"), account(writerP, "1500", "asset", "investment")

	// The personal card's charge arrives from the bank.
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, personal.ID, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(), TokenSealed: make([]byte, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Store.ApplyBankSync(ctx, ledger.BankSyncTarget{TenantID: f.tenant, BookID: personal.ID, ConnectionID: login}, ledger.BankSyncBatch{NextCursor: "c1", Complete: true,
		Accounts: []ledger.BankSyncAccount{{ProviderAccountID: "card", Name: "Example Rewards Card", Mask: "0001", Type: "credit", Subtype: "credit card", Kind: "card", Currency: personal.Currency, Scale: personal.Scale}},
		Added: []ledger.BankSyncTransaction{{ProviderAccountID: "card", ProviderTransactionID: "cloud-1", Date: "2026-11-04", AmountMinor: 4310, Currency: personal.Currency, Scale: personal.Scale,
			Description: "EXAMPLE CLOUD HOSTING", Counterparty: "Example Cloud"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		views, err := o.BankConnections(ctx, personal.ID)
		if err != nil {
			return err
		}
		return o.LinkBankAccount(ctx, personal.ID, who, views[0].Accounts[0].ID, card.ID)
	}); err != nil {
		t.Fatal(err)
	}
	var page ledger.BankActivityPage
	json.Unmarshal(f.db.Call(t, scope(agentP), "list_bank_activity", ledger.BankActivityQuery{Status: "all"}), &page)
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v", page.Items)
	}
	charge := page.Items[0]

	// Only the owner links the Books, and by account type: an income
	// account can't carry owner money.
	link := func(businessAccount string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{business.ID, personal.ID}, PayerBookID: personal.ID, Treatment: "contribution",
			PayerAccountID: investment.ID, BeneficiaryAccountID: businessAccount, Active: true})
		return bankCall(t, s, f.session, http.MethodPost, bookLinksPath, string(body))
	}
	if w := link(income.ID); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "equity account") {
		t.Fatal("income accepted for owner money", w.Code, w.Body.String())
	}
	if w := link(contributions.ID); w.Code != http.StatusOK {
		t.Fatal("link", w.Code, w.Body.String())
	}
	var seen ledger.BookLink
	json.Unmarshal(f.db.Call(t, scope(agentP), "get_book_link", map[string]any{}), &seen)
	if !seen.Linked || seen.LinkedBookID != business.ID || seen.PersonalAccountID != investment.ID || seen.BusinessAccountID != contributions.ID {
		t.Fatalf("agent's link = %+v", seen)
	}

	personalHalf := ledger.RecordBankActivity{RequestKey: "p-half", TransactionID: charge.ID, Description: "Cloud hosting for the business",
		OtherAccountID: investment.ID, PairKey: "pair-cloud-1", Reason: "The business pays Example Cloud monthly."}
	businessHalf := func(amount string) ledger.PostJournal {
		return ledger.PostJournal{RequestKey: "b-half-" + amount, EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Cloud hosting, paid personally",
			Lines:   []ledger.PostLine{{AccountID: hosting.ID, Side: "debit", AmountMinor: amount}, {AccountID: contributions.ID, Side: "credit", AmountMinor: amount}},
			PairKey: "pair-cloud-1", PairedTransactionID: charge.ID, Reason: "Cloud hosting is a business cost."}
	}
	prepare := func(c ledger.Connection, key, operation string, args any) (ledger.OperationProposal, error) {
		data, _ := json.Marshal(args)
		out, err := f.db.Store.Call(ctx, scope(c), "prepare_operation", mustJSON(ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: data}))
		var p ledger.OperationProposal
		if err == nil {
			json.Unmarshal(out, &p)
		}
		return p, err
	}
	refused := func(err error, words string) {
		t.Helper()
		var r *ledger.RefusedError
		if err == nil || !strings.Contains(err.Error(), words) && !(errors.As(err, &r) && strings.Contains(r.Reason, words)) {
			t.Fatalf("got %v, want a refusal saying %q", err, words)
		}
	}

	// Half of a pair is never recorded directly, and the business half
	// can't come first.
	_, err := f.db.Store.Call(ctx, scope(agentP), "record_bank_activity", mustJSON(personalHalf))
	refused(err, "always proposals")
	_, err = prepare(agentB, "early", "post_journal", businessHalf("4310"))
	refused(err, "prepare the half in the Book that paid first")

	first, err := prepare(agentP, "p", "record_bank_activity", personalHalf)
	if err != nil {
		t.Fatal(err)
	}
	// Alone, the personal half can't be approved: it would record the cost
	// in only one Book.
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(personal.ID, first, "approve-alone", 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "waiting for its other half") {
		t.Fatal("a waiting half was approved alone", w.Code, w.Body.String())
	}
	_, err = prepare(agentB, "wrong", "post_journal", businessHalf("4300"))
	refused(err, "with the whole amount")
	second, err := prepare(agentB, "b", "post_journal", businessHalf("4310"))
	if err != nil {
		t.Fatal(err)
	}

	// Approving the business half posts both.
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(business.ID, second, "approve-pair", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	var pairs int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entry_pairs WHERE tenant_id=$1 AND pair_key='pair-cloud-1'", f.tenant).Scan(&pairs); err != nil || pairs != 1 {
		t.Fatal("entry pair", pairs, err)
	}
	var decided int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.operation_decisions WHERE tenant_id=$1 AND proposal_id IN ($2,$3) AND decision='approve'", f.tenant, first.ID, second.ID).Scan(&decided); err != nil || decided != 2 {
		t.Fatal("both halves decided", decided, err)
	}
	json.Unmarshal(f.db.Call(t, scope(agentP), "list_bank_activity", ledger.BankActivityQuery{Status: "recorded"}), &page)
	if len(page.Items) != 1 || page.Items[0].ID != charge.ID {
		t.Fatalf("the card charge isn't recorded: %+v", page.Items)
	}
	var businessEntry, personalEntry string
	if err := f.db.Admin.QueryRow(ctx, "SELECT beneficiary_entry_id::text,payer_entry_id::text FROM public.entry_pairs WHERE tenant_id=$1", f.tenant).Scan(&businessEntry, &personalEntry); err != nil {
		t.Fatal(err)
	}
	balanced := func(when string, want int64) {
		t.Helper()
		for _, b := range []ledger.Book{business, personal} {
			var debit, credit int64
			if err := f.db.Admin.QueryRow(ctx, `SELECT coalesce(sum(amount_minor) FILTER (WHERE side='debit'),0),coalesce(sum(amount_minor) FILTER (WHERE side='credit'),0)
 FROM public.lines WHERE tenant_id=$1 AND book_id=$2`, f.tenant, b.ID).Scan(&debit, &credit); err != nil || debit != want || credit != want {
				t.Fatal(when, b.Name, debit, credit, err)
			}
		}
	}
	balanced("after approval", 4310)

	// Half of a pair is undone only with its partner: an agent can't
	// prepare it, and the owner can't unlink the card charge alone.
	_, err = prepare(agentB, "undo-half", "reverse_journal", ledger.ReverseJournal{RequestKey: "undo-half", EntryID: businessEntry, AccountingDate: "2026-11-05", Reason: "Example correction"})
	refused(err, "half of a pair")
	var bankLink string
	f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.bank_transaction_links WHERE tenant_id=$1 AND entry_id=$2 AND revoked_at IS NULL", f.tenant, personalEntry).Scan(&bankLink)
	change := func(book, operation string, args any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"book_id": book, "operation": operation, "arguments": args})
		return bankCall(t, s, f.session, http.MethodPost, "/api/v1/account-changes", string(data))
	}
	if w := change(personal.ID, "unlink_bank_activity", map[string]string{"request_key": "unlink-half", "link_id": bankLink, "reason": "Example correction"}); w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "half of a pair") {
		t.Fatal("unlinked half a pair", w.Code, w.Body.String())
	}
	// The owner's undo of the business entry reverses both, and doesn't
	// count against the agent's kind of activity.
	if w := change(business.ID, "reverse_journal", map[string]string{"request_key": "undo-pair", "entry_id": businessEntry, "accounting_date": "2026-11-05", "reason": "Not a business cost"}); w.Code != http.StatusOK {
		t.Fatal("undo pair", w.Code, w.Body.String())
	}
	balanced("after undo", 8620)
	var reversed int
	f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND reversal_of IN ($2,$3)", f.tenant, businessEntry, personalEntry).Scan(&reversed)
	var corrected int
	f.db.Admin.QueryRow(ctx, "SELECT coalesce(sum(corrected),0) FROM public.agent_kinds WHERE tenant_id=$1", f.tenant).Scan(&corrected)
	if reversed != 2 || corrected != 0 {
		t.Fatal("undo didn't reverse both, or counted as a correction", reversed, corrected)
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

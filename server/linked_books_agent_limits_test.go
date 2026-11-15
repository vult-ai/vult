package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// call makes one request through a Book's proposing agent.
func (lt *linkedTenant) call(book, method string, args any) (json.RawMessage, error) {
	return lt.f.db.Store.Call(lt.f.db.Context, lt.scope(lt.agents[book]), method, mustJSON(args))
}

// waiting counts a Book's proposals the owner hasn't decided.
func (lt *linkedTenant) waiting(book string) int {
	lt.t.Helper()
	var n int
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT count(*) FROM public.operation_proposals p WHERE p.tenant_id=$1 AND p.book_id=$2
 AND NOT EXISTS(SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id=p.tenant_id AND d.book_id=p.book_id AND d.proposal_id=p.id)`,
		lt.f.tenant, lt.books[book].ID).Scan(&n); err != nil {
		lt.t.Fatal(err)
	}
	return n
}

// terms sets one direction of a link, and the whole link on or off.
func (lt *linkedTenant) terms(payer, beneficiary string, payerAccount, beneficiaryAccount ledger.Account, direction, whole bool, revision int64) {
	lt.t.Helper()
	body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{lt.books[payer].ID, lt.books[beneficiary].ID}, PayerBookID: lt.books[payer].ID, Treatment: "owed",
		PayerAccountID: payerAccount.ID, BeneficiaryAccountID: beneficiaryAccount.ID, Active: direction, LinkActive: &whole, ExpectedRevision: revision})
	if w := bankCall(lt.t, lt.s, lt.f.session, http.MethodPost, bookLinksPath, string(body)); w.Code != http.StatusOK {
		lt.t.Fatal("change link terms", w.Code, w.Body.String())
	}
}

// An agent reads a linked Book only through its own Book's links: the other
// Book's name, profile and the one account the link names there, the net
// amount owed, and each paired cost. It reads none of the other Book's
// accounts, balances, bank activity or proposals, and no request takes
// another Book's id.
func TestLinkedBooksAgentReadsOnlyItsSideOfTheLink(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	homeLoan := lt.account("kit", "2500", "liability", "", "Home loan")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	bankLoan := lt.account("eightball", "2600", "liability", "", "Bank loan")
	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"})
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-07", AmountMinor: -25000, Description: "SHARED DESK"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	// Eightball's own entries: a loan, and a cost it says Kit paid that
	// Kit's Book never recorded.
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "post_journal", ledger.PostJournal{RequestKey: "loan", EconomicDate: "2026-11-01", AccountingDate: "2026-11-01", Description: "Loan drawdown",
		Lines: []ledger.PostLine{{AccountID: checking.ID, Side: "debit", AmountMinor: "900000"}, {AccountID: bankLoan.ID, Side: "credit", AmountMinor: "900000"}}})
	var unmatched ledger.Entry
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "post_journal", ledger.PostJournal{RequestKey: "unmatched", EconomicDate: "2026-11-02", AccountingDate: "2026-11-02", Description: "Recorded in Eightball only",
		Lines: []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "700"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "700"}}}), &unmatched)
	if unmatched.ID == "" {
		t.Fatal("no entry")
	}

	// A paired cost: each agent prepares its own Book's half.
	cloud := lt.item("kit", "EXAMPLE CLOUD")
	mine, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cloud", TransactionID: cloud.ID, Description: "Cloud hosting for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-cloud", Reason: "Eightball's cloud host"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := lt.prepare("eightball", "eb-cloud", "post_journal", ledger.PostJournal{RequestKey: "eb-cloud", EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Hosting, as Eightball worded it",
		Lines:   []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "4310"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "4310"}},
		PairKey: "pair-cloud", PairedTransactionID: cloud.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Each agent reads its own half, which names its partner; neither reads
	// the other Book's half.
	var state struct {
		Pair *ledger.ProposalPair `json:"pair"`
	}
	out, err := lt.call("kit", "get_operation_proposal", map[string]string{"proposal_id": mine.ID})
	if err != nil || json.Unmarshal(out, &state) != nil || state.Pair == nil || state.Pair.State != "paired" || state.Pair.BookID != lt.books["eightball"].ID ||
		state.Pair.BookName != lt.books["eightball"].Name || state.Pair.ProposalID != theirs.ID || state.Pair.Side != "beneficiary" {
		t.Fatalf("kit's half = %s, %v", out, err)
	}
	if _, err := lt.call("kit", "get_operation_proposal", map[string]string{"proposal_id": theirs.ID}); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("kit's agent read Eightball's half", err)
	}
	if _, err := lt.call("eightball", "get_operation_proposal", map[string]string{"proposal_id": mine.ID}); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("eightball's agent read Kit's half", err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, mine, "approve-cloud", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}

	// The links: the other Book's id, name and profile, and the account the
	// link names on each side. Nothing else about the other Book.
	out, err = lt.call("kit", "get_book_links", map[string]any{})
	var links struct {
		Links []ledger.LinkedBook `json:"links"`
	}
	if err != nil || json.Unmarshal(out, &links) != nil || len(links.Links) != 1 {
		t.Fatalf("links = %s, %v", out, err)
	}
	l := links.Links[0]
	if !l.Active || l.Other.ID != lt.books["eightball"].ID || l.Other.Name != lt.books["eightball"].Name || l.Other.Profile != "business" || l.TheyPayForMe != nil ||
		l.PaysForThem == nil || l.PaysForThem.MyAccountID != kitFromEightball.ID || l.PaysForThem.TheirAccountID != eightballToKit.ID || l.PaysForThem.Treatment != "owed" {
		t.Fatalf("kit's link = %s", out)
	}
	var one ledger.BookLink
	out, err = lt.call("kit", "get_book_link", map[string]any{})
	if err != nil || json.Unmarshal(out, &one) != nil || !one.Linked || !one.Active || one.LinkedBookID != lt.books["eightball"].ID || one.LinkedBookName != lt.books["eightball"].Name ||
		one.Mode != "owed" || one.PersonalAccountID != kitFromEightball.ID || one.BusinessAccountID != eightballToKit.ID {
		t.Fatalf("kit's single link = %s, %v", out, err)
	}

	// Who owes whom: the net amount is what the other Book's linked account
	// says, 4310 paired and 700 only Eightball recorded. The entry only one
	// side has is named by id, and each paired cost is described in the
	// words of the Book it was paid for.
	out, err = lt.call("kit", "get_linked_balances", map[string]any{})
	var balances ledger.LinkedBalances
	if err != nil || json.Unmarshal(out, &balances) != nil || len(balances.Links) != 1 || len(balances.Books) != 1 {
		t.Fatalf("balances = %s, %v", out, err)
	}
	owed := balances.Links[0]
	if owed.DebtorBookID != lt.books["eightball"].ID || owed.CreditorBookID != lt.books["kit"].ID || owed.AmountMinor != "5010" || owed.Matches ||
		len(owed.MismatchedEntryIDs) != 1 || owed.MismatchedEntryIDs[0] != unmatched.ID {
		t.Fatalf("owed = %s", out)
	}
	if len(owed.OpenPairs) != 1 || owed.OpenPairs[0].PairKey != "pair-cloud" || owed.OpenPairs[0].AmountMinor != "4310" || owed.OpenPairs[0].Description != "Hosting, as Eightball worded it" || owed.OpenPairs[0].HasReceipt {
		t.Fatalf("open pairs = %s", out)
	}
	// The liabilities listed are this Book's own, never the other Book's.
	if b := balances.Books[0]; b.BookID != lt.books["kit"].ID || len(b.Liabilities) != 2 || b.Liabilities[0].AccountID != card.ID || b.Liabilities[1].AccountID != homeLoan.ID {
		t.Fatalf("books = %s", out)
	}
	for _, private := range []string{bankLoan.ID, checking.ID, software.ID, "Bank loan", "900000", "Loan drawdown", "Recorded in Eightball only"} {
		if strings.Contains(string(out), private) {
			t.Fatalf("balances show %q of the other Book: %s", private, out)
		}
	}

	// The entry named by id can't be opened, and the other Book's words
	// aren't searchable, from this Book.
	if _, err := lt.call("kit", "get_journal", ledger.EntryQuery{EntryID: unmatched.ID}); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("kit's agent opened Eightball's entry", err)
	}
	var found struct {
		Transactions struct {
			Total int `json:"total"`
		} `json:"transactions"`
	}
	for _, words := range []string{"Recorded in Eightball only", "as Eightball worded it", "Loan drawdown"} {
		out, err = lt.call("kit", "search_book", map[string]string{"query": words})
		if err != nil || json.Unmarshal(out, &found) != nil || found.Transactions.Total != 0 {
			t.Fatalf("search %q = %s, %v", words, out, err)
		}
	}

	// A connection reads one Book. No read takes a Book id, and a
	// connection used against the linked Book is denied.
	other := lt.scope(lt.agents["kit"])
	other.BookID = lt.books["eightball"].ID
	for _, method := range []string{"get_book", "list_accounts", "search_book", "list_bank_activity", "get_trial_balance", "list_operation_proposals", "get_book_links", "get_linked_balances", "get_book_link"} {
		if _, err := lt.call("kit", method, map[string]string{"book_id": lt.books["eightball"].ID}); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal(method, "took a Book id", err)
		}
		if _, err := lt.f.db.Store.Call(lt.f.db.Context, other, method, mustJSON(map[string]any{})); !errors.Is(err, ledger.ErrDenied) {
			t.Fatal(method, "answered for the linked Book", err)
		}
	}

	// A Book with no link reads none.
	out, err = lt.call("studio", "get_book_links", map[string]any{})
	if err != nil || string(out) != `{"links":[]}` {
		t.Fatalf("studio links = %s, %v", out, err)
	}
	out, err = lt.call("studio", "get_book_link", map[string]any{})
	if err != nil || string(out) != `{"linked":false}` {
		t.Fatalf("studio single link = %s, %v", out, err)
	}
	out, err = lt.call("studio", "get_linked_balances", map[string]any{})
	if err != nil || json.Unmarshal(out, &balances) != nil || len(balances.Links) != 0 || len(balances.Books) != 1 || balances.Books[0].BookID != lt.books["studio"].ID {
		t.Fatalf("studio balances = %s, %v", out, err)
	}
}

// An agent prepares only its own Book's half of a pair. Until the other
// Book's own agent prepares the other half, nothing waits there and the owner
// can't approve. The account credited in the other Book is the one the link
// names, the amount is the bank item's, and approval posts exactly the two
// prepared halves.
func TestLinkedBooksAgentPreparesOnlyItsOwnHalf(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	groceries := lt.account("kit", "6900", "expense", "", "Groceries")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	equipment := lt.account("eightball", "1700", "asset", "", "Equipment")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	otherPayable := lt.account("eightball", "2400", "liability", "payable", "Owed to a supplier")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioPayable := lt.account("studio", "2310", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-05", AmountMinor: 1500, Description: "EXAMPLE BOOKS", Counterparty: "Example Books"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	cloud := lt.item("kit", "EXAMPLE CLOUD")
	payerHalf := func(key string, account ledger.Account) ledger.RecordBankActivity {
		return ledger.RecordBankActivity{RequestKey: key, TransactionID: cloud.ID, Description: "Cloud hosting for Eightball", OtherAccountID: account.ID, PairKey: "pair-cloud", Reason: "Eightball's cloud host"}
	}
	otherHalf := func(key, amount string, debit, credit ledger.Account) ledger.PostJournal {
		return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Cloud hosting, paid by Kit",
			Lines:   []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: amount}, {AccountID: credit.ID, Side: "credit", AmountMinor: amount}},
			PairKey: "pair-cloud", PairedTransactionID: cloud.ID, PayerBookID: lt.books["kit"].ID}
	}

	// The paying Book's half goes to the account the link names in it, for
	// the bank item's whole amount; it has no field for the other Book's
	// account or amount.
	_, err := lt.prepare("kit", "kit-wrong-account", "record_bank_activity", payerHalf("kit-wrong-account", groceries))
	refusedWith(t, err, "other_account_id is no linked Book's account in this Book, or that link is turned off")
	split := payerHalf("kit-split", kitFromEightball)
	split.OtherAccountID = ""
	split.Lines = []ledger.PostLine{{AccountID: card.ID, Side: "credit", AmountMinor: "4310"}, {AccountID: kitFromEightball.ID, Side: "debit", AmountMinor: "310"}, {AccountID: groceries.ID, Side: "debit", AmountMinor: "4000"}}
	_, err = lt.prepare("kit", "kit-split", "record_bank_activity", split)
	refusedWith(t, err, "other_account_id is no linked Book's account in this Book")
	extra := map[string]any{"request_key": "kit-extra", "transaction_id": cloud.ID, "description": "Cloud hosting", "other_account_id": kitFromEightball.ID, "pair_key": "pair-cloud",
		"beneficiary_account_id": software.ID}
	if _, err = lt.prepare("kit", "kit-extra", "record_bank_activity", extra); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("the paying half named an account in the other Book", err)
	}
	if lt.waiting("kit") != 0 || lt.waiting("eightball") != 0 {
		t.Fatal("a refused half was kept", lt.waiting("kit"), lt.waiting("eightball"))
	}

	mine, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", payerHalf("kit-cloud", kitFromEightball))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing waits in the other Book yet, for its owner or its agents, and
	// the lone half can't be approved.
	var state struct {
		Pair *ledger.ProposalPair `json:"pair"`
	}
	out, err := lt.call("kit", "get_operation_proposal", map[string]string{"proposal_id": mine.ID})
	if err != nil || json.Unmarshal(out, &state) != nil || state.Pair == nil || state.Pair.State != "waiting_for_pair" || state.Pair.BookID != lt.books["eightball"].ID ||
		state.Pair.BookName != lt.books["eightball"].Name || state.Pair.ProposalID != "" || !state.Pair.LinkActive {
		t.Fatalf("waiting half = %s, %v", out, err)
	}
	if lt.waiting("kit") != 1 || lt.waiting("eightball") != 0 {
		t.Fatal("waiting", lt.waiting("kit"), lt.waiting("eightball"))
	}
	if out, err = lt.call("eightball", "list_operation_proposals", map[string]any{}); err != nil || string(out) != `{"proposals":[]}` {
		t.Fatalf("eightball's agent sees %s, %v", out, err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, mine, "approve-alone", 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "waiting for its other half") {
		t.Fatal("a lone half was approved", w.Code, w.Body.String())
	}

	// The paying Book's agent can't prepare the other Book's half: not from
	// its own Book, and not with its connection pointed at the other Book.
	_, err = lt.prepare("kit", "kit-theirs", "post_journal", otherHalf("kit-theirs", "4310", software, eightballToKit))
	refusedWith(t, err, "through your connection to that Book")
	_, err = lt.prepare("kit", "kit-own", "post_journal", otherHalf("kit-own", "4310", groceries, kitFromEightball))
	refusedWith(t, err, "prepare the half in the Book that paid first")
	other := lt.scope(lt.agents["kit"])
	other.BookID = lt.books["eightball"].ID
	if _, err = lt.f.db.Store.Call(lt.f.db.Context, other, "prepare_operation", mustJSON(ledger.PrepareOperation{RequestKey: "kit-reach", Operation: "post_journal",
		Arguments: mustJSON(otherHalf("kit-reach", "4310", software, eightballToKit))})); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("kit's connection prepared in Eightball", err)
	}
	// A pair is never recorded directly, even by a connection that writes.
	if _, err = lt.f.db.Store.Call(lt.f.db.Context, lt.scope(lt.writers["eightball"]), "post_journal", mustJSON(otherHalf("eb-direct", "4310", software, eightballToKit))); err == nil || !strings.Contains(err.Error(), "always proposals") {
		t.Fatal("half a pair was recorded directly", err)
	}

	// A Book that isn't linked to the paying Book can't join the pair. Its
	// refusal differs from the one for a pair key nobody is waiting on.
	_, err = lt.prepare("studio", "st-cloud", "post_journal", otherHalf("st-cloud", "4310", tools, studioPayable))
	refusedWith(t, err, "these Books aren't linked for this direction, or the link is turned off")
	unknown := otherHalf("st-unknown", "4310", tools, studioPayable)
	unknown.PairKey = "pair-nobody"
	_, err = lt.prepare("studio", "st-unknown", "post_journal", unknown)
	refusedWith(t, err, "no waiting half has pair_key pair-nobody")

	// The other Book's own agent prepares its half: the bank item's whole
	// amount, credited to the account the link names, debited to a cost.
	_, err = lt.prepare("eightball", "eb-less", "post_journal", otherHalf("eb-less", "4300", software, eightballToKit))
	refusedWith(t, err, "with the whole amount")
	_, err = lt.prepare("eightball", "eb-more", "post_journal", otherHalf("eb-more", "9999", software, eightballToKit))
	refusedWith(t, err, "with the whole amount")
	_, err = lt.prepare("eightball", "eb-credit", "post_journal", otherHalf("eb-credit", "4310", software, otherPayable))
	refusedWith(t, err, "credits the account the link names here")
	_, err = lt.prepare("eightball", "eb-cash", "post_journal", otherHalf("eb-cash", "4310", checking, eightballToKit))
	refusedWith(t, err, "debits only expenses")
	wrongItem := otherHalf("eb-item", "4310", software, eightballToKit)
	wrongItem.PairedTransactionID = lt.item("kit", "EXAMPLE BOOKS").ID
	_, err = lt.prepare("eightball", "eb-item", "post_journal", wrongItem)
	refusedWith(t, err, "paired_transaction_id isn't the bank item its other half records")
	wrongDate := otherHalf("eb-date", "4310", software, eightballToKit)
	wrongDate.EconomicDate, wrongDate.AccountingDate = "2026-11-09", "2026-11-09"
	_, err = lt.prepare("eightball", "eb-date", "post_journal", wrongDate)
	refusedWith(t, err, "isn't the bank item's date")
	if lt.waiting("eightball") != 0 {
		t.Fatal("a refused half was kept", lt.waiting("eightball"))
	}
	// Which cost it is in the other Book is that Book's agent's choice.
	theirs, err := lt.prepare("eightball", "eb-cloud", "post_journal", otherHalf("eb-cloud", "4310", equipment, eightballToKit))
	if err != nil {
		t.Fatal(err)
	}
	_, err = lt.prepare("eightball", "eb-again", "post_journal", otherHalf("eb-again", "4310", software, eightballToKit))
	refusedWith(t, err, "its other half is already paired")
	if lt.waiting("kit") != 1 || lt.waiting("eightball") != 1 {
		t.Fatal("a pair is one waiting proposal in each Book", lt.waiting("kit"), lt.waiting("eightball"))
	}
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "2300") != 0 {
		t.Fatal("a waiting pair posted")
	}

	// The owner's one approval, in either Book, posts both halves as prepared.
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, mine, "approve-pair", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 4310 || lt.balance("kit", "2100") != -4310 || lt.balance("eightball", "1700") != 4310 || lt.balance("eightball", "2300") != -4310 ||
		lt.balance("eightball", "6100") != 0 || lt.balance("eightball", "1100") != 0 || lt.balance("eightball", "2400") != 0 {
		t.Fatal("posted", lt.balance("kit", "1300"), lt.balance("eightball", "1700"), lt.balance("eightball", "2300"))
	}
	var entries, lines int
	var actor string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT count(DISTINCT e.id),count(*),min(e.actor_id::text) FROM public.entries e JOIN public.lines x ON x.tenant_id=e.tenant_id AND x.book_id=e.book_id AND x.entry_id=e.id
 WHERE e.tenant_id=$1 AND e.book_id=$2`, lt.f.tenant, lt.books["eightball"].ID).Scan(&entries, &lines, &actor); err != nil || entries != 1 || lines != 2 || actor != lt.agents["eightball"].ActorID {
		t.Fatal("the other Book's entry is its own agent's, once", entries, lines, actor, err)
	}
	if lt.waiting("kit") != 0 || lt.waiting("eightball") != 0 {
		t.Fatal("both halves are decided", lt.waiting("kit"), lt.waiting("eightball"))
	}
	_ = theirs
}

// With a direction or the whole link turned off, or the link removed, no
// new half of a pair can be prepared in either Book and a half already
// waiting can't be completed. The link's reads say it is off; removed, they
// say nothing about the other Book.
func TestLinkedBooksAgentIsRefusedWhenTheLinkIsOffOrRemoved(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-05", AmountMinor: 1500, Description: "EXAMPLE BOOKS", Counterparty: "Example Books"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	cloud, books := lt.item("kit", "EXAMPLE CLOUD"), lt.item("kit", "EXAMPLE BOOKS")
	// One half is already waiting when the link changes.
	waitingHalf, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cloud", TransactionID: cloud.ID, Description: "Cloud hosting for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	attempt := 0
	refusedBothWays := func(when string, linked bool) {
		t.Helper()
		attempt++
		key := fmt.Sprintf("off-%d", attempt)
		_, err := lt.prepare("kit", "kit-"+key, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-" + key, TransactionID: books.ID, Description: "Books for Eightball",
			OtherAccountID: kitFromEightball.ID, PairKey: "pair-" + key})
		refusedWith(t, err, "other_account_id is no linked Book's account in this Book, or that link is turned off")
		_, err = lt.prepare("eightball", "eb-"+key, "post_journal", ledger.PostJournal{RequestKey: "eb-" + key, EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Cloud hosting, paid by Kit",
			Lines:   []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "4310"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "4310"}},
			PairKey: "pair-cloud", PairedTransactionID: cloud.ID})
		refusedWith(t, err, "these Books aren't linked for this direction, or the link is turned off")
		if linked {
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, waitingHalf, "approve-"+key, 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "waiting for its other half") {
				t.Fatal(when, "the waiting half was approved", w.Code, w.Body.String())
			}
		}
		if lt.waiting("kit") != 1 || lt.waiting("eightball") != 0 || lt.balance("kit", "1300") != 0 || lt.balance("eightball", "2300") != 0 {
			t.Fatal(when, "something was kept or posted", lt.waiting("kit"), lt.waiting("eightball"))
		}
	}
	read := func() (ledger.LinkedBook, ledger.BookLink, ledger.LinkedBalances, bool) {
		t.Helper()
		var links struct {
			Links []ledger.LinkedBook `json:"links"`
		}
		var one ledger.BookLink
		var balances ledger.LinkedBalances
		var state struct {
			Pair *ledger.ProposalPair `json:"pair"`
		}
		for method, into := range map[string]any{"get_book_links": &links, "get_book_link": &one, "get_linked_balances": &balances} {
			out, err := lt.call("kit", method, map[string]any{})
			if err != nil || json.Unmarshal(out, into) != nil {
				t.Fatal(method, string(out), err)
			}
		}
		out, err := lt.call("kit", "get_operation_proposal", map[string]string{"proposal_id": waitingHalf.ID})
		if err != nil || json.Unmarshal(out, &state) != nil {
			t.Fatal(string(out), err)
		}
		var first ledger.LinkedBook
		if len(links.Links) == 1 {
			first = links.Links[0]
		}
		return first, one, balances, state.Pair != nil && state.Pair.LinkActive
	}

	// This direction turned off: the link is still listed, with the other
	// Book's name, and says the direction is off.
	lt.terms("kit", "eightball", kitFromEightball, eightballToKit, false, true, 1)
	l, one, balances, pairLive := read()
	if !l.Active || l.PaysForThem == nil || l.PaysForThem.Active || l.Other.Name != lt.books["eightball"].Name || !one.Linked || one.Active || len(balances.Links) != 1 || pairLive {
		t.Fatalf("direction off: %+v %+v %+v %v", l, one, balances, pairLive)
	}
	refusedBothWays("direction off", true)

	// The whole link turned off.
	lt.terms("kit", "eightball", kitFromEightball, eightballToKit, true, false, 2)
	l, one, balances, pairLive = read()
	if l.Active || l.PaysForThem == nil || !l.PaysForThem.Active || l.Other.ID != lt.books["eightball"].ID || !one.Linked || one.Active || len(balances.Links) != 1 || pairLive {
		t.Fatalf("link off: %+v %+v %+v %v", l, one, balances, pairLive)
	}
	refusedBothWays("link off", true)
	_, err = lt.call("kit", "prepare_opening_balance", ledger.OpeningBalanceHalf{RequestKey: "kit-opening-off", OtherBookID: lt.books["eightball"].ID, OwedBy: "other_book", AsOf: "2026-01-01",
		AmountMinor: "50000", PairKey: "opening-off", Reason: "Owed at the start"})
	refusedWith(t, err, "These Books aren't linked")

	// A link with half of a pair still waiting can't be removed: that half
	// would later be approved as an entry in one Book alone. The owner
	// decides it first.
	if w := ownerAPICall(lt.s, lt.f.session, http.MethodDelete, bookLinksPath+"?id="+l.LinkID+"&book_id="+lt.books["kit"].ID, "", true); w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "still wait for a decision") {
		t.Fatal("removing a link with a waiting half", w.Code, w.Body.String())
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/reject", proposalDecisionBody(lt.books["kit"].ID, waitingHalf, "reject-cloud", 0)); w.Code != http.StatusOK {
		t.Fatal("reject the waiting half", w.Code, w.Body.String())
	}

	// The link removed: the reads no longer name the other Book.
	if w := ownerAPICall(lt.s, lt.f.session, http.MethodDelete, bookLinksPath+"?id="+l.LinkID+"&book_id="+lt.books["kit"].ID, "", true); w.Code != http.StatusOK {
		t.Fatal("remove the link", w.Code, w.Body.String())
	}
	l, one, balances, pairLive = read()
	if l.LinkID != "" || one.Linked || one.LinkedBookID != "" || one.LinkedBookName != "" || len(balances.Links) != 0 || len(balances.Books) != 1 || balances.Books[0].BookID != lt.books["kit"].ID || pairLive {
		t.Fatalf("removed: %+v %+v %+v %v", l, one, balances, pairLive)
	}
	// A new half is refused in the Book that paid, as before.
	_, err = lt.prepare("kit", "kit-gone", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-gone", TransactionID: books.ID, Description: "Books for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-gone"})
	refusedWith(t, err, "other_account_id is no linked Book's account in this Book, or that link is turned off")
	_, err = lt.call("kit", "prepare_opening_balance", ledger.OpeningBalanceHalf{RequestKey: "kit-opening-gone", OtherBookID: lt.books["eightball"].ID, OwedBy: "other_book", AsOf: "2026-01-01",
		AmountMinor: "50000", PairKey: "opening-gone", Reason: "Owed at the start"})
	refusedWith(t, err, "These Books aren't linked")

	// Nothing was recorded in either Book, and nothing waits.
	var pairs, entries int
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT (SELECT count(*) FROM public.entry_pairs WHERE tenant_id=$1),(SELECT count(*) FROM public.entries WHERE tenant_id=$1)`,
		lt.f.tenant).Scan(&pairs, &entries); err != nil || pairs != 0 || entries != 0 || lt.waiting("kit") != 0 {
		t.Fatal("after the link was removed", pairs, entries, lt.waiting("kit"), err)
	}
}

// Each agent's waiting proposals are limited in its own Book. A pair is one
// proposal in each Book, counted against the agent that prepared that half,
// so an agent that fills its own Book's limit leaves the linked Book's agent
// free to prepare.
func TestLinkedBooksAgentWaitingLimitIsPerBook(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	groceries := lt.account("kit", "6900", "expense", "", "Groceries")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	journal := func(key string) ledger.PostJournal {
		return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-11-03", AccountingDate: "2026-11-03", Description: "Groceries " + key,
			Lines: []ledger.PostLine{{AccountID: groceries.ID, Side: "debit", AmountMinor: "100"}, {AccountID: card.ID, Side: "credit", AmountMinor: "100"}}}
	}
	// One short of the limit in Kit's Book, then the paying half of a pair.
	const limit = 128
	for i := 0; i < limit-1; i++ {
		key := fmt.Sprintf("fill-%03d", i)
		if _, err := lt.prepare("kit", key, "post_journal", journal(key)); err != nil {
			t.Fatal(i, err)
		}
	}
	cloud := lt.item("kit", "EXAMPLE CLOUD")
	if _, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cloud", TransactionID: cloud.ID, Description: "Cloud hosting for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-cloud"}); err != nil {
		t.Fatal("the paying half is the last proposal within the limit", err)
	}
	if _, err := lt.prepare("kit", "over", "post_journal", journal("over")); !errors.Is(err, ledger.ErrLimit) {
		t.Fatal("over the limit", err)
	}
	if lt.waiting("kit") != limit || lt.waiting("eightball") != 0 {
		t.Fatal("waiting", lt.waiting("kit"), lt.waiting("eightball"))
	}
	// The linked Book's agent is unaffected: its half is its Book's first.
	if _, err := lt.prepare("eightball", "eb-cloud", "post_journal", ledger.PostJournal{RequestKey: "eb-cloud", EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Cloud hosting, paid by Kit",
		Lines:   []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "4310"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "4310"}},
		PairKey: "pair-cloud", PairedTransactionID: cloud.ID}); err != nil {
		t.Fatal("the linked Book's agent was blocked", err)
	}
	if lt.waiting("kit") != limit || lt.waiting("eightball") != 1 {
		t.Fatal("a pair is one proposal in each Book", lt.waiting("kit"), lt.waiting("eightball"))
	}
}

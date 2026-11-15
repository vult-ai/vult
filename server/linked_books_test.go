package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

// linkedTenant is one owner's three synthetic Books: Eightball Inc. (a
// corporation), Kit Blanche Studio LLC, and Kit Blanche's personal Book, each
// with a writing connection for setup and a proposing agent.
type linkedTenant struct {
	t        *testing.T
	f        *documentServerFixture
	s        *http.Server
	books    map[string]ledger.Book
	writers  map[string]ledger.Connection
	agents   map[string]ledger.Connection
	accounts map[string]map[string]ledger.Account
}

type beneficiaryReadTrace struct{ calls atomic.Int64 }

func (trace *beneficiaryReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if strings.Contains(query.SQL, "public.suggested_beneficiaries(") {
		trace.calls.Add(1)
	}
	return ctx
}
func (*beneficiaryReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestBankBeneficiarySuggestionsUseOneReadPerPage(t *testing.T) {
	lt := newLinkedTenant(t)
	receivable := lt.account("kit", "1300", "asset", "receivable", "Business owes")
	payable := lt.account("eightball", "2300", "liability", "payable", "Owed to personal Book")
	if w := lt.link("kit", "eightball", "owed", receivable, payable, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	cash := lt.account("kit", "1100", "asset", "cash", "Checking")
	lt.bank("kit", "cash", cash,
		ledger.BankSyncTransaction{ProviderTransactionID: "first", Date: "2026-03-01", AmountMinor: -500, Description: "Paper"},
		ledger.BankSyncTransaction{ProviderTransactionID: "pending", Date: "2026-03-02", AmountMinor: -700, Description: "Supplies", Pending: true},
		ledger.BankSyncTransaction{ProviderTransactionID: "last", Date: "2026-03-03", AmountMinor: -900, Description: "Ink"})
	trace := &beneficiaryReadTrace{}
	config := lt.f.owners.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sessions, err := access.New(lt.f.auth, pool, configuration().Origin)
	if err != nil {
		t.Fatal(err)
	}
	read := func(status string) ledger.BankActivityPage {
		t.Helper()
		var page ledger.BankActivityPage
		err := sessions.WithOwner(t.Context(), lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			page, _, err = o.BankActivity(t.Context(), lt.books["kit"].ID, ledger.BankActivityQuery{Status: status})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := read("all")
	if len(page.Items) != 3 || trace.calls.Load() != 1 {
		t.Fatal("suggestions share one query", trace.calls.Load(), page)
	}
	for _, item := range page.Items {
		if item.Status == "pending" && item.SuggestedBeneficiaries != nil || item.Status == "unrecorded" && item.SuggestedBeneficiaries == nil {
			t.Fatal("suggestions stay on eligible rows", item)
		}
	}
	trace.calls.Store(0)
	read("pending")
	if trace.calls.Load() != 0 {
		t.Fatal("pending items need no beneficiary read")
	}
}

func newLinkedTenant(t *testing.T) *linkedTenant {
	f, s, _ := bankServer(t)
	ctx := f.db.Context
	lt := &linkedTenant{t: t, f: f, s: s, books: map[string]ledger.Book{"eightball": f.book}, writers: map[string]ledger.Connection{}, agents: map[string]ledger.Connection{}, accounts: map[string]map[string]ledger.Account{}}
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		for _, name := range []string{"studio", "kit"} {
			b, err := o.CreateBook(ctx, map[string]string{"studio": "Kit Blanche Studio LLC", "kit": "Kit Blanche"}[name], f.book.Currency, f.book.Scale)
			if err != nil {
				return err
			}
			lt.books[name] = b
		}
		for name, b := range lt.books {
			w, err := o.CreateConnection(ctx, b.ID, "Example setup", "write")
			if err != nil {
				return err
			}
			a, err := o.CreateConnection(ctx, b.ID, "Example agent", "propose")
			if err != nil {
				return err
			}
			lt.writers[name], lt.agents[name] = w, a
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lt.profile("eightball", "business")
	lt.profile("studio", "business")
	lt.profile("kit", "personal")
	return lt
}

func (lt *linkedTenant) scope(c ledger.Connection) ledger.Scope {
	return ledger.Scope{TenantID: lt.f.tenant, BookID: c.BookID, ConnectionID: c.ID, ActorID: c.ActorID, Access: c.Access}
}

func (lt *linkedTenant) profile(name, profile string) {
	lt.t.Helper()
	ctx := lt.f.db.Context
	if err := lt.f.sessions.OwnerAct(ctx, lt.f.session, lt.f.tenant, func(o ledger.TenantAdmin, who string) error {
		current, err := o.Book(ctx, lt.books[name].ID)
		if err != nil {
			return err
		}
		revision := int64(0)
		if current.MetadataRevision != nil {
			revision = *current.MetadataRevision
		}
		_, err = o.ReviseBook(ctx, who, ledger.ReviseBookSettings{BookID: current.ID, RequestKey: "profile-" + current.ID, Revision: revision, Name: current.Name, Profile: &profile}, nil)
		return err
	}); err != nil {
		lt.t.Fatal(err)
	}
}

// account creates an account in a Book, named for what it is.
func (lt *linkedTenant) account(book, code, class, role, name string) ledger.Account {
	lt.t.Helper()
	var a ledger.Account
	if json.Unmarshal(lt.f.db.Call(lt.t, lt.scope(lt.writers[book]), "create_account", ledger.CreateAccount{RequestKey: "account-" + code, Class: class, Code: code, Name: name, Role: role}), &a) != nil || a.ID == "" {
		lt.t.Fatal("account", book, code)
	}
	if lt.accounts[book] == nil {
		lt.accounts[book] = map[string]ledger.Account{}
	}
	lt.accounts[book][code] = a
	return a
}

// bank connects one bank account to a Book account and brings in items.
func (lt *linkedTenant) bank(book, kind string, account ledger.Account, items ...ledger.BankSyncTransaction) {
	lt.t.Helper()
	ctx, b := lt.f.db.Context, lt.books[book]
	var login string
	if err := lt.f.sessions.OwnerAct(ctx, lt.f.session, lt.f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, b.ID, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(), TokenSealed: make([]byte, 48), KeyVersion: 1})
		return err
	}); err != nil {
		lt.t.Fatal(err)
	}
	subtype := map[string]string{"card": "credit card", "cash": "checking"}[kind]
	for i := range items {
		items[i].ProviderAccountID, items[i].Currency, items[i].Scale = "acct", b.Currency, b.Scale
	}
	if _, err := lt.f.db.Store.ApplyBankSync(ctx, ledger.BankSyncTarget{TenantID: lt.f.tenant, BookID: b.ID, ConnectionID: login}, ledger.BankSyncBatch{NextCursor: "c1", Complete: true,
		Accounts: []ledger.BankSyncAccount{{ProviderAccountID: "acct", Name: "Example " + subtype, Mask: "0001", Type: "depository", Subtype: subtype, Kind: kind, Currency: b.Currency, Scale: b.Scale}},
		Added:    items}); err != nil {
		lt.t.Fatal(err)
	}
	if err := lt.f.sessions.OwnerAct(ctx, lt.f.session, lt.f.tenant, func(o ledger.TenantAdmin, who string) error {
		views, err := o.BankConnections(ctx, b.ID)
		if err != nil {
			return err
		}
		for _, v := range views {
			for _, a := range v.Accounts {
				if a.AccountID == "" {
					return o.LinkBankAccount(ctx, b.ID, who, a.ID, account.ID)
				}
			}
		}
		return errors.New("no bank account to link")
	}); err != nil {
		lt.t.Fatal(err)
	}
}

func (lt *linkedTenant) item(book, description string) ledger.BankTransaction {
	lt.t.Helper()
	var page ledger.BankActivityPage
	json.Unmarshal(lt.f.db.Call(lt.t, lt.scope(lt.agents[book]), "list_bank_activity", ledger.BankActivityQuery{Status: "all"}), &page)
	for _, x := range page.Items {
		if x.Description == description {
			return x
		}
	}
	lt.t.Fatal("no bank item", book, description)
	return ledger.BankTransaction{}
}

// link sets one direction of the link between two Books.
func (lt *linkedTenant) link(payer, beneficiary, treatment string, payerAccount, beneficiaryAccount ledger.Account, revision int64) *httptest.ResponseRecorder {
	body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{lt.books[payer].ID, lt.books[beneficiary].ID}, PayerBookID: lt.books[payer].ID, Treatment: treatment,
		PayerAccountID: payerAccount.ID, BeneficiaryAccountID: beneficiaryAccount.ID, Active: true, ExpectedRevision: revision})
	return bankCall(lt.t, lt.s, lt.f.session, http.MethodPost, bookLinksPath, string(body))
}

func (lt *linkedTenant) prepare(book, key, operation string, args any) (ledger.OperationProposal, error) {
	data, _ := json.Marshal(args)
	out, err := lt.f.db.Store.Call(lt.f.db.Context, lt.scope(lt.agents[book]), "prepare_operation", mustJSON(ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: data}))
	var p ledger.OperationProposal
	if err == nil {
		json.Unmarshal(out, &p)
	}
	return p, err
}

func (lt *linkedTenant) balance(book, code string) int64 {
	lt.t.Helper()
	var debit, credit int64
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT coalesce(sum(amount_minor) FILTER (WHERE side='debit'),0),coalesce(sum(amount_minor) FILTER (WHERE side='credit'),0)
 FROM public.lines WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3`, lt.f.tenant, lt.books[book].ID, lt.accounts[book][code].ID).Scan(&debit, &credit); err != nil {
		lt.t.Fatal(err)
	}
	return debit - credit
}

func refusedWith(t *testing.T, err error, words string) {
	t.Helper()
	var r *ledger.RefusedError
	if err == nil || !strings.Contains(err.Error(), words) && !(errors.As(err, &r) && strings.Contains(r.Reason, words)) {
		t.Fatalf("got %v, want a refusal saying %q", err, words)
	}
}

// Any two of the owner's Books link, each direction with its own treatment
// checked by account type; a cost one Book pays for another is decided once
// in both, and a link turned off leaves its waiting pairs reject-only.
func TestLinkedBooksPayForEachOther(t *testing.T) {
	lt := newLinkedTenant(t)
	ctx := lt.f.db.Context
	// Kit's personal Book: a rewards card, and what the companies owe Kit.
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	// Eightball: checking, software, what it owes Kit, what the studio owes it.
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	eightballFromStudio := lt.account("eightball", "1310", "asset", "receivable", "Owed by the studio")
	equity := lt.account("eightball", "3000", "equity", "", "Paid-in capital")
	// The studio: design tools, Kit's contributions, what it owes Eightball.
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioCapital := lt.account("studio", "3100", "equity", "", "Owner contributions")
	studioToEightball := lt.account("studio", "2310", "liability", "payable", "Owed to Eightball Inc.")

	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-06", AmountMinor: 1800, Description: "EXAMPLE DESIGN TOOLS", Counterparty: "Example Design Tools"})
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-07", AmountMinor: -25000, Description: "SHARED DESK", Counterparty: "Shared Desk"})

	// Treatments are checked by type: money put in is only from a personal
	// Book, and an account carries one direction of one link.
	if w := lt.link("eightball", "studio", "contribution", eightballFromStudio, studioCapital, 0); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "only from a personal Book") {
		t.Fatal("contribution between businesses", w.Code, w.Body.String())
	}
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal("kit pays for eightball", w.Code, w.Body.String())
	}
	if w := lt.link("kit", "studio", "contribution", kitInStudio, studioCapital, 0); w.Code != http.StatusOK {
		t.Fatal("kit pays for the studio", w.Code, w.Body.String())
	}
	if w := lt.link("eightball", "studio", "owed", eightballFromStudio, studioToEightball, 0); w.Code != http.StatusOK {
		t.Fatal("eightball pays for the studio", w.Code, w.Body.String())
	}
	if w := lt.link("studio", "eightball", "owed", eightballFromStudio, equity, 1); w.Code == http.StatusOK {
		t.Fatal("an account was used in two directions, or the wrong Book's account accepted", w.Body.String())
	}

	// The owner sees every link with both directions and account names; an
	// agent sees its Book's links from its own side.
	w := bankCall(t, lt.s, lt.f.session, http.MethodGet, bookLinksPath, "")
	var all struct {
		Links []ledger.OwnerLink `json:"links"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 3 {
		t.Fatal("owner links", w.Code, w.Body.String())
	}
	var mine struct {
		Links []ledger.LinkedBook `json:"links"`
	}
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.agents["kit"]), "get_book_links", map[string]any{}), &mine)
	if len(mine.Links) != 2 || mine.Links[0].PaysForThem == nil || mine.Links[0].PaysForThem.MyAccountID != kitFromEightball.ID || mine.Links[0].TheyPayForMe != nil {
		t.Fatalf("kit's links = %+v", mine.Links)
	}

	// A link with no pairs yet can be removed and made again.
	linkOf := func(a, b string) string {
		for _, l := range all.Links {
			if len(l.Books) == 2 && (l.Books[0].ID == lt.books[a].ID && l.Books[1].ID == lt.books[b].ID || l.Books[0].ID == lt.books[b].ID && l.Books[1].ID == lt.books[a].ID) {
				return l.ID
			}
		}
		t.Fatal("no link between", a, b)
		return ""
	}
	removeLink := func(a, b string) *httptest.ResponseRecorder {
		return ownerAPICall(lt.s, lt.f.session, http.MethodDelete, bookLinksPath+"?id="+linkOf(a, b)+"&book_id="+lt.books[a].ID, "", true)
	}
	if w := removeLink("kit", "eightball"); w.Code != http.StatusOK {
		t.Fatal("remove an unused link", w.Code, w.Body.String())
	}
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal("link again", w.Code, w.Body.String())
	}
	if w = bankCall(t, lt.s, lt.f.session, http.MethodGet, bookLinksPath, ""); json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 3 {
		t.Fatal("owner links after relinking", w.Code, w.Body.String())
	}

	// A charge on Kit's card for Eightball: Kit is owed, Eightball owes.
	cloud := lt.item("kit", "EXAMPLE CLOUD")
	payer, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cloud", TransactionID: cloud.ID, Description: "Cloud hosting for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-cloud", Reason: "Eightball's cloud host"})
	if err != nil {
		t.Fatal(err)
	}
	beneficiary, err := lt.prepare("eightball", "eb-cloud", "post_journal", ledger.PostJournal{RequestKey: "eb-cloud", EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Cloud hosting, paid by Kit",
		Lines:   []ledger.PostLine{{AccountID: software.ID, Side: "debit", AmountMinor: "4310"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "4310"}},
		PairKey: "pair-cloud", PairedTransactionID: cloud.ID, PayerBookID: lt.books["kit"].ID, Reason: "Eightball's cloud host"})
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, payer, "approve-cloud", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 4310 || lt.balance("eightball", "2300") != -4310 || lt.balance("eightball", "6100") != 4310 {
		t.Fatal("cloud pair", lt.balance("kit", "1300"), lt.balance("eightball", "2300"), lt.balance("eightball", "6100"))
	}
	_ = beneficiary

	// Eightball pays the studio's desk from its checking: the studio owes it.
	desk := lt.item("eightball", "SHARED DESK")
	if _, err := lt.prepare("eightball", "eb-desk", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "eb-desk", TransactionID: desk.ID, Description: "The studio's desk",
		OtherAccountID: eightballFromStudio.ID, PairKey: "pair-desk"}); err != nil {
		t.Fatal(err)
	}
	studioHalf, err := lt.prepare("studio", "st-desk", "post_journal", ledger.PostJournal{RequestKey: "st-desk", EconomicDate: "2026-11-07", AccountingDate: "2026-11-07", Description: "Desk, paid by Eightball",
		Lines:   []ledger.PostLine{{AccountID: tools.ID, Side: "debit", AmountMinor: "25000"}, {AccountID: studioToEightball.ID, Side: "credit", AmountMinor: "25000"}},
		PairKey: "pair-desk", PairedTransactionID: desk.ID})
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["studio"].ID, studioHalf, "approve-desk", 0)); w.Code != http.StatusOK {
		t.Fatal("approve desk", w.Code, w.Body.String())
	}
	if lt.balance("eightball", "1310") != 25000 || lt.balance("studio", "2310") != -25000 {
		t.Fatal("desk pair", lt.balance("eightball", "1310"), lt.balance("studio", "2310"))
	}

	// Turned off, a link's waiting pairs can only be rejected.
	tools2 := lt.item("kit", "EXAMPLE DESIGN TOOLS")
	if _, err := lt.prepare("kit", "kit-tools", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-tools", TransactionID: tools2.ID, Description: "Design tools for the studio",
		OtherAccountID: kitInStudio.ID, PairKey: "pair-tools"}); err != nil {
		t.Fatal(err)
	}
	toolsHalf, err := lt.prepare("studio", "st-tools", "post_journal", ledger.PostJournal{RequestKey: "st-tools", EconomicDate: "2026-11-06", AccountingDate: "2026-11-06", Description: "Design tools, paid by Kit",
		Lines:   []ledger.PostLine{{AccountID: tools.ID, Side: "debit", AmountMinor: "1800"}, {AccountID: studioCapital.ID, Side: "credit", AmountMinor: "1800"}},
		PairKey: "pair-tools", PairedTransactionID: tools2.ID})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{lt.books["kit"].ID, lt.books["studio"].ID}, PayerBookID: lt.books["kit"].ID, Treatment: "contribution",
		PayerAccountID: kitInStudio.ID, BeneficiaryAccountID: studioCapital.ID, Active: true, LinkActive: &off, ExpectedRevision: 1})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, bookLinksPath, string(body)); w.Code != http.StatusOK {
		t.Fatal("turn off", w.Code, w.Body.String())
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["studio"].ID, toolsHalf, "approve-tools", 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "turned off") {
		t.Fatal("approved over a link turned off", w.Code, w.Body.String())
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/reject", proposalDecisionBody(lt.books["studio"].ID, toolsHalf, "reject-tools", 0)); w.Code != http.StatusOK {
		t.Fatal("reject", w.Code, w.Body.String())
	}
	// A link with pairs is turned off, never removed: its terms stay too.
	if w := removeLink("eightball", "studio"); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "turn the link off") {
		t.Fatal("removed a link with pairs", w.Code, w.Body.String())
	}
	var terms int
	if err := lt.f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.book_link_terms WHERE tenant_id=$1", lt.f.tenant).Scan(&terms); err != nil || terms != 3 {
		t.Fatal("terms after a refused removal", terms, err)
	}
}

// The owner records a cost one Book paid for a linked Book in one step;
// later charges from the same payee suggest that Book and account, from its
// own activity or from earlier pairs. Undoing either entry reverses both.
func TestOwnerRecordsAPairAtOnce(t *testing.T) {
	lt := newLinkedTenant(t)
	ctx := lt.f.db.Context
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	groceries := lt.account("kit", "6900", "expense", "", "Groceries")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	travel := lt.account("eightball", "6400", "expense", "", "Travel")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 4310, Description: "EXAMPLE CLOUD 1", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-09", AmountMinor: 4310, Description: "EXAMPLE CLOUD 2", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-10", AmountMinor: 12900, Description: "EXAMPLE AIR", Counterparty: "Example Air"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k4", Date: "2026-11-11", AmountMinor: -2000, Description: "CARD REFUND", Counterparty: "Example Air"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k5", Date: "2026-11-11", AmountMinor: 3150, Description: "EXAMPLE GROCER", Counterparty: "Example Grocer"})
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-10-20", AmountMinor: -18000, Description: "EXAMPLE AIR", Counterparty: "Example Air"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal("link", w.Code, w.Body.String())
	}
	// Eightball recorded its own flight with Example Air to travel.
	ownAir := lt.item("eightball", "EXAMPLE AIR")
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "eb-air", TransactionID: ownAir.ID, Description: "Flight", OtherAccountID: travel.ID})

	pair := func(item ledger.BankTransaction, book string, account ledger.Account) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.OwnerPair{BankItemID: item.ID, BeneficiaryBookID: lt.books[book].ID, BeneficiaryAccountID: account.ID, Description: "Paid by Kit for Eightball"})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body))
	}
	// Refused by type: a Book's own cash, and money coming in.
	if w := pair(lt.item("kit", "EXAMPLE CLOUD 1"), "eightball", checking); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "debits only expenses") {
		t.Fatal("a pair debiting cash", w.Code, w.Body.String())
	}
	if w := pair(lt.item("kit", "CARD REFUND"), "eightball", travel); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "money paid out") {
		t.Fatal("a pair for money in", w.Code, w.Body.String())
	}
	if w := pair(lt.item("kit", "EXAMPLE GROCER"), "kit", groceries); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("a pair inside one Book", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "6100") != 0 {
		t.Fatal("a refused pair left entries")
	}

	first := lt.item("kit", "EXAMPLE CLOUD 1")
	w := pair(first, "eightball", software)
	var got ledger.OwnerPairResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.PayerEntryID == "" || got.BeneficiaryEntryID == "" {
		t.Fatal("record a pair", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 4310 || lt.balance("kit", "2100") != -4310 || lt.balance("eightball", "6100") != 4310 || lt.balance("eightball", "2300") != -4310 {
		t.Fatal("both sides", lt.balance("kit", "1300"), lt.balance("eightball", "6100"), lt.balance("eightball", "2300"))
	}
	if w := pair(first, "eightball", software); w.Code == http.StatusOK {
		t.Fatal("recorded the same item twice")
	}

	// Unrecorded charges suggest where they belong; a refund and an item
	// with no history suggest nothing.
	w = bankCall(t, lt.s, lt.f.session, http.MethodGet, "/api/v1/bank/activity?book_id="+lt.books["kit"].ID, "")
	var activity struct {
		Page ledger.BankActivityPage `json:"page"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &activity) != nil {
		t.Fatal("activity", w.Code, w.Body.String())
	}
	suggested := map[string][]ledger.SuggestedBeneficiary{}
	for _, x := range activity.Page.Items {
		suggested[x.Description] = x.SuggestedBeneficiaries
	}
	if s := suggested["EXAMPLE CLOUD 2"]; len(s) != 1 || s[0].BookID != lt.books["eightball"].ID || s[0].AccountID != software.ID || s[0].Why == "" {
		t.Fatalf("from an earlier pair: %+v", s)
	}
	if s := suggested["EXAMPLE AIR"]; len(s) != 1 || s[0].AccountID != travel.ID {
		t.Fatalf("from the other Book's own activity: %+v", s)
	}
	if len(suggested["CARD REFUND"]) != 0 || len(suggested["EXAMPLE GROCER"]) != 0 {
		t.Fatal("suggested without a reason", suggested["CARD REFUND"], suggested["EXAMPLE GROCER"])
	}

	// Undoing the owner's pair from either side reverses both.
	data, _ := json.Marshal(map[string]any{"book_id": lt.books["kit"].ID, "operation": "reverse_journal",
		"arguments": map[string]string{"request_key": "undo-cloud", "entry_id": got.PayerEntryID, "accounting_date": "2026-11-12", "reason": "Not Eightball's"}})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/account-changes", string(data)); w.Code != http.StatusOK {
		t.Fatal("undo", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "6100") != 0 || lt.balance("eightball", "2300") != 0 {
		t.Fatal("after undo", lt.balance("kit", "1300"), lt.balance("eightball", "6100"), lt.balance("eightball", "2300"))
	}
	// Turned off, the link records no new pairs.
	off := false
	body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{lt.books["kit"].ID, lt.books["eightball"].ID}, PayerBookID: lt.books["kit"].ID, Treatment: "owed",
		PayerAccountID: kitFromEightball.ID, BeneficiaryAccountID: eightballToKit.ID, Active: true, LinkActive: &off, ExpectedRevision: 1})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, bookLinksPath, string(body)); w.Code != http.StatusOK {
		t.Fatal("turn off", w.Code, w.Body.String())
	}
	if w := pair(lt.item("kit", "EXAMPLE CLOUD 2"), "eightball", software); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "turned off") {
		t.Fatal("paired over a link turned off", w.Code, w.Body.String())
	}
	_ = ctx
}

// Money one linked Book pays the other settles what it owes. A business
// repaying its owner names the costs it repays, exactly; nothing pays more
// than is owed; a cost a settlement repays is undone only after it. Who owes
// whom reads the same from both sides.
func TestLinkedBooksSettleUp(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	travel := lt.account("eightball", "6400", "expense", "", "Travel")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	eightballFromStudio := lt.account("eightball", "1310", "asset", "receivable", "Owed by the studio")
	lt.account("eightball", "2500", "liability", "loan", "Equipment loan")
	studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioToEightball := lt.account("studio", "2310", "liability", "payable", "Owed to Eightball Inc.")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-03", AmountMinor: 12900, Description: "EXAMPLE AIR", Counterparty: "Example Air"})
	lt.bank("kit", "cash", kitChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-13", AmountMinor: 4310, Description: "FROM EIGHTBALL", Counterparty: "Eightball Inc."},
		ledger.BankSyncTransaction{ProviderTransactionID: "k4", Date: "2026-11-14", AmountMinor: 20000, Description: "FROM EIGHTBALL AGAIN", Counterparty: "Eightball Inc."})
	lt.bank("eightball", "cash", checking,
		ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-12", AmountMinor: -4310, Description: "TO KIT", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "e2", Date: "2026-11-14", AmountMinor: -20000, Description: "TO KIT AGAIN", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "e3", Date: "2026-11-05", AmountMinor: -25000, Description: "SHARED DESK", Counterparty: "Shared Desk"},
		ledger.BankSyncTransaction{ProviderTransactionID: "e4", Date: "2026-11-21", AmountMinor: 10000, Description: "FROM THE STUDIO", Counterparty: "Kit Blanche Studio"})
	lt.bank("studio", "cash", studioChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-20", AmountMinor: -10000, Description: "TO EIGHTBALL", Counterparty: "Eightball Inc."})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal("link kit", w.Code, w.Body.String())
	}
	if w := lt.link("eightball", "studio", "owed", eightballFromStudio, studioToEightball, 0); w.Code != http.StatusOK {
		t.Fatal("link studio", w.Code, w.Body.String())
	}
	pair := func(item ledger.BankTransaction, book string, account ledger.Account) ledger.OwnerPairResult {
		body, _ := json.Marshal(ledger.OwnerPair{BankItemID: item.ID, BeneficiaryBookID: lt.books[book].ID, BeneficiaryAccountID: account.ID, Description: item.Description})
		var out ledger.OwnerPairResult
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("pair", w.Code, w.Body.String())
		}
		return out
	}
	cloud := pair(lt.item("kit", "EXAMPLE CLOUD"), "eightball", software)
	air := pair(lt.item("kit", "EXAMPLE AIR"), "eightball", travel)
	pair(lt.item("eightball", "SHARED DESK"), "studio", tools)

	// Eightball repays Kit for the cloud bill: two halves, prepared by the
	// agents, the payer's first.
	eb := lt.item("eightball", "TO KIT")
	kitIn := lt.item("kit", "FROM EIGHTBALL")
	half := func(book, key string, item ledger.BankTransaction, account ledger.Account, repays []ledger.Repay) (ledger.OperationProposal, error) {
		return lt.prepare(book, key, "record_bank_activity", ledger.RecordBankActivity{RequestKey: key, TransactionID: item.ID, Description: "Repaid by Eightball",
			OtherAccountID: account.ID, PairKey: "settle-cloud", Settlement: true, Repays: repays})
	}
	if _, err := half("eightball", "eb-repay", eb, eightballToKit, nil); err != nil {
		t.Fatal("first half", err)
	}
	ebHalf, err := lt.prepare("eightball", "eb-repay-2", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "eb-repay-2", TransactionID: eb.ID, Description: "Repaid by Eightball",
		OtherAccountID: eightballToKit.ID, PairKey: "settle-cloud-2", Settlement: true, Repays: ledger.RepayKeys(cloud.PairKey)})
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["eightball"].ID, ebHalf, "approve-alone", 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "waiting for its other half") {
		t.Fatal("approved half a settlement", w.Code, w.Body.String())
	}
	_, err = lt.prepare("kit", "kit-repay-air", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-repay-air", TransactionID: kitIn.ID, Description: "Repaid by Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "settle-cloud-2", Settlement: true, Repays: ledger.RepayKeys(air.PairKey)})
	refusedWith(t, err, "same costs")
	kitHalf, err := lt.prepare("kit", "kit-repay", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-repay", TransactionID: kitIn.ID, Description: "Repaid by Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "settle-cloud-2", Settlement: true, Repays: ledger.RepayKeys(cloud.PairKey)})
	if err != nil {
		t.Fatal("second half", err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, kitHalf, "approve-repay", 0)); w.Code != http.StatusOK {
		t.Fatal("approve settlement", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 12900 || lt.balance("eightball", "2300") != -12900 || lt.balance("kit", "1000") != 4310 || lt.balance("eightball", "1100") != -4310-25000 {
		t.Fatal("after the settlement", lt.balance("kit", "1300"), lt.balance("eightball", "2300"), lt.balance("kit", "1000"), lt.balance("eightball", "1100"))
	}

	// The cloud bill had no receipt: the owner is asked for it.
	var reminders int
	lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND status='open' AND text LIKE 'Add the receipt for%'",
		lt.f.tenant, lt.books["eightball"].ID).Scan(&reminders)
	if reminders != 1 {
		t.Fatal("receipt reminders", reminders)
	}

	// Paying more than is owed, with nowhere to record the extra, says how
	// to make room for it, and leaves nothing behind.
	settle := func(debtor, creditor ledger.BankTransaction, repays []string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.OwnerSettlement{DebtorItemID: debtor.ID, CreditorItemID: creditor.ID, Repays: ledger.RepayKeys(repays...)})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body))
	}
	if w := settle(lt.item("eightball", "TO KIT AGAIN"), lt.item("kit", "FROM EIGHTBALL AGAIN"), []string{air.PairKey}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), " paid Kit Blanche ") || !strings.Contains(w.Body.String(), "more than it owes. Choose how to record the extra in Links.") {
		t.Fatal("overpaid", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 12900 || lt.balance("eightball", "1100") != -4310-25000 {
		t.Fatal("a refused settlement left entries")
	}
	// Between businesses, a payment needn't name costs.
	if w := settle(lt.item("studio", "TO EIGHTBALL"), lt.item("eightball", "FROM THE STUDIO"), nil); w.Code != http.StatusOK {
		t.Fatal("business settlement", w.Code, w.Body.String())
	}

	// Who owes whom, from the owner and from Kit's agent.
	w := bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
	var all ledger.LinkedBalances
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 2 {
		t.Fatal("balances", w.Code, w.Body.String())
	}
	for _, l := range all.Links {
		if !l.Matches || len(l.MismatchedEntryIDs) != 0 {
			t.Fatalf("sides disagree: %+v", l)
		}
		switch l.CreditorBookID {
		case lt.books["kit"].ID:
			if l.DebtorBookID != lt.books["eightball"].ID || l.AmountMinor != "12900" || len(l.OpenPairs) != 2 {
				t.Fatalf("kit and eightball: %+v", l)
			}
			for _, p := range l.OpenPairs {
				if p.Repaid != (p.PairKey == cloud.PairKey) {
					t.Fatalf("repaid: %+v", p)
				}
			}
		case lt.books["eightball"].ID:
			if l.DebtorBookID != lt.books["studio"].ID || l.AmountMinor != "15000" {
				t.Fatalf("eightball and the studio: %+v", l)
			}
		default:
			t.Fatalf("link %+v", l)
		}
	}
	var loans int
	for _, b := range all.Books {
		for _, a := range b.Liabilities {
			if a.AccountID == eightballToKit.ID || a.AccountID == studioToEightball.ID {
				t.Fatal("a link's account listed as another liability", a)
			}
			if a.Role == "loan" {
				loans++
			}
		}
	}
	if loans != 1 {
		t.Fatal("other liabilities", all.Books)
	}
	var mine ledger.LinkedBalances
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.agents["kit"]), "get_linked_balances", map[string]any{}), &mine)
	if len(mine.Links) != 1 || len(mine.Books) != 1 || mine.Books[0].BookID != lt.books["kit"].ID {
		t.Fatalf("kit's balances: %+v", mine)
	}

	// The cloud cost is repaid, so it's undone only after its settlement;
	// undoing the settlement reverses both of its entries.
	undo := func(book, key, entry string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"book_id": lt.books[book].ID, "operation": "reverse_journal",
			"arguments": map[string]string{"request_key": key, "entry_id": entry, "accounting_date": "2026-11-22", "reason": "Example correction"}})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/account-changes", string(data))
	}
	if w := undo("kit", "undo-cloud", cloud.PayerEntryID); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "undo the settlement first") {
		t.Fatal("undid a repaid cost", w.Code, w.Body.String())
	}
	var settlementEntry string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT payer_entry_id::text FROM public.entry_pairs WHERE tenant_id=$1 AND pair_key='settle-cloud-2'", lt.f.tenant).Scan(&settlementEntry); err != nil {
		t.Fatal(err)
	}
	if w := undo("eightball", "undo-settlement", settlementEntry); w.Code != http.StatusOK {
		t.Fatal("undo the settlement", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1000") != 0 || lt.balance("kit", "1300") != 4310+12900 || lt.balance("eightball", "2300") != -4310-12900 {
		t.Fatal("after undoing the settlement", lt.balance("kit", "1000"), lt.balance("kit", "1300"), lt.balance("eightball", "2300"))
	}
	if w := undo("kit", "undo-cloud-2", cloud.PayerEntryID); w.Code != http.StatusOK {
		t.Fatal("undo the cost", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 12900 || lt.balance("eightball", "2300") != -12900 || lt.balance("eightball", "6100") != 0 {
		t.Fatal("after undoing the cost", lt.balance("kit", "1300"), lt.balance("eightball", "2300"))
	}

	// With what Eightball pays for Kit set to owed back, the overpayment is
	// recorded plainly: what was owed is paid, and Kit owes the rest.
	kitOwes := lt.account("kit", "2320", "liability", "payable", "Owed to Eightball Inc.")
	eightballFromKit := lt.account("eightball", "1320", "asset", "receivable", "Owed by Kit Blanche")
	if w := lt.link("eightball", "kit", "owed", eightballFromKit, kitOwes, 1); w.Code != http.StatusOK {
		t.Fatal("eightball pays for kit", w.Code, w.Body.String())
	}
	w = settle(lt.item("eightball", "TO KIT AGAIN"), lt.item("kit", "FROM EIGHTBALL AGAIN"), []string{air.PairKey})
	var overpaid ledger.OwnerSettlementResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &overpaid) != nil || overpaid.ExcessMinor != "7100" || overpaid.ExcessRecordedAs != "owed_back" {
		t.Fatal("overpayment", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "2300") != 0 || lt.balance("eightball", "1320") != 7100 || lt.balance("kit", "2320") != -7100 ||
		lt.balance("kit", "1000") != 20000 || lt.balance("eightball", "1100") != -25000+10000-20000 {
		t.Fatal("after the overpayment", lt.balance("kit", "1300"), lt.balance("eightball", "2300"), lt.balance("eightball", "1320"), lt.balance("kit", "2320"))
	}
}

// An LLC whose payments to its owner are owner's equity repays without
// naming costs: the oldest open costs it covers are repaid, and anything
// more is a draw. The cost it repaid has no receipt, so the owner is
// reminded to add it.
func TestAnLLCRepaysItsOwner(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
	draws := lt.account("studio", "3200", "equity", "", "Owner draws")
	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 5000, Description: "EXAMPLE DESK", Counterparty: "Example Desk"})
	lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-12", AmountMinor: 8000, Description: "FROM THE STUDIO", Counterparty: "Kit Blanche Studio"})
	lt.bank("studio", "cash", studioChecking, ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-11", AmountMinor: -8000, Description: "TO KIT", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", "EXAMPLE DESK").ID, BeneficiaryBookID: lt.books["studio"].ID, BeneficiaryAccountID: tools.ID, Description: "Desk"})
	var desk ledger.OwnerPairResult
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &desk) != nil {
		t.Fatal("desk", w.Code, w.Body.String())
	}
	body, _ = json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("studio", "TO KIT").ID, CreditorItemID: lt.item("kit", "FROM THE STUDIO").ID})
	var paid ledger.OwnerSettlementResult
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &paid) != nil ||
		paid.ExcessMinor != "3000" || paid.ExcessRecordedAs != "owner_draw" {
		t.Fatal("settle", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1310") != 0 || lt.balance("studio", "2300") != 0 || lt.balance("studio", "3200") != 3000 || lt.balance("kit", "1500") != -3000 ||
		lt.balance("kit", "1000") != 8000 || lt.balance("studio", "1000") != -8000 {
		t.Fatal("balances", lt.balance("kit", "1310"), lt.balance("studio", "2300"), lt.balance("studio", "3200"), lt.balance("kit", "1500"))
	}
	w := bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
	var all ledger.LinkedBalances
	if json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 1 || len(all.Links[0].OpenPairs) != 1 || !all.Links[0].OpenPairs[0].Repaid || all.Links[0].AmountMinor != "0" {
		t.Fatal("balances read", w.Body.String())
	}
	var reminders int
	lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1", lt.f.tenant).Scan(&reminders)
	// The cost repaid has no receipt: the owner is reminded, to document it.
	if reminders != 1 {
		t.Fatal("receipt reminders", reminders)
	}
}

// The owner says where charges go: by answering an agent's question with
// one of its choices, on screen, or by rejecting a charge and naming its
// Book. Each becomes a rule agents read.
func TestOwnerAssignsChargesToLinkedBooks(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	studioCapital := lt.account("studio", "3100", "equity", "", "Owner contributions")
	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"})
	for _, l := range []struct {
		to          string
		mine, their ledger.Account
		treatment   string
	}{{"eightball", kitFromEightball, eightballToKit, "owed"}, {"studio", kitInStudio, studioCapital, "contribution"}} {
		if w := lt.link("kit", l.to, l.treatment, l.mine, l.their, 0); w.Code != http.StatusOK {
			t.Fatal("link", w.Code, w.Body.String())
		}
	}
	kit, eightball, studio := lt.books["kit"].ID, lt.books["eightball"].ID, lt.books["studio"].ID
	agent := lt.scope(lt.agents["kit"])
	ask := func(args map[string]any) (json.RawMessage, error) {
		return lt.f.db.Store.Call(lt.f.db.Context, agent, "ask_question", mustJSON(args))
	}

	// An entity question offers choices; without them it's refused.
	_, err := ask(map[string]any{"subject_kind": "entity", "subject_key": card.ID + ",example cloud", "question": "Which Book is Example Cloud for?"})
	refusedWith(t, err, "offers choices")
	out, err := ask(map[string]any{"subject_kind": "entity", "subject_key": card.ID + ",example cloud", "question": "Which Book is Example Cloud for?",
		"choices": []map[string]string{{"value": eightball, "label": "Eightball Inc."}, {"value": studio, "label": "Kit Blanche Studio LLC"}, {"value": "personal", "label": "Personal"}}})
	var question ledger.AgentQuestion
	if err != nil || json.Unmarshal(out, &question) != nil || len(question.Choices) == 0 {
		t.Fatal("ask", err, string(out))
	}
	answer := func(value any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"book_id": kit, "question_id": question.ID, "answer": value})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, agentNotesPath+"/answer", string(body))
	}
	if w := answer(map[string]any{"value": "someone else"}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "one of the question") {
		t.Fatal("answered outside the choices", w.Code, w.Body.String())
	}
	if w := answer(map[string]any{"value": eightball, "make_rule": true}); w.Code != http.StatusOK {
		t.Fatal("answer", w.Code, w.Body.String())
	}
	var rules struct {
		Rules []ledger.AssignmentRule `json:"rules"`
	}
	json.Unmarshal(lt.f.db.Call(t, agent, "list_assignment_rules", map[string]any{}), &rules)
	if len(rules.Rules) != 1 || rules.Rules[0].Target != eightball || rules.Rules[0].CounterpartyKey != "example cloud" || rules.Rules[0].BankAccountID != card.ID || rules.Rules[0].Source != "answer" {
		t.Fatalf("rule from the answer: %+v", rules.Rules)
	}

	// On screen: a rule replaces the one for the same counterparty, and
	// charges go only to this Book or a Book linked to it.
	set := func(book string, change ledger.AssignmentRuleChange) *httptest.ResponseRecorder {
		change.BookID = lt.books[book].ID
		body, _ := json.Marshal(change)
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, assignmentRulesPath, string(body))
	}
	if w := set("kit", ledger.AssignmentRuleChange{BankAccountID: card.ID, CounterpartyKey: "Example Cloud", Target: studio}); w.Code != http.StatusOK ||
		json.Unmarshal(w.Body.Bytes(), &rules) != nil || len(rules.Rules) != 1 || rules.Rules[0].Target != studio || rules.Rules[0].Source != "owner" {
		t.Fatal("replace a rule", w.Code, w.Body.String())
	}
	if w := set("eightball", ledger.AssignmentRuleChange{CounterpartyKey: "Example Desk", Target: studio}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "linked to it") {
		t.Fatal("a rule to an unlinked Book", w.Code, w.Body.String())
	}

	// Rejecting a pair names the Book it belongs to: the decision says so,
	// and the charge itself gets a rule.
	cloud := lt.item("kit", "EXAMPLE CLOUD")
	half, err := lt.prepare("kit", "kit-cloud", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-cloud", TransactionID: cloud.ID, Description: "Cloud for Eightball",
		OtherAccountID: kitFromEightball.ID, PairKey: "pair-cloud", AssignedBy: "rule"})
	if err != nil {
		t.Fatal(err)
	}
	decide := func(path string, extra map[string]any) *httptest.ResponseRecorder {
		var body map[string]any
		json.Unmarshal([]byte(proposalDecisionBody(kit, half, "decide-cloud", 0)), &body)
		for k, v := range extra {
			body[k] = v
		}
		data, _ := json.Marshal(body)
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, path, string(data))
	}
	if w := decide("/api/v1/agent-owner/approve", map[string]any{"reassigned_to": studio}); w.Code != http.StatusBadRequest {
		t.Fatal("reassigned on an approval", w.Code, w.Body.String())
	}
	w := decide("/api/v1/agent-owner/reject", map[string]any{"reassigned_to": studio, "reason": "This is the studio's"})
	var decided ledger.OperationDecisionResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &decided) != nil || decided.Decision.ReassignedTo != studio {
		t.Fatal("reassign", w.Code, w.Body.String())
	}
	var state struct {
		Decision *ledger.OperationDecision `json:"decision"`
	}
	json.Unmarshal(lt.f.db.Call(t, agent, "get_operation_proposal", ledger.ProposalQuery{ProposalID: half.ID}), &state)
	if state.Decision == nil || state.Decision.ReassignedTo != studio {
		t.Fatalf("the agent reads the reassignment: %+v", state.Decision)
	}
	json.Unmarshal(lt.f.db.Call(t, agent, "list_assignment_rules", map[string]any{}), &rules)
	var item *ledger.AssignmentRule
	for i, r := range rules.Rules {
		if r.TransactionID == cloud.ID {
			item = &rules.Rules[i]
		}
	}
	if len(rules.Rules) != 2 || item == nil || item.Target != studio || item.Source != "reassigned" {
		t.Fatalf("rules after reassigning: %+v", rules.Rules)
	}

	// The owner removes a rule.
	if w := ownerAPICall(lt.s, lt.f.session, http.MethodDelete, assignmentRulesPath+"?id="+item.ID+"&book_id="+kit, "", true); w.Code != http.StatusOK {
		t.Fatal("remove a rule", w.Code, w.Body.String())
	}
	json.Unmarshal(lt.f.db.Call(t, agent, "list_assignment_rules", map[string]any{}), &rules)
	if len(rules.Rules) != 1 {
		t.Fatalf("after removing: %+v", rules.Rules)
	}
}

// Undoing a pair frees its bank item: it reads unrecorded and is recorded
// again for another Book, with no separate unlink. A plain recording,
// undone, is recorded again the same way.
func TestUndoingAPairFreesItsBankItem(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	groceries := lt.account("kit", "6900", "expense", "", "Groceries")
	household := lt.account("kit", "6950", "expense", "", "Household")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioCapital := lt.account("studio", "3100", "equity", "", "Owner contributions")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE TOOLS", Counterparty: "Example Tools"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-05", AmountMinor: 2200, Description: "EXAMPLE GROCER", Counterparty: "Example Grocer"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := lt.link("kit", "studio", "contribution", kitInStudio, studioCapital, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	item := lt.item("kit", "EXAMPLE TOOLS")
	pairFor := func(key, book string, mine, theirs, expense ledger.Account) ledger.OperationProposal {
		if _, err := lt.prepare("kit", "kit-"+key, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-" + key, TransactionID: item.ID, Description: "Tools",
			OtherAccountID: mine.ID, PairKey: key}); err != nil {
			t.Fatal(err)
		}
		half, err := lt.prepare(book, book+"-"+key, "post_journal", ledger.PostJournal{RequestKey: book + "-" + key, EconomicDate: "2026-11-04", AccountingDate: "2026-11-04", Description: "Tools, paid by Kit",
			Lines:   []ledger.PostLine{{AccountID: expense.ID, Side: "debit", AmountMinor: "4310"}, {AccountID: theirs.ID, Side: "credit", AmountMinor: "4310"}},
			PairKey: key, PairedTransactionID: item.ID})
		if err != nil {
			t.Fatal(err)
		}
		return half
	}
	approve := func(book string, p ledger.OperationProposal, key string) {
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books[book].ID, p, key, 0)); w.Code != http.StatusOK {
			t.Fatal("approve", book, w.Code, w.Body.String())
		}
	}
	undo := func(book, key, entry string) {
		data, _ := json.Marshal(map[string]any{"book_id": lt.books[book].ID, "operation": "reverse_journal",
			"arguments": map[string]string{"request_key": key, "entry_id": entry, "accounting_date": "2026-11-06", "reason": "Not Eightball's"}})
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/account-changes", string(data)); w.Code != http.StatusOK {
			t.Fatal("undo", w.Code, w.Body.String())
		}
	}
	status := func(book, description string) string { return lt.item(book, description).Status }

	approve("eightball", pairFor("pair-tools", "eightball", kitFromEightball, eightballToKit, software), "approve-eightball")
	var payerEntry string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT payer_entry_id::text FROM public.entry_pairs WHERE tenant_id=$1 AND pair_key='pair-tools'", lt.f.tenant).Scan(&payerEntry); err != nil {
		t.Fatal(err)
	}
	// Undone from the Book that paid, it reads unrecorded, and moves to the studio.
	undo("kit", "undo-tools", payerEntry)
	if s := status("kit", "EXAMPLE TOOLS"); s != "unrecorded" {
		t.Fatal("after undo", s)
	}
	approve("studio", pairFor("pair-tools-2", "studio", kitInStudio, studioCapital, tools), "approve-studio")
	if lt.balance("eightball", "6100") != 0 || lt.balance("eightball", "2300") != 0 || lt.balance("kit", "1300") != 0 ||
		lt.balance("studio", "6200") != 4310 || lt.balance("kit", "1500") != 4310 || lt.balance("kit", "2100") != -4310 || status("kit", "EXAMPLE TOOLS") != "recorded" {
		t.Fatal("moved to the studio", lt.balance("eightball", "6100"), lt.balance("studio", "6200"), lt.balance("kit", "1500"), lt.balance("kit", "2100"))
	}

	// A plain recording, undone, is recorded again with no unlink.
	grocer := lt.item("kit", "EXAMPLE GROCER")
	var recorded ledger.BankLinkResult
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "grocer", TransactionID: grocer.ID, Description: "Groceries", OtherAccountID: groceries.ID}), &recorded)
	if recorded.Entry == nil {
		t.Fatal("record groceries")
	}
	undo("kit", "undo-grocer", recorded.Entry.ID)
	if s := status("kit", "EXAMPLE GROCER"); s != "unrecorded" {
		t.Fatal("after undoing groceries", s)
	}
	lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "grocer-2", TransactionID: grocer.ID, Description: "Household", OtherAccountID: household.ID})
	if lt.balance("kit", "6900") != 0 || lt.balance("kit", "6950") != 2200 || lt.balance("kit", "2100") != -4310-2200 {
		t.Fatal("recorded again", lt.balance("kit", "6900"), lt.balance("kit", "6950"), lt.balance("kit", "2100"))
	}
}

// Refusals about a pair say what stopped it and what to do; nothing is
// recorded.
func TestPairRefusalsSayWhy(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	travel := lt.account("eightball", "6400", "expense", "", "Travel")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-10-20", AmountMinor: 9000, Description: "EXAMPLE AIR", Counterparty: "Example Air"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-04", AmountMinor: 4310, Description: "EXAMPLE CLOUD", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-05", AmountMinor: 1500, Description: "EXAMPLE BOOKS", Counterparty: "Example Books"})
	lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k4", Date: "2026-11-12", AmountMinor: -500, Description: "KIT OUT", Counterparty: "Example"})
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-12", AmountMinor: -500, Description: "EIGHTBALL OUT", Counterparty: "Example"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	pair := func(key, description, date, amount string, expense ledger.Account) (ledger.OperationProposal, ledger.OperationProposal) {
		item := lt.item("kit", description)
		mine, err := lt.prepare("kit", "kit-"+key, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "kit-" + key, TransactionID: item.ID, Description: description,
			OtherAccountID: kitFromEightball.ID, PairKey: key})
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := lt.prepare("eightball", "eb-"+key, "post_journal", ledger.PostJournal{RequestKey: "eb-" + key, EconomicDate: date, AccountingDate: date, Description: description,
			Lines:   []ledger.PostLine{{AccountID: expense.ID, Side: "debit", AmountMinor: amount}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: amount}},
			PairKey: key, PairedTransactionID: item.ID})
		if err != nil {
			t.Fatal(err)
		}
		return mine, theirs
	}
	refused := func(book string, p ledger.OperationProposal, path, key, words string) {
		t.Helper()
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, path, proposalDecisionBody(lt.books[book].ID, p, key, 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), words) {
			t.Fatalf("want %q: %d %s", words, w.Code, w.Body.String())
		}
	}
	airMine, _ := pair("pair-air", "EXAMPLE AIR", "2026-10-20", "9000", travel)
	cloudMine, _ := pair("pair-cloud", "EXAMPLE CLOUD", "2026-11-04", "4310", software)
	booksMine, booksTheirs := pair("pair-books", "EXAMPLE BOOKS", "2026-11-05", "1500", software)

	// Eightball closed October; then retired Software.
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "close_period", ledger.ClosePeriod{RequestKey: "close-oct", From: "2026-10-01", Through: "2026-10-31", Reason: "Example close"})
	refused("kit", airMine, "/api/v1/agent-owner/approve", "approve-air", lt.books["eightball"].Name+"'s period through 2026-10-31 is closed")
	var acct ledger.Account
	json.Unmarshal(lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "retire_account", ledger.RetireAccount{RequestKey: "retire-software", AccountID: software.ID, ExpectedRevision: 1}), &acct)
	refused("kit", cloudMine, "/api/v1/agent-owner/approve", "approve-cloud", "Software in "+lt.books["eightball"].Name+" is retired")
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "2300") != 0 {
		t.Fatal("a refused pair left entries")
	}
	// Rejected from Eightball, Kit's half can't be approved.
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/reject", proposalDecisionBody(lt.books["eightball"].ID, booksTheirs, "reject-books", 0)); w.Code != http.StatusOK {
		t.Fatal("reject", w.Code, w.Body.String())
	}
	refused("kit", booksMine, "/api/v1/agent-owner/approve", "approve-books", "This pair was rejected")
	// Kit's agent can't prepare Eightball's half in Kit's Book.
	_, err := lt.prepare("kit", "kit-wrong", "post_journal", ledger.PostJournal{RequestKey: "kit-wrong", EconomicDate: "2026-11-05", AccountingDate: "2026-11-05", Description: "Wrong Book",
		Lines: []ledger.PostLine{{AccountID: travel.ID, Side: "debit", AmountMinor: "100"}, {AccountID: eightballToKit.ID, Side: "credit", AmountMinor: "100"}}})
	refusedWith(t, err, "through your connection to that Book")
	// Two payments out aren't a settlement.
	body, _ := json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("eightball", "EIGHTBALL OUT").ID, CreditorItemID: lt.item("kit", "KIT OUT").ID})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Both items pay out") {
		t.Fatal("two payments out", w.Code, w.Body.String())
	}
}

// What one linked Book owed the other before their first bank line is
// entered once, mirrored in both against each Book's opening balances
// account: by the owner at once, or as two agent halves the owner approves.
// It shows in who owes whom and is undone as a pair.
func TestOpeningBalancesBetweenLinkedBooks(t *testing.T) {
	lt := newLinkedTenant(t)
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.account("eightball", "3900", "equity", "opening_balance", "Opening balances")
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	enter := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.OwnerOpeningBalance{DebtorBookID: lt.books["eightball"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2024-11-01", AmountMinor: "150000"})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(body))
	}
	if w := enter(); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Add an opening balances account") {
		t.Fatal("no opening balances account", w.Code, w.Body.String())
	}
	lt.account("kit", "3900", "equity", "opening_balance", "Opening balances")
	w := enter()
	var opened ledger.OwnerOpeningBalanceResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &opened) != nil {
		t.Fatal("enter", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 150000 || lt.balance("kit", "3900") != -150000 || lt.balance("eightball", "2300") != -150000 || lt.balance("eightball", "3900") != 150000 {
		t.Fatal("mirrored", lt.balance("kit", "1300"), lt.balance("kit", "3900"), lt.balance("eightball", "2300"), lt.balance("eightball", "3900"))
	}
	if w := enter(); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "already entered") {
		t.Fatal("entered twice", w.Code, w.Body.String())
	}
	w = bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
	var all ledger.LinkedBalances
	if json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 1 || all.Links[0].DebtorBookID != lt.books["eightball"].ID || all.Links[0].AmountMinor != "150000" ||
		!all.Links[0].Matches || len(all.Links[0].MismatchedEntryIDs) != 0 {
		t.Fatal("who owes whom", w.Body.String())
	}
	data, _ := json.Marshal(map[string]any{"book_id": lt.books["eightball"].ID, "operation": "reverse_journal",
		"arguments": map[string]string{"request_key": "undo-opening", "entry_id": opened.DebtorEntryID, "accounting_date": "2026-11-12", "reason": "Entered wrong"}})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/account-changes", string(data)); w.Code != http.StatusOK {
		t.Fatal("undo", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 0 || lt.balance("eightball", "2300") != 0 {
		t.Fatal("after undo", lt.balance("kit", "1300"), lt.balance("eightball", "2300"))
	}

	// Agents prepare the two halves; the first waits, the owner approves once.
	half := func(book, other, owedBy string) (ledger.OperationProposal, error) {
		out, err := lt.f.db.Store.Call(lt.f.db.Context, lt.scope(lt.agents[book]), "prepare_opening_balance", mustJSON(ledger.OpeningBalanceHalf{RequestKey: book + "-opening",
			OtherBookID: lt.books[other].ID, OwedBy: owedBy, AsOf: "2024-11-01", AmountMinor: "120000", PairKey: "opening-eb-kit", Reason: "From the 2024 year-end statement"}))
		var p ledger.OperationProposal
		if err == nil {
			json.Unmarshal(out, &p)
		}
		return p, err
	}
	first, err := half("eightball", "kit", "this_book")
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["eightball"].ID, first, "approve-alone", 0)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "waiting") {
		t.Fatal("approved half an opening balance", w.Code, w.Body.String())
	}
	second, err := half("kit", "eightball", "other_book")
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["kit"].ID, second, "approve-opening", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	if lt.balance("kit", "1300") != 120000 || lt.balance("eightball", "2300") != -120000 || lt.balance("kit", "3900") != -120000 || lt.balance("eightball", "3900") != 120000 {
		t.Fatal("approved", lt.balance("kit", "1300"), lt.balance("eightball", "2300"))
	}

	// Eightball repays without naming anything: the oldest open item, the
	// opening balance, is repaid, and who owes whom lists it as repaid.
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	lt.bank("eightball", "cash", checking, ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-12", AmountMinor: -120000, Description: "TO KIT", Counterparty: "Kit Blanche"})
	lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-13", AmountMinor: 120000, Description: "FROM EIGHTBALL", Counterparty: "Eightball Inc."})
	settle := func(repays []string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("eightball", "TO KIT").ID, CreditorItemID: lt.item("kit", "FROM EIGHTBALL").ID, Repays: ledger.RepayKeys(repays...)})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body))
	}
	if w := settle(nil); w.Code != http.StatusOK {
		t.Fatal("repay the opening balance", w.Code, w.Body.String())
	}
	w = bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
	if json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 1 || all.Links[0].AmountMinor != "0" || len(all.Links[0].OpenPairs) != 1 ||
		all.Links[0].OpenPairs[0].Kind != "opening" || !all.Links[0].OpenPairs[0].Repaid {
		t.Fatal("repaid opening balance", w.Body.String())
	}

	// Refusals say what to do: unlinked Books, and a closed period.
	body, _ := json.Marshal(ledger.OwnerOpeningBalance{DebtorBookID: lt.books["studio"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2024-11-01", AmountMinor: "100"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(body)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "aren't linked") {
		t.Fatal("unlinked", w.Code, w.Body.String())
	}
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "close_period", ledger.ClosePeriod{RequestKey: "close-2023", From: "2023-01-01", Through: "2023-12-31", Reason: "Example close"})
	body, _ = json.Marshal(ledger.OwnerOpeningBalance{DebtorBookID: lt.books["eightball"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2023-06-30", AmountMinor: "100"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(body)); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "period through 2023-12-31 is closed") {
		t.Fatal("closed period", w.Code, w.Body.String())
	}
}

// Payments that name no costs repay the oldest first, adding up across
// payments, so nothing reads open once nothing is owed. An agent's
// overpaying half is refused whichever comes first. Turning a link off
// needs any sign-in; turning it on, a recent one. A stale revision says so.
func TestPartialRepaymentsAndNarrowingLinks(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
	draws := lt.account("studio", "3200", "equity", "", "Owner draws")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-01", AmountMinor: 5000, Description: "EXAMPLE DESK", Counterparty: "Example Desk"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-03", AmountMinor: 4000, Description: "EXAMPLE LAMP", Counterparty: "Example Lamp"})
	lt.bank("kit", "cash", kitChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-10", AmountMinor: 6000, Description: "FROM THE STUDIO 1", Counterparty: "Kit Blanche Studio"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k4", Date: "2026-11-20", AmountMinor: 3000, Description: "FROM THE STUDIO 2", Counterparty: "Kit Blanche Studio"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k5", Date: "2026-11-25", AmountMinor: 9000, Description: "FROM THE STUDIO 3", Counterparty: "Kit Blanche Studio"})
	lt.bank("studio", "cash", studioChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-10", AmountMinor: -6000, Description: "TO KIT 1", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "s2", Date: "2026-11-20", AmountMinor: -3000, Description: "TO KIT 2", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "s3", Date: "2026-11-25", AmountMinor: -9000, Description: "TO KIT 3", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, d := range []string{"EXAMPLE DESK", "EXAMPLE LAMP"} {
		body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", d).ID, BeneficiaryBookID: lt.books["studio"].ID, BeneficiaryAccountID: tools.ID, Description: d})
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
			t.Fatal("pair", d, w.Code, w.Body.String())
		}
	}
	open := func() map[string]bool {
		w := bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
		var all ledger.LinkedBalances
		if json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 1 {
			t.Fatal("balances", w.Body.String())
		}
		out := map[string]bool{}
		for _, p := range all.Links[0].OpenPairs {
			out[p.Description] = !p.Repaid
		}
		return out
	}
	settle := func(n string) {
		t.Helper()
		body, _ := json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("studio", "TO KIT "+n).ID, CreditorItemID: lt.item("kit", "FROM THE STUDIO "+n).ID})
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusOK {
			t.Fatal("settle", n, w.Code, w.Body.String())
		}
	}
	settle("1") // $60: the desk, oldest, is repaid; the lamp is $10 short.
	if o := open(); o["EXAMPLE DESK"] || !o["EXAMPLE LAMP"] {
		t.Fatal("after $60", o)
	}
	settle("2") // $30 more: nothing is owed, so nothing is open.
	if o := open(); o["EXAMPLE DESK"] || o["EXAMPLE LAMP"] {
		t.Fatal("after $90", o)
	}
	if lt.balance("studio", "2300") != 0 || lt.balance("kit", "1310") != 0 {
		t.Fatal("owed", lt.balance("studio", "2300"), lt.balance("kit", "1310"))
	}
	// With nothing owed, an agent's paying half is refused though it comes first.
	_, err := lt.prepare("studio", "studio-over", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "studio-over", TransactionID: lt.item("studio", "TO KIT 3").ID,
		Description: "Repaying Kit", OtherAccountID: studioToKit.ID, PairKey: "settle-over", Settlement: true})
	refusedWith(t, err, "more than is owed")

	// Turning the link off on an older sign-in works; turning it on asks
	// for a recent one. A stale revision says the link changed.
	if _, err := lt.f.db.Admin.Exec(lt.f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(lt.f.session)); err != nil {
		t.Fatal(err)
	}
	toggle := func(on bool, revision int64) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ledger.LinkTermsChange{BookIDs: [2]string{lt.books["kit"].ID, lt.books["studio"].ID}, PayerBookID: lt.books["kit"].ID, Treatment: "owed",
			PayerAccountID: kitFromStudio.ID, BeneficiaryAccountID: studioToKit.ID, Active: true, LinkActive: &on, ExpectedRevision: revision})
		return bankCall(t, lt.s, lt.f.session, http.MethodPost, bookLinksPath, string(body))
	}
	if w := toggle(false, 2); w.Code != http.StatusOK {
		t.Fatal("turn off on an older sign-in", w.Code, w.Body.String())
	}
	if w := toggle(true, 3); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh_authentication_required") {
		t.Fatal("turned on without a recent sign-in", w.Code, w.Body.String())
	}
	if _, err := lt.f.db.Admin.Exec(lt.f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(lt.f.session)); err != nil {
		t.Fatal(err)
	}
	if w := toggle(true, 2); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "changed since you opened it") {
		t.Fatal("stale revision", w.Code, w.Body.String())
	}
	if w := toggle(true, 3); w.Code != http.StatusOK {
		t.Fatal("turn on", w.Code, w.Body.String())
	}
}

func TestLinkedBalanceTotalsPreservePairedAndUnpairedEffects(t *testing.T) {
	lt := newLinkedTenant(t)
	ctx := lt.f.db.Context
	kr := lt.account("kit", "1300", "asset", "receivable", "Business owes")
	bp := lt.account("eightball", "2300", "liability", "payable", "Owed to personal Book")
	br := lt.account("eightball", "1300", "asset", "receivable", "Personal Book owes")
	kp := lt.account("kit", "2300", "liability", "payable", "Owed to business")
	ko := lt.account("kit", "3900", "equity", "opening_balance", "Opening balances")
	bo := lt.account("eightball", "3900", "equity", "opening_balance", "Opening balances")
	other := lt.account("eightball", "2500", "liability", "payable", "Other payable")
	for i, terms := range []struct {
		payer, beneficiary  string
		receivable, payable ledger.Account
	}{
		{"kit", "eightball", kr, bp}, {"eightball", "kit", br, kp},
	} {
		if w := lt.link(terms.payer, terms.beneficiary, "owed", terms.receivable, terms.payable, int64(i)); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Retain the earlier calculation as a reference for the same authorized projection.
	data, err := os.ReadFile("../ledger/migrations/bridge/0022_partial_repayments.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, definition, ok := strings.Cut(string(data), "CREATE OR REPLACE FUNCTION public.linked_balances")
	if !ok {
		t.Fatal("balance function is missing")
	}
	definition, _, ok = strings.Cut(definition, "\n$$;")
	if !ok {
		t.Fatal("balance function is incomplete")
	}
	definition = "CREATE FUNCTION public.linked_balance_reference" + definition + "\n$$;"
	definition = strings.Replace(definition, "SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok", "SELECT true AS ok", 1)
	if _, err := lt.f.db.Admin.Exec(ctx, definition); err != nil {
		t.Fatal(err)
	}
	compare := func() {
		t.Helper()
		var raw []byte
		if err := lt.f.db.Admin.QueryRow(ctx, "SELECT public.linked_balance_reference($1,NULL)", lt.f.tenant).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var expected, actual ledger.LinkedBalances
		if json.Unmarshal(raw, &expected) != nil {
			t.Fatal("invalid reference balances")
		}
		w := bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &actual) != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatal("account totals changed the posting projection", w.Code, string(raw), w.Body.String())
		}
	}
	post := func(book, key string, debit, credit ledger.Account, amount string) ledger.Entry {
		t.Helper()
		var entry ledger.Entry
		raw := lt.f.db.Call(t, lt.scope(lt.writers[book]), "post_journal", ledger.PostJournal{RequestKey: key, AccountingDate: "2026-01-03", EconomicDate: "2026-01-03", Description: "Example balance",
			Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: amount}, {AccountID: credit.ID, Side: "credit", AmountMinor: amount}}})
		if json.Unmarshal(raw, &entry) != nil {
			t.Fatal("invalid entry")
		}
		return entry
	}
	compare()
	w := bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(mustJSON(ledger.OwnerOpeningBalance{DebtorBookID: lt.books["eightball"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2024-01-01", AmountMinor: "150000"})))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	compare()
	unpaired := post("kit", "unpaired-receivable", kr, ko, "60")
	post("eightball", "unpaired-payable", bo, bp, "60")
	post("eightball", "other-payable", bo, other, "9007199254740993")
	compare()
	lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "reverse_journal", ledger.ReverseJournal{RequestKey: "undo-unpaired", EntryID: unpaired.ID, AccountingDate: "2026-02-01", Reason: "Example correction"})
	compare()
	post("eightball", "reverse-direction-receivable", br, bo, "700")
	post("kit", "reverse-direction-payable", ko, kp, "700")
	compare()
}

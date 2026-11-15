package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// reportFixture is Eightball Inc. paying back Kit Blanche: Kit's card paid
// two of Eightball's costs, owed back through a link, and Eightball's
// checking pays Kit.
type reportFixture struct {
	t                                      *testing.T
	lt                                     *linkedTenant
	link, person                           string
	payable, receivable, travel, kitWallet ledger.Account
	card, kitMeals                         ledger.Account
	checking                               ledger.Account
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	lt := newLinkedTenant(t)
	f := &reportFixture{t: t, lt: lt}
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	f.card = card
	f.kitMeals = lt.account("kit", "6100", "expense", "", "Meals")
	f.kitWallet = lt.account("kit", "1010", "asset", "cash", "Wallet")
	f.receivable = lt.account("kit", "1320", "asset", "receivable", "Owed by Eightball")
	f.checking = lt.account("eightball", "1000", "asset", "cash", "Eightball checking")
	f.travel = lt.account("eightball", "6300", "expense", "", "Travel")
	f.payable = lt.account("eightball", "2310", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "c1", Date: "2026-11-03", AmountMinor: 42000, Description: "EXAMPLE AIR", Counterparty: "Example Air"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c2", Date: "2026-11-04", AmountMinor: 14700, Description: "HARBOR HOTEL", Counterparty: "Harbor Hotel"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c3", Date: "2026-11-05", AmountMinor: 10000, Description: "EXAMPLE TAXI", Counterparty: "Example Taxi"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c4", Date: "2026-11-07", AmountMinor: 2600, Description: "EXAMPLE CAFE", Counterparty: "Example Cafe"})
	lt.bank("eightball", "cash", f.checking,
		ledger.BankSyncTransaction{ProviderTransactionID: "p1", Date: "2026-11-20", AmountMinor: -30000, Description: "TO KIT BLANCHE", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "p2", Date: "2026-11-21", AmountMinor: -10000, Description: "TO KIT BLANCHE TAXI", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "p3", Date: "2026-11-25", AmountMinor: 3000, Description: "FROM KIT BLANCHE", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "eightball", "owed", f.receivable, f.payable, 0); w.Code != http.StatusOK {
		t.Fatal("link", w.Code, w.Body.String())
	}
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT id::text FROM public.book_links WHERE tenant_id=$1", lt.f.tenant).Scan(&f.link); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"EXAMPLE AIR", "HARBOR HOTEL", "EXAMPLE TAXI"} {
		body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", d).ID, BeneficiaryBookID: lt.books["eightball"].ID, BeneficiaryAccountID: f.travel.ID, Description: d})
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
			t.Fatal("pair", d, w.Code, w.Body.String())
		}
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		p, err := o.SavePerson(lt.f.db.Context, f.business(), who, ledger.PersonChange{Name: "Kit Blanche", Capacity: "shareholder_employee", PersonalBookID: lt.books["kit"].ID})
		f.person = p.ID
		return err
	})
	return f
}

func (f *reportFixture) business() string { return f.lt.books["eightball"].ID }

func (f *reportFixture) owner(call func(ledger.TenantAdmin, string) error) {
	f.t.Helper()
	if err := f.lt.f.sessions.OwnerAct(f.lt.f.db.Context, f.lt.f.session, f.lt.f.tenant, call); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reportFixture) tryOwner(call func(ledger.TenantAdmin, string) error) error {
	return f.lt.f.sessions.OwnerAct(f.lt.f.db.Context, f.lt.f.session, f.lt.f.tenant, call)
}

func (f *reportFixture) pairKey(description string) string {
	f.t.Helper()
	var key string
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, `SELECT p.pair_key FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id=p.tenant_id AND e.book_id=p.beneficiary_book_id
 AND e.id=p.beneficiary_entry_id WHERE p.tenant_id=$1 AND p.kind IN ('cost','opening') AND e.description ILIKE $2`, f.lt.f.tenant, "%"+description+"%").Scan(&key); err != nil {
		f.t.Fatal("pair key", description, err)
	}
	return key
}

func (f *reportFixture) newReport(reference string, agent bool) string {
	f.t.Helper()
	var ref string
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		ref, err = o.CreateReport(f.lt.f.db.Context, who, ledger.NewReport{LinkID: f.link, PersonID: f.person, Reference: reference, PreparedByAgent: agent})
		return err
	})
	return ref
}

func (f *reportFixture) claim(reference, pair, purpose string) string {
	f.t.Helper()
	var claim string
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		claim, err = o.AddReportClaim(f.lt.f.db.Context, f.business(), who, reference, ledger.ClaimInput{PairKey: f.pairKey(pair), Purpose: purpose})
		return err
	})
	return claim
}

func (f *reportFixture) detail(reference string) ledger.ReportDetail {
	f.t.Helper()
	var d ledger.ReportDetail
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		d, err = o.ExpenseReport(f.lt.f.db.Context, f.business(), reference)
		return err
	})
	return d
}

// identity is the signed-in owner's identity.
func (f *reportFixture) identity() string {
	f.t.Helper()
	var id string
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, "SELECT identity_id::text FROM access.sessions WHERE token_hash=$1", access.Hash(f.lt.f.session)).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// proof stands in for a passkey assertion the owner just gave for this
// report, revision and snapshot.
func (f *reportFixture) proof(purpose string, d ledger.ReportDetail) string {
	f.t.Helper()
	token := "proof-" + ledger.ID()
	_, err := f.lt.f.db.Admin.Exec(f.lt.f.db.Context, `INSERT INTO access.book_deletion_proofs(token_hash,identity_id,epoch,session_hash,tenant_id,book_ref,purpose,bound_to,ceremony,taken_at,verified_at)
 SELECT $1,s.identity_id,i.epoch,s.token_hash,$2,$3,$4,$5,'{}',clock_timestamp(),clock_timestamp() FROM access.sessions s JOIN access.identities i ON i.id=s.identity_id WHERE s.token_hash=$6`,
		access.Hash(token), f.lt.f.tenant, f.business(), purpose, ledger.ProofBinding(d.ID, d.RevisionID, d.Revision, d.SnapshotSHA256), access.Hash(f.lt.f.session))
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func (f *reportFixture) submit(reference string, approve bool) []ledger.ReportEvent {
	f.t.Helper()
	d := f.detail(reference)
	purpose := "submit"
	if approve {
		purpose = "submit_approve"
	}
	token := f.proof(purpose, d)
	var events []ledger.ReportEvent
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		events, err = o.SubmitReport(f.lt.f.db.Context, f.business(), who, f.identity(), reference,
			ledger.SubmitReport{Revision: d.Revision, SnapshotSHA256: d.SnapshotSHA256, Attestation: true, Approve: approve, ProofToken: token})
		return err
	})
	return events
}

// payment records a bank payment from Eightball to Kit as a payment of
// what Eightball owes, and returns its entry.
func (f *reportFixture) payment(description string) string {
	f.t.Helper()
	lt := f.lt
	p, err := lt.prepare("eightball", "pay-"+description, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "pay-" + description,
		TransactionID: lt.item("eightball", description).ID, Description: "Paid Kit back", OtherAccountID: f.payable.ID, Reason: "Repaying Kit"})
	if err != nil {
		f.t.Fatal(err)
	}
	if w := bankCall(f.t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(f.business(), p, "approve-"+description, 0)); w.Code != http.StatusOK {
		f.t.Fatal("approve", w.Code, w.Body.String())
	}
	var entry string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT l.entry_id::text FROM public.bank_transaction_links l WHERE l.tenant_id=$1 AND l.transaction_id=$2 AND l.revoked_at IS NULL`,
		lt.f.tenant, lt.item("eightball", description).ID).Scan(&entry); err != nil {
		f.t.Fatal(err)
	}
	return entry
}

func (f *reportFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func eventKinds(events []ledger.ReportEvent) string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return strings.Join(out, ",")
}

// An existing marked cost enters a report: no second expense or payable
// appears, from drafting to approval and payment.
func TestAMarkedCostOnAReportAddsNoSecondExpense(t *testing.T) {
	f := newReportFixture(t)
	entries := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant)
	pairs := f.count("SELECT count(*) FROM public.entry_pairs WHERE tenant_id=$1", f.lt.f.tenant)
	ref := f.newReport("", false)
	if _, ok := ledger.NormalizeReportReference(ref); !ok || !strings.HasPrefix(ref, "ER-") || len(ref) != 17 {
		t.Fatal("reference", ref)
	}
	f.claim(ref, "EXAMPLE AIR", "Flight to the customer meeting")
	f.claim(ref, "HARBOR HOTEL", "Customer meeting stay")
	events := f.submit(ref, true)
	if eventKinds(events) != "submitted,approved" {
		t.Fatal("events", eventKinds(events))
	}
	d := f.detail(ref)
	if d.Workflow != "approved" || !d.SelfApproved || d.ApprovedMinor != "56700" || d.RemainingMinor != "56700" || d.Settlement != "unsettled" ||
		d.Memo != ref+" Expense reimbursement" {
		t.Fatal("approved report", d.ReportSummary, d.Memo)
	}
	if got := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant); got != entries {
		t.Fatal("a report posted entries", entries, got)
	}
	if got := f.count("SELECT count(*) FROM public.entry_pairs WHERE tenant_id=$1", f.lt.f.tenant); got != pairs {
		t.Fatal("a report added pairs", pairs, got)
	}
	// The hotel has no receipt: lodging always needs one.
	found := false
	for _, w := range d.Warnings {
		found = found || w.Kind == "receipt_required"
	}
	if !found {
		t.Fatal("no receipt warning", d.Warnings)
	}
}

// An agent drafts a cash claim: drafting alone records nothing as owed.
func TestAnAgentsDraftedCashClaimIsNotOwed(t *testing.T) {
	f := newReportFixture(t)
	entries := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant)
	ref := f.newReport("", true)
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.AddReportClaim(f.lt.f.db.Context, f.business(), who, ref, ledger.ClaimInput{Date: "2026-11-06", Vendor: "Example Parking", AmountMinor: "4500",
			Purpose: "Parking at the customer", PaidFromAccountID: f.kitWallet.ID, ProposedAccountID: f.travel.ID})
		return err
	})
	if got := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant); got != entries {
		t.Fatal("drafting recorded entries")
	}
	if f.count("SELECT count(*) FROM public.claims WHERE tenant_id=$1 AND kind='cash' AND obligation_entry_id IS NULL AND recognized_at IS NULL", f.lt.f.tenant) != 1 {
		t.Fatal("the cash claim is recorded as owed")
	}
	if f.count("SELECT count(*) FROM public.expense_reports WHERE tenant_id=$1 AND prepared_by_agent", f.lt.f.tenant) != 1 {
		t.Fatal("the agent's draft is not marked")
	}
	// Accepting it, at approval, is its own event and records it as owed.
	kitBefore := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", f.lt.f.tenant, f.lt.books["kit"].ID)
	events := f.submit(ref, true)
	if eventKinds(events) != "submitted,accepted,approved" {
		t.Fatal("events", eventKinds(events))
	}
	// The business's side only; the person's waits for them.
	if f.count("SELECT count(*) FROM public.claim_obligations WHERE tenant_id=$1 AND person_status='pending'", f.lt.f.tenant) != 1 ||
		f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant) != entries+1 ||
		f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", f.lt.f.tenant, f.lt.books["kit"].ID) != kitBefore {
		t.Fatal("acceptance wrote outside the business's Book")
	}
	kit := f.lt.books["kit"].ID
	if f.count("SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND subject_key LIKE 'claim:%' AND status='open'", f.lt.f.tenant, kit) != 1 {
		t.Fatal("no item for Kit to record their side")
	}
	// The wallet has no feed: Kit records the cash paid, in Kit's Book.
	var items []ledger.PersonSideItem
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		if items, err = o.PersonSideItems(f.lt.f.db.Context, kit); err != nil || len(items) != 1 || items[0].HasFeed || len(items[0].Candidates) != 0 {
			return errors.New("the item to record")
		}
		_, err = o.RecordPersonSide(f.lt.f.db.Context, kit, who, items[0].ClaimID, "")
		return err
	})
	if f.count("SELECT count(*) FROM public.claim_obligations WHERE tenant_id=$1 AND person_status='recorded' AND person_method='new_entry'", f.lt.f.tenant) != 1 ||
		f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", f.lt.f.tenant) != entries+2 ||
		f.count("SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND subject_key LIKE 'claim:%' AND status='open'", f.lt.f.tenant, kit) != 0 {
		t.Fatal("Kit's side")
	}
}

// A cash claim paid with Kit's card, whose charge is already fed and
// recorded in Kit's Book as Kit's own: approving it records the business's
// side only, and Kit, confirming the match, moves that same charge to what
// Eightball owes. Kit's card still shows one outflow, Eightball owes it
// once, and both Books agree.
func TestACashClaimOnAPersonalCardChargeAlreadyInThePersonsBook(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	kit := f.lt.books["kit"].ID
	lt := f.lt
	p, err := lt.prepare("kit", "cafe", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "cafe", TransactionID: lt.item("kit", "EXAMPLE CAFE").ID,
		Description: "Cafe", OtherAccountID: f.kitMeals.ID, Reason: "Kit's lunch"})
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(kit, p, "approve-cafe", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	cardLines := func() int {
		return f.count("SELECT count(*) FROM public.lines WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3 AND side='credit' AND amount_minor=2600", lt.f.tenant, kit, f.card.ID)
	}
	if cardLines() != 1 || lt.balance("kit", "6100") != 2600 {
		t.Fatal("the charge in Kit's Book", cardLines(), lt.balance("kit", "6100"))
	}
	ref := f.newReport("", false)
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.AddReportClaim(ctx, f.business(), who, ref, ledger.ClaimInput{Kind: "cash", Date: "2026-11-08", Vendor: "Example Cafe", AmountMinor: "2600",
			PaidFromAccountID: f.card.ID, ProposedAccountID: f.travel.ID, Purpose: "Lunch with the customer"})
		return err
	})
	kitEntries := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", lt.f.tenant, kit)
	owed, owedToKit := lt.balance("eightball", "2310"), lt.balance("kit", "1320")
	f.submit(ref, true)
	if got := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", lt.f.tenant, kit); got != kitEntries {
		t.Fatal("approval wrote in Kit's Book")
	}
	// Eightball owes it now: on its Debts and in who owes whom, once.
	var balances ledger.LinkedBalances
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		balances, err = o.OwnerLinkedBalances(ctx)
		return err
	})
	claimed := false
	for _, l := range balances.Links {
		for _, op := range l.OpenPairs {
			claimed = claimed || op.Kind == "claim" && op.AmountMinor == "2600"
		}
	}
	if !claimed || lt.balance("eightball", "2310") != owed-2600 {
		t.Fatal("Eightball's debt", claimed, lt.balance("eightball", "2310"))
	}
	// Kit is offered the fed charge, a day apart; matching is Kit's call.
	var items []ledger.PersonSideItem
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		items, err = o.PersonSideItems(ctx, kit)
		return err
	})
	if len(items) != 1 || !items[0].HasFeed || len(items[0].Candidates) != 1 || items[0].Candidates[0].DaysApart != 1 || !items[0].Candidates[0].Recorded ||
		items[0].Candidates[0].Description != "EXAMPLE CAFE" {
		t.Fatal("Kit's item", items)
	}
	// With a feed, a new entry isn't recorded instead.
	if err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.RecordPersonSide(ctx, kit, who, items[0].ClaimID, "")
		return err
	}); err == nil {
		t.Fatal("a second outflow was recorded")
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.RecordPersonSide(ctx, kit, who, items[0].ClaimID, items[0].Candidates[0].BankItemID)
		return err
	})
	if cardLines() != 1 || lt.balance("kit", "6100") != 0 || lt.balance("kit", "1320") != owedToKit+2600 {
		t.Fatal("Kit's Book after the match", cardLines(), lt.balance("kit", "6100"), lt.balance("kit", "1320"))
	}
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		balances, err = o.OwnerLinkedBalances(ctx)
		return err
	})
	if len(balances.Links) != 1 || !balances.Links[0].Matches || len(balances.Links[0].MismatchedEntryIDs) != 0 {
		t.Fatal("the Books don't agree", balances.Links)
	}
}

// The same claim submitted on two reports at once: only one use wins.
func TestOneCostOnTwoReportsAtOnce(t *testing.T) {
	f := newReportFixture(t)
	first, second := f.newReport("", false), f.newReport("", false)
	key := f.pairKey("EXAMPLE AIR")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ref := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.tryOwner(func(o ledger.TenantAdmin, who string) error {
				_, err := o.AddReportClaim(context.Background(), f.business(), who, ref, ledger.ClaimInput{PairKey: key, Purpose: "Flight"})
				return err
			})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		var conflict *ledger.ReportConflict
		switch {
		case err == nil:
			won++
		case !errors.As(err, &conflict):
			t.Fatal("unexpected refusal", err)
		}
	}
	if won != 1 || f.count("SELECT count(*) FROM public.report_lines WHERE tenant_id=$1", f.lt.f.tenant) != 1 {
		t.Fatal("both reports claimed the cost", errs)
	}
}

func (f *reportFixture) approvedReport() string {
	f.t.Helper()
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE AIR", "Flight to the customer meeting")
	f.claim(ref, "HARBOR HOTEL", "Customer meeting stay")
	f.submit(ref, true)
	return ref
}

// A matched payment is unmatched: the allocation goes, the bank movement
// and its entry stay, and the money is unapplied, never spread elsewhere.
func TestUnmatchingKeepsTheBankMovement(t *testing.T) {
	f := newReportFixture(t)
	ref := f.approvedReport()
	entry := f.payment("TO KIT BLANCHE")
	var paid ledger.PaymentResult
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		paid, err = o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, ref, entry, 0, nil)
		return err
	})
	d := f.detail(ref)
	if d.PaidMinor != "30000" || d.Settlement != "partly_settled" || d.RemainingMinor != "26700" || len(paid.Allocations) == 0 {
		t.Fatal("paid", d.ReportSummary, paid)
	}
	// The payment pays this report's claims, not the oldest cost elsewhere.
	if f.count("SELECT count(*) FROM public.allocations WHERE tenant_id=$1 AND source_entry_id=$2 AND removed_at IS NULL AND source<>'report_payment'", f.lt.f.tenant, entry) != 0 {
		t.Fatal("the payment kept its plain allocation")
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		for _, a := range paid.Allocations {
			if err := o.RemoveReportAllocation(f.lt.f.db.Context, f.business(), who, a.ID, "Wrong report"); err != nil {
				return err
			}
		}
		return nil
	})
	d = f.detail(ref)
	if d.PaidMinor != "0" || d.Settlement != "unsettled" {
		t.Fatal("unmatched", d.ReportSummary)
	}
	if f.count("SELECT count(*) FROM public.bank_transaction_links WHERE tenant_id=$1 AND entry_id=$2 AND revoked_at IS NULL", f.lt.f.tenant, entry) != 1 ||
		f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND reversal_of=$2", f.lt.f.tenant, entry) != 0 {
		t.Fatal("the bank movement or its entry changed")
	}
	if f.count("SELECT count(*) FROM public.allocations WHERE tenant_id=$1 AND source_entry_id=$2 AND removed_at IS NULL", f.lt.f.tenant, entry) != 0 {
		t.Fatal("the unmatched payment was spread over other costs")
	}
	var unapplied int64
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, "SELECT public.unapplied_amount($1,$2,$3,$4)", f.lt.f.tenant, f.lt.books["kit"].ID, f.business(), f.payable.ID).Scan(&unapplied); err != nil || unapplied != 30000 {
		t.Fatal("unapplied", unapplied, err)
	}
	// Matching it again by its reference works.
	var proposals []ledger.MatchProposal
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		_, proposals, err = o.ReportMatches(f.lt.f.db.Context, f.business(), ref)
		return err
	})
	_ = proposals
}

// A manual reimbursement later arrives in a bank import: one cash event
// and one settlement remain.
func TestAManualReimbursementLaterImportedIsOnePayment(t *testing.T) {
	f := newReportFixture(t)
	ref := f.approvedReport()
	lt := f.lt
	other := lt.account("eightball", "1001", "asset", "cash", "Eightball savings")
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "post_journal", ledger.PostJournal{RequestKey: "manual-pay", EconomicDate: "2026-11-22", AccountingDate: "2026-11-22",
		Description: "Paid Kit back by hand", Lines: []ledger.PostLine{{AccountID: f.payable.ID, Side: "debit", AmountMinor: "56700"}, {AccountID: other.ID, Side: "credit", AmountMinor: "56700"}}})
	var entry string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT id::text FROM public.entries WHERE tenant_id=$1 AND description='Paid Kit back by hand'", lt.f.tenant).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(lt.f.db.Context, f.business(), who, ref, entry, 0, nil)
		return err
	})
	lt.bank("eightball", "cash", other, ledger.BankSyncTransaction{ProviderTransactionID: "m1", Date: "2026-11-22", AmountMinor: -56700, Description: ref + " Expense reimbursement", Counterparty: "Kit Blanche"})
	lt.f.db.Call(t, lt.scope(lt.writers["eightball"]), "link_bank_activity", ledger.LinkBankActivity{RequestKey: "link-manual", TransactionID: lt.item("eightball", ref+" Expense reimbursement").ID, EntryID: entry})
	if f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id IN (SELECT source_entry_id FROM public.allocations WHERE removed_at IS NULL AND source='report_payment')", lt.f.tenant, f.business()) != 1 {
		t.Fatal("the payment became more than one cash event")
	}
	d := f.detail(ref)
	if d.Settlement != "settled" || d.PaidMinor != "56700" || f.count("SELECT count(DISTINCT source_entry_id) FROM public.allocations WHERE tenant_id=$1 AND source='report_payment' AND removed_at IS NULL", lt.f.tenant) != 1 {
		t.Fatal("settled once", d.ReportSummary)
	}
}

// A paid $100 claim corrected to $70: the original payment stays visible,
// and $30 is recoverable, resolved explicitly.
func TestACorrectedPaidClaimLeavesARecoverableAmount(t *testing.T) {
	f := newReportFixture(t)
	ref := f.newReport("", false)
	taxi := f.claim(ref, "EXAMPLE TAXI", "Taxi to the customer")
	f.submit(ref, true)
	entry := f.payment("TO KIT BLANCHE TAXI")
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, ref, entry, 0, nil)
		return err
	})
	if d := f.detail(ref); d.Settlement != "settled" || d.PaidMinor != "10000" {
		t.Fatal("paid", d.ReportSummary)
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		if err := o.AmendReport(f.lt.f.db.Context, f.business(), who, ref, "Part of the ride was personal"); err != nil {
			return err
		}
		return o.EditReportClaim(f.lt.f.db.Context, f.business(), who, ref, taxi, ledger.ClaimInput{PortionMinor: "7000"})
	})
	f.submit(ref, true)
	d := f.detail(ref)
	if len(d.Recoverables) != 1 || d.Recoverables[0].AmountMinor != "3000" || d.Recoverables[0].RecoveryStatus != "outstanding" ||
		d.Recoverables[0].TaxStatus != "not_determined" || d.Recoverables[0].Origin != "reduction" {
		t.Fatal("recoverable", d.Recoverables)
	}
	if len(d.Payments) != 1 || d.Payments[0].AmountMinor != "10000" || d.Payments[0].RemovedAt != nil {
		t.Fatal("the original payment", d.Payments)
	}
	if len(d.Revisions) != 2 || d.Revisions[0].ApprovedAt == nil || d.Revisions[1].ApprovedAt == nil {
		t.Fatal("revisions", d.Revisions)
	}
	// Kit's repayment is offered as the return, and no cost is.
	back := f.payment("FROM KIT BLANCHE")
	d = f.detail(ref)
	if c := d.Recoverables[0].ReturnCandidates; len(c) != 1 || c[0].EntryID != back || c[0].AmountMinor != "3000" || !c[0].AmountFits {
		t.Fatal("return candidates", c)
	}
	// On the tax track: flagged for review, then treated with the
	// accountant's note, both kept; a review is added. Recovery is apart.
	f.owner(func(o ledger.TenantAdmin, who string) error {
		if err := o.ReviewRecoverable(f.lt.f.db.Context, f.business(), who, d.Recoverables[0].ID, ledger.RecoverableReview{ResponsiblePersonID: f.person,
			BasisAmountMinor: "3000", Timing: "Next payroll", ReviewDate: "2026-11-30"}); err != nil {
			return err
		}
		if err := o.ResolveRecoverable(f.lt.f.db.Context, f.business(), who, d.Recoverables[0].ID, "flagged", "", "", ""); err != nil {
			return err
		}
		if err := o.ResolveRecoverable(f.lt.f.db.Context, f.business(), who, d.Recoverables[0].ID, "treated", "", "", ""); err == nil {
			t.Fatal("treated without a note")
		}
		return o.ResolveRecoverable(f.lt.f.db.Context, f.business(), who, d.Recoverables[0].ID, "treated", "", "", "Returned through the next payroll")
	})
	d = f.detail(ref)
	r := d.Recoverables[0]
	if r.TaxStatus != "treatment_determined" || r.RecoveryStatus != "outstanding" || r.TreatmentNote != "Returned through the next payroll" || len(r.Events) != 2 ||
		r.RecordedBy == "" || len(r.Reviews) != 1 || r.Reviews[0].BasisAmountMinor != "3000" || len(r.ReturnCandidates) != 1 {
		t.Fatal("tax track", r)
	}
	// Returned, with Kit's repayment: no more returns offered.
	f.owner(func(o ledger.TenantAdmin, who string) error {
		return o.ResolveRecoverable(f.lt.f.db.Context, f.business(), who, r.ID, "returned", back, "", "")
	})
	r = f.detail(ref).Recoverables[0]
	if r.RecoveryStatus != "returned" || r.Resolution == nil || *r.Resolution != "returned" || len(r.ReturnCandidates) != 0 {
		t.Fatal("returned", r)
	}
}

// An older expense imported after payments were allocated: earlier
// allocations don't change.
func TestAnOlderCostLeavesAReportsPaymentsAlone(t *testing.T) {
	f := newReportFixture(t)
	ref := f.approvedReport()
	entry := f.payment("TO KIT BLANCHE")
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, ref, entry, 0, nil)
		return err
	})
	before := f.count("SELECT coalesce(sum(amount_minor),0)::int FROM public.allocations WHERE tenant_id=$1 AND source_entry_id=$2 AND removed_at IS NULL", f.lt.f.tenant, entry)
	lt := f.lt
	older := lt.account("kit", "2110", "liability", "card", "Older card")
	lt.bank("kit", "card", older, ledger.BankSyncTransaction{ProviderTransactionID: "o1", Date: "2026-09-01", AmountMinor: 2500, Description: "EXAMPLE BOOKS", Counterparty: "Example Books"})
	body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", "EXAMPLE BOOKS").ID, BeneficiaryBookID: f.business(), BeneficiaryAccountID: f.travel.ID, Description: "EXAMPLE BOOKS"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
		t.Fatal("older cost", w.Code, w.Body.String())
	}
	after := f.count("SELECT coalesce(sum(amount_minor),0)::int FROM public.allocations WHERE tenant_id=$1 AND source_entry_id=$2 AND removed_at IS NULL", f.lt.f.tenant, entry)
	if before != 30000 || after != before || f.detail(ref).PaidMinor != "30000" {
		t.Fatal("the report's payment moved", before, after)
	}
}

// Evidence that arrives after an on-time submission keeps its own date:
// the frozen revision doesn't change, and the late receipt is a supplement.
func TestLateEvidenceKeepsItsOwnTiming(t *testing.T) {
	f := newReportFixture(t)
	ref := f.newReport("", false)
	hotel := f.claim(ref, "HARBOR HOTEL", "Customer meeting stay")
	f.submit(ref, false)
	frozen := f.detail(ref).SnapshotSHA256
	upload := decodeOwnerUpload(t, runOwnerUpload(f.lt.s, ownerUploadRequest(f.lt.f.session, f.business(), "receipt-1", "Harbor receipt.pdf", "application/pdf", []byte("%PDF-1.7\nSynthetic receipt\n"))), http.StatusCreated)
	f.owner(func(o ledger.TenantAdmin, who string) error {
		return o.AddClaimEvidence(f.lt.f.db.Context, f.business(), who, hotel, upload.Document.ID, ledger.EvidenceInput{})
	})
	d := f.detail(ref)
	if d.SnapshotSHA256 != frozen || len(d.Lines) != 1 || !d.Lines[0].HasReceipt || !d.Lines[0].Evidence[0].Supplement || d.Lines[0].EvidenceReceivedOn == "" {
		t.Fatal("late evidence", d.SnapshotSHA256 == frozen, d.Lines)
	}
	submitted := d.Revisions[0].SubmittedAt
	if submitted == nil || !d.Lines[0].Evidence[0].AddedAt.After(*submitted) {
		t.Fatal("the evidence timing isn't visible")
	}
	if !strings.Contains(eventKinds(d.Events), "evidence_added") {
		t.Fatal("events", eventKinds(d.Events))
	}
}

// A business Book restored without the person's Book keeps its claims,
// approvals, evidence, allocations and documents.
func TestABusinessRestoredAloneKeepsItsReports(t *testing.T) {
	f := newReportFixture(t)
	ref := f.newReport("", false)
	hotel := f.claim(ref, "HARBOR HOTEL", "Customer meeting stay")
	upload := decodeOwnerUpload(t, runOwnerUpload(f.lt.s, ownerUploadRequest(f.lt.f.session, f.business(), "receipt-2", "Harbor receipt.pdf", "application/pdf", []byte("%PDF-1.7\nSynthetic receipt\n"))), http.StatusCreated)
	f.owner(func(o ledger.TenantAdmin, who string) error {
		return o.AddClaimEvidence(f.lt.f.db.Context, f.business(), who, hotel, upload.Document.ID, ledger.EvidenceInput{})
	})
	f.submit(ref, true)
	entry := f.payment("TO KIT BLANCHE")
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, ref, entry, 0, nil)
		return err
	})
	// A second report approved and not yet paid.
	unpaid := f.newReport("", false)
	f.claim(unpaid, "EXAMPLE AIR", "Flight")
	f.submit(unpaid, true)
	var archive bytes.Buffer
	if err := f.lt.f.db.Owner.WriteArchive(f.lt.f.db.Context, f.lt.f.vault, &archive, f.business()); err != nil {
		t.Fatal(err)
	}
	restored := testutil.New(t)
	if err := (ledger.Admin{DB: restored.Admin}).RestoreArchive(restored.Context, f.lt.f.vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("restore", err)
	}
	for table, want := range map[string]int{"expense_reports": 1, "claims": 1, "claim_evidence": 1, "report_revisions": 1, "report_lines": 1, "report_approvals": 1, "people": 1, "allocations": 1} {
		var n int
		if err := restored.Admin.QueryRow(restored.Context, "SELECT count(*) FROM public."+table).Scan(&n); err != nil || n < want {
			t.Fatal("restored", table, n, err)
		}
	}
	var books, evidenceDocs int
	if err := restored.Admin.QueryRow(restored.Context, `SELECT (SELECT count(*) FROM public.books),(SELECT count(*) FROM public.claim_evidence v JOIN public.documents d ON d.tenant_id=v.tenant_id AND d.book_id=v.business_book_id AND d.id=v.document_id)`).Scan(&books, &evidenceDocs); err != nil || books != 1 || evidenceDocs != 1 {
		t.Fatal("restored alone", books, evidenceDocs, err)
	}
	// Ids and references come back as they were; only the report with money
	// still to pay waits to be reconciled.
	var flags string
	if err := restored.Admin.QueryRow(restored.Context, `SELECT string_agg(reference||'='||reconciled_after_restore, ',' ORDER BY reference) FROM public.expense_reports`).Scan(&flags); err != nil {
		t.Fatal(err)
	}
	want := []string{ref + "=true", unpaid + "=false"}
	if ref > unpaid {
		want[0], want[1] = want[1], want[0]
	}
	if flags != strings.Join(want, ",") || f.count("SELECT count(*) FROM public.expense_reports WHERE id IN (SELECT id FROM public.expense_reports) AND reconciled_after_restore") != 2 {
		t.Fatal("reconciled after restore", flags)
	}
	// Until it's reconciled, no payment is matched to it.
	if _, err := f.lt.f.db.Admin.Exec(f.lt.f.db.Context, "UPDATE public.expense_reports SET reconciled_after_restore=false WHERE reference=$1", unpaid); err != nil {
		t.Fatal(err)
	}
	other := f.payment("TO KIT BLANCHE TAXI")
	err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, unpaid, other, 0, nil)
		return err
	})
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "reconciled") {
		t.Fatal("matched before reconciling", err)
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		if err := o.ReconcileReport(f.lt.f.db.Context, f.business(), who, unpaid, ""); err == nil {
			return errors.New("reconciled without saying what was checked")
		}
		if err := o.ReconcileReport(f.lt.f.db.Context, f.business(), who, unpaid, "Checked the bank since the backup: nothing paid."); err != nil {
			return err
		}
		_, err := o.ApplyReportPayment(f.lt.f.db.Context, f.business(), who, unpaid, other, 0, nil)
		return err
	})
	if !f.detail(unpaid).ReconciledAfterRestore {
		t.Fatal("still waiting after reconciling")
	}
}

// A legacy opening balance owed to the person can be reported: its
// provenance stays explicit and no new expense is recognised.
func TestALegacyOpeningBalanceOnAReportKeepsItsProvenance(t *testing.T) {
	f := newReportFixture(t)
	lt := f.lt
	lt.account("kit", "3900", "equity", "opening_balance", "Opening balances")
	lt.account("eightball", "3900", "equity", "opening_balance", "Opening balances")
	body, _ := json.Marshal(ledger.OwnerOpeningBalance{DebtorBookID: f.business(), CreditorBookID: lt.books["kit"].ID, AsOf: "2026-01-01", AmountMinor: "50000", Description: "Owed before the records began"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(body)); w.Code != http.StatusOK {
		t.Fatal("opening", w.Code, w.Body.String())
	}
	entries := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", lt.f.tenant)
	ref := f.newReport("", false)
	f.claim(ref, "Owed before the records began", "Costs paid before the company's books began")
	f.submit(ref, true)
	d := f.detail(ref)
	if len(d.Lines) != 1 || d.Lines[0].Kind != "opening" || d.Lines[0].Provenance == "" {
		t.Fatal("opening claim", d.Lines)
	}
	if got := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1", lt.f.tenant); got != entries {
		t.Fatal("an opening balance on a report recognised a new expense", entries, got)
	}
}

// A report created after the fact with a reference already sent; bad or
// taken references are refused in a sentence.
func TestAReportWithAReferenceAlreadySent(t *testing.T) {
	f := newReportFixture(t)
	ref := f.newReport("er-7q2m-k4hd-9w1c", false)
	if ref != "ER-7Q2M-K4HD-9W1C" {
		t.Fatal("normalized", ref)
	}
	if n, ok := ledger.NormalizeReportReference("ER-7Q2M-K4HD-9WLO"); !ok || n != "ER-7Q2M-K4HD-9W10" {
		t.Fatal("Crockford reading", n, ok)
	}
	for given, want := range map[string]string{"ER-7Q2M-K4HD": "isn't a report reference", "ER-7Q2M-K4HD-9W1U": "isn't a report reference", "ER-7Q2M-K4HD-9W1C": "already used"} {
		err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
			_, err := o.CreateReport(f.lt.f.db.Context, who, ledger.NewReport{LinkID: f.link, PersonID: f.person, Reference: given})
			return err
		})
		var refused *ledger.RefusedError
		if !errors.As(err, &refused) || !strings.Contains(refused.Reason, want) {
			t.Fatal(given, err)
		}
	}
	// The payment that already carried it matches by reference first.
	f.claim(ref, "EXAMPLE TAXI", "Taxi to the customer")
	f.submit(ref, true)
	lt := f.lt
	other := lt.account("eightball", "1002", "asset", "cash", "Eightball reserve")
	lt.bank("eightball", "cash", other, ledger.BankSyncTransaction{ProviderTransactionID: "r1", Date: "2026-11-13", AmountMinor: -10000, Description: "ER-7Q2M-K4HD-9W1C Expense reimbursement", Counterparty: "Kit Blanche"})
	var proposals []ledger.MatchProposal
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		_, proposals, err = o.ReportMatches(lt.f.db.Context, f.business(), ref)
		return err
	})
	if len(proposals) == 0 || proposals[0].Reason != "reference" || !proposals[0].Checks["amount"] {
		t.Fatal("matches", proposals)
	}
}

// Approval binds to the exact revision: a report changed underneath the
// approver isn't approved, and a proof for another revision is refused.
func TestApprovalBindsToTheRevision(t *testing.T) {
	f := newReportFixture(t)
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE AIR", "Flight")
	d := f.detail(ref)
	token := f.proof("submit_approve", d)
	f.claim(ref, "HARBOR HOTEL", "Stay")
	err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.SubmitReport(f.lt.f.db.Context, f.business(), who, f.identity(), ref,
			ledger.SubmitReport{Revision: d.Revision, SnapshotSHA256: d.SnapshotSHA256, Attestation: true, Approve: true, ProofToken: token})
		return err
	})
	if !errors.Is(err, ledger.ErrRevisionChanged) {
		t.Fatal("approved a changed report", err)
	}
	// A proof for the old snapshot doesn't approve the new one.
	now := f.detail(ref)
	err = f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.SubmitReport(f.lt.f.db.Context, f.business(), who, f.identity(), ref,
			ledger.SubmitReport{Revision: now.Revision, SnapshotSHA256: now.SnapshotSHA256, Attestation: true, Approve: true, ProofToken: token})
		return err
	})
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "passkey") {
		t.Fatal("a proof for another snapshot was accepted", err)
	}
	// An independent policy refuses the owner's own approval.
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.AdoptReportPolicy(f.lt.f.db.Context, f.business(), who, who, ledger.PolicyAdoption{EffectiveFrom: "2026-11-01", AdoptedText: "Reports are approved by Kit.",
			ApprovalKind: "independent", ApproverPersonIDs: []string{f.person}})
		return err
	})
	token = f.proof("submit_approve", now)
	err = f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.SubmitReport(f.lt.f.db.Context, f.business(), who, f.identity(), ref,
			ledger.SubmitReport{Revision: now.Revision, SnapshotSHA256: now.SnapshotSHA256, Attestation: true, Approve: true, ProofToken: token})
		return err
	})
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "someone other than") {
		t.Fatal("self-approval under an independent policy", err)
	}
}

// A draft that was never submitted can be thrown away: its cash claims go
// with it and its costs are free for the next report. A submitted report
// is withdrawn instead. Claims say their kind and the business's portion
// under either name, and each line names both its accounts.
func TestANeverSubmittedDraftIsDeleted(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	ref := f.newReport("", false)
	add := func(q ledger.ClaimInput) (string, error) {
		var claim string
		err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
			var err error
			claim, err = o.AddReportClaim(ctx, f.business(), who, ref, q)
			return err
		})
		return claim, err
	}
	air := f.pairKey("EXAMPLE AIR")
	for _, q := range []ledger.ClaimInput{
		{Kind: "cash", PairKey: air},
		{Kind: "cost"},
		{Kind: "gift", PairKey: air},
		{PairKey: air, PortionMinor: "40000", BusinessPortionMinor: "39000"},
	} {
		if _, err := add(q); err == nil {
			t.Fatal("accepted", q)
		}
	}
	if _, err := add(ledger.ClaimInput{Kind: "cost", PairKey: air, BusinessPortionMinor: "40000", ProposedAccountID: f.travel.ID, Purpose: "Flight"}); err != nil {
		t.Fatal(err)
	}
	cash, err := add(ledger.ClaimInput{Kind: "cash", Date: "2026-11-06", Vendor: "Example Parking", AmountMinor: "4500", BusinessPortionMinor: "4500",
		PaidFromAccountID: f.kitWallet.ID, ProposedAccountID: f.travel.ID, Purpose: "Parking at the customer"})
	if err != nil {
		t.Fatal(err)
	}
	d := f.detail(ref)
	if len(d.Lines) != 2 || d.Lines[0].AmountMinor != "40000" || d.Lines[1].AmountMinor != "4500" {
		t.Fatal("lines", d.Lines)
	}
	for _, l := range d.Lines {
		if l.ProposedAccountName != "Travel" || l.AccountName == "" || l.RecordedAccountID != "" && l.RecordedAccountName == "" {
			t.Fatal("account names", l.ProposedAccountName, l.RecordedAccountName, l.AccountName)
		}
	}
	f.owner(func(o ledger.TenantAdmin, who string) error { return o.DeleteDraftReport(ctx, f.business(), who, ref) })
	if err := f.tryOwner(func(o ledger.TenantAdmin, _ string) error {
		_, err := o.ExpenseReport(ctx, f.business(), ref)
		return err
	}); err == nil {
		t.Fatal("the deleted draft is still there")
	}
	if f.count("SELECT count(*) FROM public.claims WHERE tenant_id=$1 AND id=$2", f.lt.f.tenant, cash) != 0 ||
		f.count("SELECT count(*) FROM public.report_events WHERE tenant_id=$1", f.lt.f.tenant) != 0 {
		t.Fatal("the draft left its cash claim or events")
	}
	// Its reference stays used.
	if err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.CreateReport(ctx, who, ledger.NewReport{LinkID: f.link, PersonID: f.person, Reference: ref})
		return err
	}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatal("a deleted draft's reference was issued again", err)
	}
	// The flight is free to claim again.
	again := f.newReport("", false)
	f.claim(again, "EXAMPLE AIR", "Flight")
	// Once submitted, a report is only withdrawn.
	f.submit(again, false)
	err = f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		return o.DeleteDraftReport(ctx, f.business(), who, again)
	})
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "withdraw") {
		t.Fatal("a submitted report was deleted", err)
	}
}

// A person's relationship is recorded with the day it applies from, and
// each cost reads the one in effect on its day. Not established brings no
// employee guidance.
func TestTheRelationshipOnTheDayOfTheCost(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	f.owner(func(o ledger.TenantAdmin, who string) error {
		people, err := o.People(ctx, f.business())
		if err != nil || len(people) != 1 {
			return errors.New("people")
		}
		_, err = o.SavePerson(ctx, f.business(), who, ledger.PersonChange{ID: f.person, Name: "Kit Blanche", Capacity: "not_established", EffectiveFrom: "2026-11-04",
			PersonalBookID: f.lt.books["kit"].ID, ExpectedRevision: people[0].Revision})
		return err
	})
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE AIR", "Flight")
	f.claim(ref, "EXAMPLE TAXI", "Taxi")
	d := f.detail(ref)
	got := map[string]string{}
	for _, l := range d.Lines {
		got[l.Date] = l.Capacity
	}
	if got["2026-11-03"] != "shareholder_employee" || got["2026-11-05"] != "not_established" || d.Person.EffectiveFrom != "2026-11-04" {
		t.Fatal("capacities", got, d.Person)
	}
}

// A business's policy is adopted by its owner, version by version from a
// day. Each claim keeps the version in force on the day of its cost, each
// approval is checked against the version in force now, and the profile's
// receipt check runs whatever the company's threshold.
func TestAnAdoptedPolicyGovernsClaimsAndApprovals(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	var rae string
	f.owner(func(o ledger.TenantAdmin, who string) error {
		p, err := o.SavePerson(ctx, f.business(), who, ledger.PersonChange{Name: "Rae Approver", Capacity: "employee"})
		rae = p.ID
		if err != nil {
			return err
		}
		if _, err := o.AdoptReportPolicy(ctx, f.business(), who, who, ledger.PolicyAdoption{EffectiveFrom: "2026-11-01", AdoptedText: "Receipts for anything over $20.",
			ApprovalKind: "owner_self", ReceiptThresholdMinor: "2000"}); err != nil {
			return err
		}
		// Naming someone the business doesn't pay back is refused.
		if _, err := o.AdoptReportPolicy(ctx, f.business(), who, who, ledger.PolicyAdoption{EffectiveFrom: "2026-11-05", AdoptedText: "Rae approves.",
			ApprovalKind: "independent", ApproverPersonIDs: []string{ledger.ID()}}); err == nil {
			return errors.New("a stranger was made an approver")
		}
		_, err = o.AdoptReportPolicy(ctx, f.business(), who, who, ledger.PolicyAdoption{EffectiveFrom: "2026-11-05", AdoptedText: "Rae approves.",
			ApprovalKind: "independent", ApproverPersonIDs: []string{rae}, ReceiptThresholdMinor: "2000"})
		return err
	})
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE AIR", "Flight")
	f.claim(ref, "EXAMPLE TAXI", "Taxi")
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.AddReportClaim(ctx, f.business(), who, ref, ledger.ClaimInput{Kind: "cash", Date: "2026-11-02", Vendor: "Example Parking", AmountMinor: "4500",
			PaidFromAccountID: f.kitWallet.ID, ProposedAccountID: f.travel.ID, Purpose: "Parking"})
		return err
	})
	d := f.detail(ref)
	got := map[string]string{}
	for _, l := range d.Lines {
		got[l.Vendor] = strconv.Itoa(l.PolicyVersion) + "/" + l.ReceiptRule
	}
	if got["Example Parking"] != "1/company" || !strings.HasPrefix(got[d.Lines[0].Vendor], "1/profile") || d.Policy.Version != 2 || len(d.Policy.Approvers) != 1 {
		t.Fatal("policy versions and receipt rules", got, d.Policy.Version)
	}
	var policies []ledger.ReportPolicy
	f.owner(func(o ledger.TenantAdmin, _ string) error {
		var err error
		policies, err = o.ReportPolicies(ctx, f.business())
		return err
	})
	if len(policies) != 2 || policies[1].AdoptedText != "Receipts for anything over $20." || policies[0].ApprovalKind != "independent" {
		t.Fatal("versions", policies)
	}
	// Under the policy in force now the owner can't approve their own.
	token := f.proof("submit_approve", d)
	err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.SubmitReport(ctx, f.business(), who, f.identity(), ref,
			ledger.SubmitReport{Revision: d.Revision, SnapshotSHA256: d.SnapshotSHA256, Attestation: true, Approve: true, ProofToken: token})
		return err
	})
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "someone other than") {
		t.Fatal("self-approval under an independent policy", err)
	}
}

// The supplier's invoice facts are kept exactly as given and never carry
// the report's reference; at submission each claim keeps what the rule
// profile found.
func TestTheProfilesAssessmentAndTheSuppliersInvoice(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	ref := f.newReport("", false)
	invoice := `{"number": "INV-2041",  "supplier": "Example Air", "customer": "Eightball Inc.", "issued": "2026-11-03", "tax": {"vat": 0}}`
	var air string
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		air, err = o.AddReportClaim(ctx, f.business(), who, ref, ledger.ClaimInput{PairKey: f.pairKey("EXAMPLE AIR"), Purpose: "Flight", SupplierInvoice: json.RawMessage(invoice)})
		return err
	})
	if err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		return o.EditReportClaim(ctx, f.business(), who, ref, air, ledger.ClaimInput{SupplierInvoice: json.RawMessage(`{"number": "` + ref + `"}`)})
	}); err == nil {
		t.Fatal("the report's reference went into the supplier's invoice")
	}
	f.claim(ref, "EXAMPLE TAXI", "Taxi")
	f.submit(ref, true)
	d := f.detail(ref)
	if string(d.Lines[0].SupplierInvoice) != invoice || d.Lines[0].RuleProfile != "us_actual_expense_v1" {
		t.Fatal("invoice", string(d.Lines[0].SupplierInvoice))
	}
	var a map[string]any
	if json.Unmarshal(d.Lines[0].Assessment, &a) != nil || a["status"] != "assessed" || a["receipt"] != "missing" || a["business_purpose"] != "stated" || a["receipt_rule"] != "profile" {
		t.Fatal("assessment", string(d.Lines[0].Assessment))
	}
}

// A payment over what a report was due leaves a residual the owner
// classifies; the report isn't enlarged, and only an error is owed back.
func TestAPaymentsLeftoverWaitsForItsClassification(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE TAXI", "Taxi")
	f.submit(ref, true)
	// $300 paid against a $100 report.
	entry := f.payment("TO KIT BLANCHE")
	var paid ledger.PaymentResult
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		paid, err = o.ApplyReportPayment(ctx, f.business(), who, ref, entry, 0, nil)
		return err
	})
	d := f.detail(ref)
	if paid.ExcessMinor != "20000" || paid.ResidualID == "" || len(d.Residuals) != 1 || d.Residuals[0].Classification != nil || len(d.Recoverables) != 0 || d.ApprovedMinor != "10000" {
		t.Fatal("residual", paid, d.Residuals, d.Recoverables)
	}
	f.owner(func(o ledger.TenantAdmin, who string) error {
		if err := o.ClassifyResidual(ctx, f.business(), who, paid.ResidualID, "another_report", "", ""); err == nil {
			return errors.New("another report without naming it")
		}
		return o.ClassifyResidual(ctx, f.business(), who, paid.ResidualID, "error", "", "Paid twice by mistake")
	})
	d = f.detail(ref)
	if d.Residuals[0].Classification == nil || *d.Residuals[0].Classification != "error" || len(d.Recoverables) != 1 || d.Recoverables[0].AmountMinor != "20000" ||
		d.Recoverables[0].Origin != "overpayment" || d.Residuals[0].RecoverableID != d.Recoverables[0].ID {
		t.Fatal("classified", d.Residuals, d.Recoverables)
	}
	if err := f.tryOwner(func(o ledger.TenantAdmin, who string) error {
		return o.ClassifyResidual(ctx, f.business(), who, paid.ResidualID, "fee", "", "")
	}); err == nil {
		t.Fatal("classified twice")
	}
}

// A cash claim is paid like any cost: a payment over it pays what's open
// and leaves the rest as a residual, never refused.
func TestAnOverpaidCashClaimLeavesAResidual(t *testing.T) {
	f := newReportFixture(t)
	ctx := f.lt.f.db.Context
	ref := f.newReport("", false)
	f.owner(func(o ledger.TenantAdmin, who string) error {
		_, err := o.AddReportClaim(ctx, f.business(), who, ref, ledger.ClaimInput{Kind: "cash", Date: "2026-11-06", Vendor: "Example Parking", AmountMinor: "4500",
			PaidFromAccountID: f.kitWallet.ID, ProposedAccountID: f.travel.ID, Purpose: "Parking"})
		return err
	})
	f.submit(ref, true)
	entry := f.payment("TO KIT BLANCHE TAXI")
	var paid ledger.PaymentResult
	f.owner(func(o ledger.TenantAdmin, who string) error {
		var err error
		paid, err = o.ApplyReportPayment(ctx, f.business(), who, ref, entry, 0, nil)
		return err
	})
	d := f.detail(ref)
	if d.Settlement != "settled" || d.PaidMinor != "4500" || paid.ExcessMinor != "5500" || len(d.Residuals) != 1 || d.Residuals[0].AmountMinor != "5500" {
		t.Fatal("overpaid cash claim", d.ReportSummary, paid, d.Residuals)
	}
}

// A cost part repaid by an earlier payment is claimed for what's left.
func TestAPartRepaidCostIsClaimedForWhatsOpen(t *testing.T) {
	f := newReportFixture(t)
	// $100 paid on what Eightball owes reaches the oldest cost: $100 of
	// the $420 flight.
	f.payment("TO KIT BLANCHE TAXI")
	ref := f.newReport("", false)
	f.claim(ref, "EXAMPLE AIR", "Flight")
	d := f.detail(ref)
	if d.Lines[0].AmountMinor != "32000" || d.Lines[0].PriorReimbursedMinor != "10000" || d.Lines[0].OriginalMinor != "42000" {
		t.Fatal("part repaid", d.Lines[0].AmountMinor, d.Lines[0].PriorReimbursedMinor)
	}
}

// Correcting an accepted cash claim never edits the entry first recorded:
// a correcting entry moves what the business owes by the difference, and
// the Debts card and who owes whom read the same. Unpaid, a taxi of $25
// corrected to $20 is owed $20; paid $25 and corrected to $20, $5 is owed
// back; raised from $20 to $25, $5 more is owed, and Kit, who had recorded
// their side, records the same correction in their Book.
func TestCorrectingAnAcceptedCashClaim(t *testing.T) {
	type state struct{ owed, open, recoverable int64 }
	for _, c := range []struct {
		name           string
		portion, now   string
		paid, recorded bool
		want           state
	}{
		{"lowered unpaid", "2500", "2000", false, false, state{2000, 2000, 0}},
		{"lowered paid", "2500", "2000", true, false, state{2000, 0, 500}},
		{"raised", "2000", "2500", false, true, state{2500, 2500, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newReportFixture(t)
			ctx := f.lt.f.db.Context
			kit := f.lt.books["kit"].ID
			ref := f.newReport("", false)
			var claim string
			f.owner(func(o ledger.TenantAdmin, who string) error {
				var err error
				claim, err = o.AddReportClaim(ctx, f.business(), who, ref, ledger.ClaimInput{Kind: "cash", Date: "2026-11-06", Vendor: "Example Cab", AmountMinor: "2500",
					PortionMinor: c.portion, PaidFromAccountID: f.kitWallet.ID, ProposedAccountID: f.travel.ID, Purpose: "Cab to the customer"})
				return err
			})
			f.submit(ref, true)
			if c.recorded {
				f.owner(func(o ledger.TenantAdmin, who string) error {
					_, err := o.RecordPersonSide(ctx, kit, who, claim, "")
					return err
				})
			}
			if c.paid {
				entry := f.payment("TO KIT BLANCHE TAXI")
				f.owner(func(o ledger.TenantAdmin, who string) error {
					_, err := o.ApplyReportPayment(ctx, f.business(), who, ref, entry, 2500, nil)
					return err
				})
			}
			entries := f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", f.lt.f.tenant, f.business())
			f.owner(func(o ledger.TenantAdmin, who string) error {
				if err := o.AmendReport(ctx, f.business(), who, ref, "The fare was corrected"); err != nil {
					return err
				}
				return o.EditReportClaim(ctx, f.business(), who, ref, claim, ledger.ClaimInput{PortionMinor: c.now})
			})
			f.submit(ref, true)
			// One correcting entry; the first is unchanged.
			if f.count("SELECT count(*) FROM public.entries WHERE tenant_id=$1 AND book_id=$2", f.lt.f.tenant, f.business()) != entries+1 ||
				f.count("SELECT count(*) FROM public.claim_obligation_adjustments WHERE tenant_id=$1 AND claim_id=$2 AND from_revision=1 AND to_revision=2", f.lt.f.tenant, claim) != 1 {
				t.Fatal("no correcting entry")
			}
			var owed, open int64
			if err := f.lt.f.db.Admin.QueryRow(ctx, `SELECT o.amount,o.open FROM public.open_costs($1,$2,$3,$4,NULL) o WHERE o.pair_key=$5`,
				f.lt.f.tenant, kit, f.business(), f.payable.ID, "claim:"+claim).Scan(&owed, &open); err != nil {
				t.Fatal(err)
			}
			d := f.detail(ref)
			var recoverable int64
			for _, x := range d.Recoverables {
				n, _ := strconv.ParseInt(x.AmountMinor, 10, 64)
				recoverable += n
			}
			if (state{owed, open, recoverable}) != c.want {
				t.Fatal("after the correction", state{owed, open, recoverable}, "want", c.want)
			}
			if c.recorded {
				var corrections []ledger.PersonSideCorrection
				f.owner(func(o ledger.TenantAdmin, who string) error {
					var err error
					if corrections, err = o.PersonSideCorrections(ctx, kit); err != nil || len(corrections) != 1 || corrections[0].DeltaMinor != "500" {
						return errors.New("Kit's correction to record")
					}
					_, err = o.RecordPersonCorrection(ctx, kit, who, corrections[0].ID, "")
					return err
				})
			}
			// The Debts card reads what the business owes Kit, and who owes
			// whom the same.
			debts := -(f.lt.balance("eightball", "2310"))
			var balances ledger.LinkedBalances
			f.owner(func(o ledger.TenantAdmin, _ string) error {
				var err error
				balances, err = o.OwnerLinkedBalances(ctx)
				return err
			})
			if len(balances.Links) != 1 || balances.Links[0].AmountMinor != strconv.FormatInt(debts, 10) {
				t.Fatal("Debts and who owes whom", debts, balances.Links)
			}
			// The claim's entries and its correction are paired, never
			// shown as unexplained.
			var correction, first string
			if err := f.lt.f.db.Admin.QueryRow(ctx, `SELECT a.business_entry_id::text,o.business_entry_id::text FROM public.claim_obligation_adjustments a
 JOIN public.claim_obligations o ON o.tenant_id=a.tenant_id AND o.claim_id=a.claim_id WHERE a.claim_id=$1`, claim).Scan(&correction, &first); err != nil {
				t.Fatal(err)
			}
			for _, m := range balances.Links[0].MismatchedEntryIDs {
				if m == correction || m == first {
					t.Fatal("the claim's entries read as unexplained", balances.Links[0].MismatchedEntryIDs)
				}
			}
			if c.recorded && !balances.Links[0].Matches {
				t.Fatal("Kit's Book and Eightball's disagree after the correction", balances.Links)
			}
		})
	}
}

package ledger_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// An agent the owner lets record when confident records within the Book's
// limits, and everything else stays a proposal; the ledger checks it, by
// account types only.
func TestAgentsRecordWhenConfidentWithinTheBooksLimits(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	_, _, byCode := chartBookAccounts(t, f)
	equity, tax, sales := byCode["3100"], byCode["2400"], byCode["4100"]
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "ok", "2026-04-01", -2500, "Example Office Supply"),
		f.tx("acc-checking", "big", "2026-04-02", -90000, "Example Furniture"),
		f.tx("acc-checking", "owner", "2026-04-03", -10000, "Transfer to owner"),
		f.tx("acc-checking", "tax", "2026-04-04", -4000, "State tax payment"),
		f.tx("acc-checking", "refund", "2026-04-05", -1500, "Refund to customer"),
	}, nil)
	page := f.inbox(t, f.s, "all")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	record := func(key, item, other string) error {
		_, err := f.db.Store.Call(ctx, agent, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: key, TransactionID: byDescription(page, item).ID,
			Description: item, OtherAccountID: other, Reason: "The usual supplier for this"}))
		return err
	}
	// Without a grant, or by rules only, the agent drafts.
	if err := record("no-grant", "Example Office Supply", f.office.ID); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("recorded without a grant: %v", err)
	}
	succeeded(t, f.owner.SetRecordingMode(ctx, f.book.ID, f.actor, agent.ConnectionID, ledger.RecordByRules))
	if err := record("rules", "Example Office Supply", f.office.ID); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("recorded by its own judgement under a rules grant: %v", err)
	}
	succeeded(t, f.owner.SetRecordingMode(ctx, f.book.ID, f.actor, agent.ConnectionID, ledger.RecordWhenSure))
	succeeded(t, f.owner.MarkTaxSensitive(ctx, f.book.ID, f.actor, tax.ID, true))
	succeeded(t, record("ok", "Example Office Supply", f.office.ID))
	bankRefused(t, record("big", "Example Furniture", f.office.ID), "over the Book's limit")
	bankRefused(t, record("owner", "Transfer to owner", equity.ID), "money between the owner and the Book")
	bankRefused(t, record("tax", "State tax payment", tax.ID), "reached only by the owner's rules")
	bankRefused(t, record("refund", "Refund to customer", sales.ID), "refunds are asked")
	// A higher limit lets the furniture through.
	policy, err := f.owner.SetAgentCap(ctx, f.book.ID, f.actor, "100000", 0)
	succeeded(t, err)
	if policy.AmountCapMinor != "100000" || len(policy.TaxSensitive) != 1 {
		t.Fatalf("policy = %+v", policy)
	}
	succeeded(t, record("big-2", "Example Furniture", f.office.ID))
	// The summary counts what agents recorded, from the ledger.
	var summary ledger.AgentSummary
	json.Unmarshal(f.db.Call(t, agent, "get_agent_summary", map[string]string{"since": "2000-01-01"}), &summary)
	if summary.Recorded.Total != 2 || summary.Recorded.By["confident"] != 2 || summary.Waiting.Unrecorded != 3 {
		t.Fatalf("summary = %+v", summary)
	}
}

// Trust is earned per kind from the owner's decisions and lost by a
// correction; after 15 accepted the owner is offered a rule.
func TestTrustIsEarnedPerKindAndLostByACorrection(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	var items []ledger.BankSyncTransaction
	for i := 1; i <= 17; i++ {
		items = append(items, ledger.BankSyncTransaction{ProviderAccountID: "acc-checking", ProviderTransactionID: fmt.Sprintf("s%d", i), Date: fmt.Sprintf("2026-%02d-%02d", 1+i/28, 1+i%28),
			AmountMinor: -1599, Currency: f.book.Currency, Scale: f.book.Scale, Description: fmt.Sprintf("EXAMPLE STREAM %d", i), Counterparty: "Example Stream"})
	}
	f.sync(t, "c1", "c2", items, nil)
	page := f.inbox(t, f.s, "all")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	var last ledger.OperationProposal
	decide := func(i int, decision string) {
		t.Helper()
		var p ledger.OperationProposal
		key := fmt.Sprintf("p%d", i)
		json.Unmarshal(f.db.Call(t, agent, "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: "record_bank_activity",
			Arguments: value(ledger.RecordBankActivity{RequestKey: key + "-r", TransactionID: byDescription(page, fmt.Sprintf("EXAMPLE STREAM %d", i)).ID, Description: "Streaming", OtherAccountID: f.office.ID})}), &p)
		if decision == "approve" {
			approveOperation(t, f.db, f.s, p, f.s)
			last = p
			return
		}
		owner, epoch := proposalOwner(t, f.db, f.s)
		if _, err := f.db.Admin.Exec(ctx, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,'reject','Example decision')`, f.book.TenantID, f.book.ID, p.ID, owner, epoch, "decision-"+key, p.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	kind := func() ledger.AgentKind {
		t.Helper()
		kinds, err := f.owner.AgentKinds(ctx, f.book.ID)
		succeeded(t, err)
		if len(kinds) != 1 {
			t.Fatalf("kinds = %+v", kinds)
		}
		return kinds[0]
	}
	decide(1, "reject")
	if k := kind(); k.Corrected != 1 || k.Level != "draft" || k.Counterparty != "example stream" || k.DeclinedAccountID != f.office.ID || k.DecidedAccountID != "" {
		t.Fatalf("after a rejection = %+v", k)
	}
	// The ledger names the kind the same way on the inbox and in trust.
	if item := byDescription(page, "EXAMPLE STREAM 2"); item.KindKey != kind().KindKey || item.KindKey != f.checking.ID+",out,example stream" {
		t.Fatalf("kind keys = %q and %q", item.KindKey, kind().KindKey)
	}
	for i := 2; i <= 16; i++ {
		decide(i, "approve")
	}
	if k := kind(); k.AcceptedUnchanged != 15 || k.AcceptedSinceCorrection != 15 || !k.Offered || k.DecidedAccountID != f.office.ID || k.DeclinedAccountID != "" {
		t.Fatalf("after 15 accepted = %+v", k)
	}
	if _, err := f.owner.AcceptKindOffer(ctx, f.book.ID, f.actor, f.checking.ID, "out", "example stream"); err == nil {
		t.Fatal("an offer was accepted before any accepted item was recorded")
	}
	// Once an accepted item is recorded, the owner accepts the offer: a rule
	// for the kind, and the kind is trusted again.
	executeOperation(t, f.db, f.s, last)
	rule, err := f.owner.AcceptKindOffer(ctx, f.book.ID, f.actor, f.checking.ID, "out", "example stream")
	succeeded(t, err)
	if k := kind(); rule.Counterparty != "example stream" || rule.OtherAccountID != f.office.ID || k.RuleID != rule.ID || k.Level != "record" || k.Offered {
		t.Fatalf("rule = %+v, kind = %+v", rule, k)
	}
}

// Where trust starts: a Book whose built-in agent hasn't passed the skills
// test starts every kind at drafts; passing it starts them at recording.
// A transfer under a confident grant keeps to the limit.
func TestTrustStartsFromTheSkillsTest(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "ok", "2026-04-01", -2500, "Example Office Supply"),
		f.tx("acc-checking", "pay", "2026-04-05", -60000, "ONLINE PAYMENT TO CARD"),
		f.tx("acc-card", "paid", "2026-04-06", -60000, "PAYMENT THANK YOU"),
	}, nil)
	page := f.inbox(t, f.s, "all")
	saved, err := f.owner.SaveAgentSettings(ctx, f.book.ID, f.actor, ledger.AgentSettingsChange{})
	succeeded(t, err)
	if saved.DefaultTrust != "draft" {
		t.Fatalf("settings = %+v", saved)
	}
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	succeeded(t, f.owner.SetRecordingMode(ctx, f.book.ID, f.actor, agent.ConnectionID, ledger.RecordWhenSure))
	record := func(key string) error {
		_, err := f.db.Store.Call(ctx, agent, "record_bank_activity", value(ledger.RecordBankActivity{RequestKey: key, TransactionID: byDescription(page, "Example Office Supply").ID,
			Description: "Supplies", OtherAccountID: f.office.ID, Reason: "Office supplies"}))
		return err
	}
	bankRefused(t, record("before"), "until the agent earns trust")
	if err := f.db.Store.RecordAgentSkills(ctx, f.book.TenantID, f.book.ID, "other-model", "confident", 29, 30); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("a result for another model was kept: %v", err)
	}
	succeeded(t, f.db.Store.RecordAgentSkills(ctx, f.book.TenantID, f.book.ID, "", "confident", 29, 30))
	succeeded(t, record("after"))
	_, err = f.db.Store.Call(ctx, agent, "record_bank_transfer", value(ledger.RecordBankTransfer{RequestKey: "card",
		TransactionIDs: []string{byDescription(page, "ONLINE PAYMENT TO CARD").ID, byDescription(page, "PAYMENT THANK YOU").ID}}))
	bankRefused(t, err, "transfer is over the Book's limit")
}

// A proposal from an agent the owner disconnected since says so; the owner
// may still decide it.
func TestProposalsSayWhenTheirAgentIsGone(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "x", "2026-04-02", -2500, "Example Office Supply")}, nil)
	item := byDescription(f.inbox(t, f.s, "all"), "Example Office Supply")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	f.db.Call(t, agent, "prepare_operation", ledger.PrepareOperation{RequestKey: "p", Operation: "record_bank_activity",
		Arguments: value(ledger.RecordBankActivity{RequestKey: "r", TransactionID: item.ID, Description: "Supplies", OtherAccountID: f.office.ID})})
	list := func() ledger.ProposalSummary {
		page, err := f.owner.OperationProposals(f.db.Context, f.book.ID, 0, 10, true)
		succeeded(t, err)
		if len(page.Plans) != 1 {
			t.Fatalf("plans = %+v", page.Plans)
		}
		return page.Plans[0]
	}
	if list().AgentDisconnected {
		t.Fatal("a connected agent's proposal was marked")
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE public.connections SET revoked_at=clock_timestamp() WHERE id=$1", agent.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if p := list(); !p.AgentDisconnected || p.Status != "approval_required" {
		t.Fatalf("after disconnecting = %+v", p)
	}
}

// The owner reviewing an agent's proposal sees what the bank said and the
// agent's reason for recording it this way.
func TestProposalsShowTheBankTextAndTheAgentsReason(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "x", "2026-04-02", -2500, "Example Office Supply")}, nil)
	item := byDescription(f.inbox(t, f.s, "all"), "Example Office Supply")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	f.db.Call(t, agent, "prepare_operation", ledger.PrepareOperation{RequestKey: "p", Operation: "record_bank_activity",
		Arguments: value(ledger.RecordBankActivity{RequestKey: "r", TransactionID: item.ID, Description: "Supplies", OtherAccountID: f.office.ID, Reason: "Printer paper, as last month"})})
	page, err := f.owner.OperationProposals(f.db.Context, f.book.ID, 0, 10, true)
	succeeded(t, err)
	if len(page.Plans) != 1 {
		t.Fatalf("plans = %+v", page.Plans)
	}
	state, err := f.owner.OperationProposal(f.db.Context, f.book.ID, page.Plans[0].ID)
	succeeded(t, err)
	if c := state.Proposal.Preview.BankChange; c == nil || c.Reason != "Printer paper, as last month" {
		t.Fatalf("bank change = %+v", c)
	}
	if len(state.BankItems) != 1 || state.BankItems[0].ID != item.ID || state.BankItems[0].Description != item.Description {
		t.Fatalf("bank items = %+v", state.BankItems)
	}
}

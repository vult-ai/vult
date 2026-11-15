package ledger_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestProposalHistoryPagesIncludeScopedBankKindsAndRejections(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "first", "2026-04-01", -500, "Example Shop 1"),
		f.tx("acc-checking", "second", "2026-04-02", -700, "Example Shop 2"),
	}, nil)
	page := f.inbox(t, f.s, "all")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	var prepared []ledger.OperationProposal
	for _, description := range []string{"Example Shop 1", "Example Shop 2"} {
		item := byDescription(page, description)
		var p ledger.OperationProposal
		if err := json.Unmarshal(f.db.Call(t, agent, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare-" + item.ID, Operation: "record_bank_activity",
			Arguments: value(ledger.RecordBankActivity{RequestKey: "record-" + item.ID, TransactionID: item.ID, Description: description, OtherAccountID: f.office.ID})}), &p); err != nil {
			t.Fatal(err)
		}
		prepared = append(prepared, p)
	}
	owner, epoch := proposalOwner(t, f.db, f.s)
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
VALUES($1,$2,$3,1,$4,$5,'reject-synthetic',$6,'reject','Use the supplies account instead.')`, f.book.TenantID, f.book.ID, prepared[1].ID, owner, epoch, prepared[1].SHA256); err != nil {
		t.Fatal(err)
	}
	read := func(scope ledger.Scope, before int64) ([]ledger.AgentProposal, int64) {
		t.Helper()
		var out struct {
			Proposals  []ledger.AgentProposal `json:"proposals"`
			NextBefore int64                  `json:"next_before"`
		}
		if err := json.Unmarshal(f.db.Call(t, scope, "list_operation_proposals", map[string]any{"limit": 1, "before": before}), &out); err != nil {
			t.Fatal(err)
		}
		return out.Proposals, out.NextBefore
	}
	first, next := read(agent, 0)
	if len(first) != 1 || first[0].ProposalID != prepared[1].ID || next != first[0].Sequence || first[0].Status != "rejected" || first[0].Reason != "Use the supplies account instead." {
		t.Fatalf("first page: %+v, next %d", first, next)
	}
	kind := first[0].ActivityKind
	if kind == nil || kind.AccountID != f.checking.ID || kind.AccountClass != "asset" || kind.Direction != "out" || kind.Counterparty != "example shop" || len(first[0].TransactionIDs) != 1 {
		t.Fatalf("bank kind: %+v", first[0])
	}
	second, next := read(agent, next)
	if len(second) != 1 || second[0].ProposalID != prepared[0].ID || second[0].Status != "approval_required" {
		t.Fatalf("second page: %+v", second)
	}
	if empty, next := read(agent, next); len(empty) != 0 || next != 0 {
		t.Fatalf("end: %+v %d", empty, next)
	}
	other := connectionScope(t, f.db, f.book.ID, "propose")
	if rows, next := read(other, 0); len(rows) != 0 || next != 0 {
		t.Fatalf("another actor read proposal history: %+v", rows)
	}
	if _, err := f.db.Store.Call(ctx, agent, "list_operation_proposals", []byte(`{"before":-1}`)); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("negative cursor: %v", err)
	}
	// A kind outside the default summary is still available by its exact
	// ledger key; stateless clients must not lose its owner's decision.
	if _, err := f.db.Admin.Exec(ctx, `INSERT INTO public.agent_kinds(tenant_id,book_id,account_id,direction,counterparty_key,level,accepted_unchanged)
SELECT $1,$2,$3,'out','synthetic other '||n,'draft',100 FROM generate_series(1,500) n`, f.book.TenantID, f.book.ID, f.checking.ID); err != nil {
		t.Fatal(err)
	}
	var kinds struct {
		Kinds []ledger.AgentKind `json:"kinds"`
	}
	if err := json.Unmarshal(f.db.Call(t, agent, "list_agent_kinds", struct{}{}), &kinds); err != nil {
		t.Fatal(err)
	}
	if len(kinds.Kinds) != 500 {
		t.Fatal("summary bound", len(kinds.Kinds))
	}
	for _, k := range kinds.Kinds {
		if k.Counterparty == "example shop" {
			t.Fatal("fixture did not place the decision beyond the summary")
		}
	}
	key := f.checking.ID + ",out,example shop"
	if err := json.Unmarshal(f.db.Call(t, agent, "list_agent_kinds", map[string]any{"kind_keys": []string{key}}), &kinds); err != nil {
		t.Fatal(err)
	}
	if len(kinds.Kinds) != 1 || kinds.Kinds[0].KindKey != key || kinds.Kinds[0].DeclinedAccountID != f.office.ID {
		t.Fatalf("exact decision: %+v", kinds.Kinds)
	}
}

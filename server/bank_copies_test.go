package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// Two agents propose the same bank item. Once the owner approves one, the
// other copy says its bank item is already recorded, shows no balances,
// refuses approval with that reason, and its rejection doesn't count
// against the agent.
func TestCopiesOfARecordedBankItemCanOnlyBeRejected(t *testing.T) {
	d, fixture := bankPaths(t, "mcp")
	f := d.(mcpDriver).f
	s := d.(mcpDriver).s
	ctx, book := f.db.Context, f.book.ID
	var first, second ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		if first, err = o.CreateConnection(ctx, book, "Example agent A", "propose"); err != nil {
			return err
		}
		second, err = o.CreateConnection(ctx, book, "Example agent B", "propose")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var item ledger.BankTransaction
	for _, x := range d.Inbox(t, "unrecorded").Items {
		if x.AmountMinor[0] == '-' && item.ID == "" {
			item = x
		}
	}
	if item.ID == "" {
		t.Fatal("no money-out item to propose")
	}
	propose := func(c ledger.Connection, key string) ledger.OperationProposal {
		t.Helper()
		scope := ledger.Scope{TenantID: f.tenant, BookID: book, ConnectionID: c.ID, ActorID: c.ActorID, Access: "propose"}
		args, _ := json.Marshal(ledger.RecordBankActivity{RequestKey: key + "-r", TransactionID: item.ID, Description: "Supplies", OtherAccountID: fixture.Expense})
		var p ledger.OperationProposal
		if json.Unmarshal(f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: "record_bank_activity", Arguments: args}), &p) != nil || p.ID == "" {
			t.Fatal("prepare", key)
		}
		return p
	}
	older, newer := propose(first, "a"), propose(second, "b")
	read := func(p ledger.OperationProposal) ledger.ProposalState {
		t.Helper()
		var state ledger.ProposalState
		if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			state, err = o.OperationProposal(ctx, book, p.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return state
	}
	st := read(older)
	if st.DuplicateOf == 0 || st.BankRecorded {
		t.Fatalf("before = %+v", st)
	}
	wantItem := ledger.ProposalBankItem{ID: item.ID, Date: item.Date, AmountMinor: item.AmountMinor, Description: item.Description, Counterparty: item.Counterparty, Source: item.Source}
	if len(st.BankItems) != 1 || st.BankItems[0] != wantItem {
		t.Fatal("the proposal must show the exact bank item", st.BankItems)
	}
	var batch []ledger.ProposalState
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		batch, err = o.OperationProposalStates(ctx, book, []string{older.ID, newer.ID})
		return err
	}); err != nil || len(batch) != 2 {
		t.Fatal("bank proposal details were not read together", err)
	}
	for _, state := range batch {
		if len(state.BankItems) != 1 || state.BankItems[0] != wantItem {
			t.Fatal("batch details changed the bank item", state.BankItems)
		}
	}
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(book, newer, "approve-newer", 0)); w.Code != http.StatusOK {
		t.Fatal("approve newer", w.Code, w.Body.String())
	}
	st = read(older)
	if !st.BankRecorded || st.DuplicateOf != 0 || len(st.Balances) != 0 || st.Status != "approval_required" {
		t.Fatalf("after = %+v", st)
	}
	w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(book, older, "approve-older", 0))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "already recorded") {
		t.Fatal("approve older", w.Code, w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/agent-owner/reject", proposalDecisionBody(book, older, "reject-older", 0)); w.Code != http.StatusOK {
		t.Fatal("reject older", w.Code, w.Body.String())
	}
	var corrected int
	if err := f.db.Admin.QueryRow(ctx, "SELECT coalesce(sum(corrected),0) FROM public.agent_kinds WHERE tenant_id=$1 AND book_id=$2", f.tenant, book).Scan(&corrected); err != nil || corrected != 0 {
		t.Fatal("rejecting a copy counted as a correction", corrected, err)
	}
}

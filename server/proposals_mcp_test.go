package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type proposalAdmission struct {
	denied  string
	checked []string
}

func (p *proposalAdmission) Check(_ context.Context, _ ledger.Scope, method string) error {
	p.checked = append(p.checked, method)
	if method == p.denied {
		return ledger.ErrDenied
	}
	return nil
}

func proposalJSON(t *testing.T, input any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func remoteProposalFixture(t *testing.T, operation string) (*documentServerFixture, string, ledger.Scope, ledger.PrepareOperation) {
	t.Helper()
	f := documentServer(t)
	token := f.token(t, "write")
	scope, err := f.sessions.ResolveAgent(f.db.Context, token)
	if err != nil {
		t.Fatal(err)
	}
	store := remoteStore(f.db.Runtime, f.sessions, token, nil, nil)
	accounts := make([]ledger.Account, 2)
	for i, class := range []string{"asset", "equity"} {
		result, err := store.Call(f.db.Context, scope, "create_account", proposalJSON(t, ledger.CreateAccount{RequestKey: class, Class: class, Name: "Example " + class}))
		if err != nil || json.Unmarshal(result, &accounts[i]) != nil {
			t.Fatal("create scoped account", err)
		}
	}
	journal := ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{
		{AccountID: accounts[0].ID, Side: "debit", AmountMinor: "9007199254740993"},
		{AccountID: accounts[1].ID, Side: "credit", AmountMinor: "9007199254740993"},
	}}
	q := ledger.PrepareOperation{RequestKey: "prepare", Operation: operation, Arguments: proposalJSON(t, journal)}
	if operation == "reverse_journal" {
		result, err := store.Call(f.db.Context, scope, "post_journal", q.Arguments)
		var entry ledger.Entry
		if err != nil || json.Unmarshal(result, &entry) != nil || entry.ID == "" {
			t.Fatal("seed reversal target", err)
		}
		q.Arguments = proposalJSON(t, ledger.ReverseJournal{RequestKey: "accepted-reversal", EntryID: entry.ID, AccountingDate: "2026-01-03", Reason: "Example correction"})
	}
	return f, token, scope, q
}

func approveRemoteProposal(t *testing.T, f *documentServerFixture, scope ledger.Scope, p ledger.OperationProposal, admit ledger.DecisionAdmission) (ledger.OperationDecisionResult, error) {
	t.Helper()
	return f.sessions.DecideOperation(f.db.Context, f.session, f.tenant, ledger.DecideOperation{
		BookID: scope.BookID, ProposalID: p.ID, SHA256: p.SHA256, RequestKey: "decision", Decision: "approve",
		Reason: "Example accepted effects",
	}, admit)
}

const proposalEffects = `SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.requests)+(SELECT count(*) FROM public.operation_proposals)+(SELECT count(*) FROM public.operation_consumptions)+(SELECT count(*) FROM public.operation_decisions)`

func TestRemoteProposalCannotBypassUnderlyingAdmission(t *testing.T) {
	for _, operation := range []string{"post_journal", "reverse_journal"} {
		for _, phase := range []string{"prepare_operation", "owner_approval"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				f, token, scope, q := remoteProposalFixture(t, operation)
				var p ledger.OperationProposal
				if phase == "owner_approval" {
					result, err := remoteCall(f.db.Context, f.db.Runtime, f.sessions, token, scope, nil, nil, "prepare_operation", proposalJSON(t, q))
					if err != nil || json.Unmarshal(result, &p) != nil {
						t.Fatal("prepare operation", err)
					}
				}
				var before, after int
				if err := f.db.Admin.QueryRow(f.db.Context, proposalEffects).Scan(&before); err != nil {
					t.Fatal(err)
				}
				checked := []string{}
				var err error
				if phase == "owner_approval" {
					_, err = approveRemoteProposal(t, f, scope, p, func(_ context.Context, _ pgx.Tx, _ ledger.Scope, method string, _ json.RawMessage) error {
						checked = append(checked, method)
						if method == operation {
							return ledger.ErrDenied
						}
						return nil
					})
				} else {
					policy := &proposalAdmission{denied: operation}
					_, err = remoteCall(f.db.Context, f.db.Runtime, f.sessions, token, scope, policy, nil, phase, proposalJSON(t, q))
					checked = policy.checked
				}
				if !errors.Is(err, ledger.ErrDenied) && !errors.Is(err, access.ErrDenied) {
					t.Fatal("proposal bypassed underlying operation policy", err)
				}
				if err := f.db.Admin.QueryRow(f.db.Context, proposalEffects).Scan(&after); err != nil || before != after {
					t.Fatal("denied proposal retained work or posted", before, after, err)
				}
				found := false
				for _, method := range checked {
					found = found || method == operation
				}
				if !found {
					t.Fatal("underlying admission was never checked")
				}
			})
		}
	}
}

type approvalPostingPolicy struct {
	proposalAdmission
	denyTransaction bool
	allowed         bool
	scope           ledger.Scope
	calls           int
}

func (p *approvalPostingPolicy) CheckTransaction(ctx context.Context, tx pgx.Tx, scope ledger.Scope, operation string, _ json.RawMessage) error {
	p.calls++
	p.scope = scope
	if err := tx.QueryRow(ctx, "SELECT public.ledger_allowed($1,$2,true)", scope.TenantID, scope.BookID).Scan(&p.allowed); err != nil {
		return err
	}
	if p.denyTransaction || !p.allowed || operation != "post_journal" {
		return ledger.ErrDenied
	}
	return nil
}

func TestBrowserProposalAdmitsTheUnderlyingPosting(t *testing.T) {
	for _, mode := range []string{"check-denies", "transaction-denies", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			f, _, scope, p := browserProposal(t)
			policy := &approvalPostingPolicy{denyTransaction: mode == "transaction-denies"}
			if mode == "check-denies" {
				policy.denied = "post_journal"
			}
			s := newBrowserServer(t, f, policy)
			post := func() int {
				return ownerAPICall(s, f.session, http.MethodPost, "/api/v1/agent-owner/approve",
					proposalDecisionBody(scope.BookID, p, "review", 0), true).Code
			}
			var before, after int
			if err := f.db.Admin.QueryRow(f.db.Context, proposalEffects).Scan(&before); err != nil {
				t.Fatal(err)
			}
			status := post()
			if len(policy.checked) != 1 || policy.checked[0] != "post_journal" {
				t.Fatal("approval did not check the underlying operation")
			}
			if mode == "check-denies" && policy.calls != 0 ||
				mode != "check-denies" && (policy.calls != 1 || !policy.allowed || policy.scope != scope) {
				t.Fatal("transaction policy lacked the authorized preparer scope")
			}
			if err := f.db.Admin.QueryRow(f.db.Context, proposalEffects).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if mode != "allowed" {
				if status != http.StatusForbidden || after != before {
					t.Fatal("a refused admission left a decision or posting", status)
				}
				return
			}
			if status != http.StatusOK || after != before+4 {
				t.Fatal("admitted approval did not post exactly once", status)
			}
			policy.denied, policy.denyTransaction = "post_journal", true
			if post() != http.StatusOK || len(policy.checked) != 1 || policy.calls != 1 {
				t.Fatal("a decision retry re-ran financial admission")
			}
		})
	}
}

func TestRemoteProposalOwnerPostsAndRecoversAnExactReversal(t *testing.T) {
	f, token, scope, q := remoteProposalFixture(t, "reverse_journal")
	client := f.client(t, token)
	var p ledger.OperationProposal
	documentTool(t, f.db.Context, client, "prepare_operation", q, &p)
	out, err := approveRemoteProposal(t, f, scope, p, nil)
	if err != nil || out.Status != "committed" || out.Outcome == nil {
		t.Fatal("owner approval did not post the reversal", err)
	}
	var posted ledger.Entry
	if json.Unmarshal(out.Outcome.Result, &posted) != nil || posted.ReversalOf == "" ||
		posted.ReversalOf != p.Preview.ReversalOf || posted.ActorID != scope.ActorID ||
		posted.Lines[0].AmountMinor != "9007199254740993" {
		t.Fatal("the reviewed reversal lost its original, actor or exact amount")
	}
	before, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = f.db.Owner.WriteArchive(f.db.Context, f.vault, &archive, scope.BookID); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.RestoreArchive(target.Context, f.vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("owner-approved operation did not restore", err)
	}
	after, err := target.Owner.Snapshot(target.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"entries", "lines", "requests", "operation_proposals", "operation_decisions", "operation_consumptions"} {
		if !bytes.Equal(before.Tables[table], after.Tables[table]) {
			t.Fatal("recovery changed the reviewed decision or effect", table)
		}
	}
	for _, mode := range []string{"missing-consumption", "superseded-approval"} {
		t.Run(mode, func(t *testing.T) {
			corrupt := before
			corrupt.Tables = maps.Clone(before.Tables)
			if mode == "missing-consumption" {
				corrupt.Tables["operation_consumptions"] = json.RawMessage(`[]`)
			} else {
				var decisions []map[string]json.RawMessage
				if json.Unmarshal(corrupt.Tables["operation_decisions"], &decisions) != nil || len(decisions) != 1 {
					t.Fatal("invalid approved fixture")
				}
				reject := maps.Clone(decisions[0])
				reject["decision"], reject["revision"] = json.RawMessage(`"reject"`), json.RawMessage(`2`)
				reject["request_key"] = json.RawMessage(`"later-decision"`)
				corrupt.Tables["operation_decisions"] = proposalJSON(t, append(decisions, reject))
			}
			target := testutil.New(t)
			if err := target.Owner.Restore(target.Context, corrupt); !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("inconsistent approval history was recovered", err)
			}
			if books, err := target.Owner.Books(target.Context); err != nil || len(books) != 0 {
				t.Fatal("failed approval recovery left a partial Book", err)
			}
		})
	}
}

func TestRemoteProposalOwnerApprovalPostsForAnAgentThatMovedOn(t *testing.T) {
	f, token, scope, q := remoteProposalFixture(t, "post_journal")
	client := f.client(t, token)
	var p ledger.OperationProposal
	documentTool(t, f.db.Context, client, "prepare_operation", q, &p)
	if p.Preview.Classes[0].DebitMinor != "9007199254740993" || p.OperationKey != "accepted-post" || p.Approval.Message == "" || p.ExpiresAt != nil {
		t.Fatal("transport lost exact effects, operation identity or the queue message")
	}
	for _, name := range []string{"approve_operation", "reject_operation", "bind_owner"} {
		result, err := client.CallTool(f.db.Context, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !result.IsError {
			t.Fatal("remote agent gained owner authority", name)
		}
	}
	// The agent's access credential is not part of the owner's approval.
	out, err := approveRemoteProposal(t, f, scope, p, nil)
	if err != nil || out.Status != "committed" || out.Outcome == nil {
		t.Fatal("owner approval did not post", err)
	}
	var posted ledger.Entry
	if json.Unmarshal(out.Outcome.Result, &posted) != nil || posted.ActorID != scope.ActorID {
		t.Fatal("posted entry lost its preparer")
	}
	var state ledger.ProposalState
	documentTool(t, f.db.Context, client, "get_operation_proposal", ledger.ProposalQuery{RequestKey: q.RequestKey}, &state)
	if state.Status != "committed" || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, out.Outcome.Result) {
		t.Fatal("posted outcome not discoverable by the agent")
	}
	var retried ledger.Entry
	documentTool(t, f.db.Context, client, "post_journal", q.Arguments, &retried)
	if retried.ID != posted.ID {
		t.Fatal("agent retry did not return the posted entry")
	}
	var entries, consumed int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.operation_consumptions)").Scan(&entries, &consumed); err != nil || entries != 1 || consumed != 1 {
		t.Fatal("approval posted more than once", entries, consumed, err)
	}
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		return owner.Revoke(f.db.Context, scope.ConnectionID)
	}); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(f.db.Context, &mcp.CallToolParams{Name: "get_operation_proposal", Arguments: ledger.ProposalQuery{ProposalID: p.ID}})
	if err == nil && !result.IsError {
		t.Fatal("revoked remote session retained Book access")
	}
}

func TestRemoteProposalStaleApprovalPostsNothing(t *testing.T) {
	f, token, scope, q := remoteProposalFixture(t, "post_journal")
	result, err := remoteCall(f.db.Context, f.db.Runtime, f.sessions, token, scope, nil, nil, "prepare_operation", proposalJSON(t, q))
	var p ledger.OperationProposal
	if err != nil || json.Unmarshal(result, &p) != nil {
		t.Fatal("prepare operation", err)
	}
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		return owner.ClosePeriod(f.db.Context, scope.BookID, "2026-01-01", "2026-01-31", "Example close with review items acknowledged")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = approveRemoteProposal(t, f, scope, p, nil); !errors.Is(err, ledger.ErrProposalStale) {
		t.Fatal("stale dependency became a generic or successful outcome", err)
	}
	var rows int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_consumptions)+(SELECT count(*) FROM public.operation_decisions)").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("stale proposal posted", rows, err)
	}
}

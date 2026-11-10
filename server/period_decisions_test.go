package server

import (
	"encoding/json"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// The owner-only period decisions an agent prepares are recorded when the
// owner approves them through their own signed-in session.
func TestOwnerApprovesAnAgentsReopeningAndCloseWithOpenItems(t *testing.T) {
	f, token, scope, _ := remoteProposalFixture(t, "post_journal")
	store := remoteStore(f.db.Runtime, f.sessions, token, nil, nil)
	call := func(method string, input any) json.RawMessage {
		t.Helper()
		result, err := store.Call(f.db.Context, scope, method, proposalJSON(t, input))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return result
	}
	prepare := func(key, operation string, args any) ledger.OperationProposal {
		t.Helper()
		var p ledger.OperationProposal
		result, err := remoteCall(f.db.Context, f.db.Runtime, f.sessions, token, scope, nil, nil, "prepare_operation", proposalJSON(t, ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: proposalJSON(t, args)}))
		if err != nil || json.Unmarshal(result, &p) != nil {
			t.Fatal("prepare", operation, err)
		}
		return p
	}
	approve := func(p ledger.OperationProposal, key string) {
		t.Helper()
		if _, err := f.sessions.DecideOperation(f.db.Context, f.session, f.tenant, ledger.DecideOperation{
			BookID: scope.BookID, ProposalID: p.ID, SHA256: p.SHA256, RequestKey: key, Decision: "approve", Reason: "Agreed",
		}, nil); err != nil {
			t.Fatal("approve", p.Operation, err)
		}
	}
	periods := func() []ledger.AccountingPeriod {
		var out struct {
			Periods []ledger.AccountingPeriod `json:"periods"`
		}
		if json.Unmarshal(call("list_closed_periods", struct{}{}), &out) != nil {
			t.Fatal("invalid periods")
		}
		return out.Periods
	}
	// A clean month: the agent closes it directly.
	var feb ledger.AccountingPeriod
	if json.Unmarshal(call("close_period", ledger.ClosePeriod{RequestKey: "feb", From: "2026-02-01", Through: "2026-02-28"}), &feb) != nil || feb.Status != "closed" {
		t.Fatal("a write agent closes a clean period")
	}
	// The agent can't reopen it; it prepares the reopening for the owner.
	if _, err := store.Call(f.db.Context, scope, "reopen_period", proposalJSON(t, ledger.ReopenPeriod{RequestKey: "direct", PeriodID: feb.ID, ExpectedRevision: 1, Reason: "x"})); err == nil {
		t.Fatal("an agent reopened a period")
	}
	approve(prepare("prepare-reopen", "reopen_period", ledger.ReopenPeriod{RequestKey: "reopen", PeriodID: feb.ID, ExpectedRevision: 1, Reason: "A February invoice arrived late"}), "approve-reopen")
	if p := periods(); len(p) != 1 || p[0].Status != "reopened" || p[0].Events[1].Reason != "A February invoice arrived late" {
		t.Fatal("the owner's approval reopened the period", p)
	}
	// January holds an entry with no payee or evidence: the agent can only
	// prepare a close, with a reason, and the owner's approval records it.
	var software ledger.Account
	if json.Unmarshal(call("create_account", ledger.CreateAccount{RequestKey: "software", Class: "expense", Code: "6200", Name: "Example software"}), &software) != nil {
		t.Fatal("invalid account")
	}
	call("post_journal", ledger.PostJournal{RequestKey: "jan", EconomicDate: "2026-01-10", AccountingDate: "2026-01-10", Description: "Example January charge", Lines: []ledger.PostLine{
		{AccountID: software.ID, Side: "debit", AmountMinor: "100"}, {AccountID: listedAccount(t, call, "asset"), Side: "credit", AmountMinor: "100"}}})
	if _, err := store.Call(f.db.Context, scope, "close_period", proposalJSON(t, ledger.ClosePeriod{RequestKey: "jan-direct", From: "2026-01-01", Through: "2026-01-31", Reason: "x"})); err == nil {
		t.Fatal("an agent closed a period with open items")
	}
	close := prepare("prepare-close", "close_period", ledger.ClosePeriod{RequestKey: "close-jan", From: "2026-01-01", Through: "2026-01-31", Reason: "Evidence arrives with the year-end pack"})
	if close.Preview.PeriodChange == nil || close.Preview.PeriodChange.OpenItems["entries_without_payee"] < 1 {
		t.Fatal("the close proposal shows what is still open", close.Preview)
	}
	approve(close, "approve-close")
	p := periods()
	if len(p) != 2 || p[0].From != "2026-01-01" || p[0].Status != "closed" || p[0].Events[0].Reason == "" || p[0].Events[0].OpenItems["entries_without_payee"] < 1 {
		t.Fatal("the owner's approval closed January with its open items", p)
	}
}

func listedAccount(t *testing.T, call func(string, any) json.RawMessage, class string) string {
	t.Helper()
	var out struct {
		Accounts []ledger.Account `json:"accounts"`
	}
	if json.Unmarshal(call("list_accounts", struct{}{}), &out) != nil {
		t.Fatal("invalid accounts")
	}
	for _, a := range out.Accounts {
		if a.Class == class {
			return a.ID
		}
	}
	t.Fatal("no account of class", class)
	return ""
}

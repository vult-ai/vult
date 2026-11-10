package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestProposalCatalogPreservesLegacyAndOwnerBoundaries(t *testing.T) {
	read := []string{"get_book", "list_accounts", "get_journal", "list_journals", "get_balance", "get_trial_balance", "get_request_outcome", "get_operation_proposal", "search_book", "list_closed_periods", "get_exchange_rate", "list_starter_charts", "list_payees", "get_entry_payee", "list_recurring_charges", "get_cash_flow", "list_statements", "get_statement_reconciliation", "suggest_statement_matches", "get_period_review"}
	write := []string{"create_account", "revise_account", "retire_account", "reactivate_account", "apply_starter_chart", "create_payee", "revise_payee", "set_entry_payee", "import_statement", "mark_statement_duplicate", "match_statement", "revoke_statement_match", "record_statement_adjustment", "review_statement", "close_period", "post_journal", "reverse_journal", "prepare_operation", "execute_proposal", "set_book_logo"}
	docRead := []string{"get_document", "list_documents", "get_entry_documents", "get_source_usage", "list_entries_missing_evidence"}
	docWrite := []string{"add_document", "correct_source_citation", "attach_evidence", "detach_evidence"}
	for _, documents := range []bool{false, true} {
		for _, permission := range []string{"read", "write"} {
			name := permission
			if documents {
				name += "-documents"
			}
			t.Run(name, func(t *testing.T) {
				expected := append([]string{}, read...)
				if permission == "write" {
					expected = append(expected, write...)
				}
				if documents {
					expected = append(expected, docRead...)
					if permission == "write" {
						expected = append(expected, docWrite...)
					}
				}
				names := map[string]bool{}
				for _, name := range expected {
					names[name] = true
				}
				client := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{}`), nil
				}, permission, Options{Documents: documents})
				catalog, err := client.ListTools(context.Background(), nil)
				if err != nil || len(catalog.Tools) != len(names) {
					t.Fatal("wrong exact tool catalog", err)
				}
				for _, tool := range catalog.Tools {
					if !names[tool.Name] {
						t.Fatal("unexpected or duplicate tool", tool.Name)
					}
					delete(names, tool.Name)
					if tool.Name == "get_operation_proposal" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
						t.Fatal("proposal inspection declared a write")
					}
					if (tool.Name == "prepare_operation" || tool.Name == "execute_proposal") && (permission != "write" || tool.Annotations == nil || tool.Annotations.ReadOnlyHint) {
						t.Fatal("proposal mutation escaped write scope")
					}
				}
				if len(names) != 0 {
					t.Fatal("legacy or proposal tool disappeared", names)
				}
			})
		}
	}
}

type invalidProposalInput struct {
	name  string
	input any
}

func invalidProposalInputs() []invalidProposalInput {
	args := ledger.ReverseJournal{RequestKey: "reverse", EntryID: ledger.ID(), AccountingDate: "2026-01-03", Reason: "Example correction"}
	return []invalidProposalInput{
		{"prepare_operation", map[string]any{"request_key": "prepare", "operation": "close_period", "arguments": args}},
		{"prepare_operation", map[string]any{"request_key": "prepare", "operation": "post_journal", "arguments": args}},
		{"prepare_operation", map[string]any{"request_key": "prepare", "operation": "reverse_journal", "arguments": args, "book_id": ledger.ID()}},
		{"get_operation_proposal", map[string]any{}},
		{"get_operation_proposal", map[string]any{"proposal_id": ledger.ID(), "request_key": "prepare"}},
		{"get_operation_proposal", map[string]any{"proposal_id": ledger.ID(), "tenant_id": ledger.ID()}},
		{"execute_proposal", map[string]any{"proposal_id": ledger.ID(), "sha256": "bad"}},
		{"execute_proposal", map[string]any{"proposal_id": ledger.ID(), "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "decision": "approve"}},
	}
}

func proposalInputSchemas(t *testing.T, client *mcp.ClientSession) map[string]*jsonschema.Resolved {
	t.Helper()
	catalog, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range catalog.Tools {
		if tool.Name != "prepare_operation" && tool.Name != "get_operation_proposal" && tool.Name != "execute_proposal" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var definition jsonschema.Schema
		if err = json.Unmarshal(data, &definition); err != nil {
			t.Fatal(err)
		}
		resolved, err := definition.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	return schemas
}

func schemaInput(t *testing.T, input any) any {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestProposalSchemasRejectScopeAndOperationSubstitution(t *testing.T) {
	called := 0
	client := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		called++
		return json.RawMessage(`{}`), nil
	}, "write")
	schemas := proposalInputSchemas(t, client)
	for _, tc := range invalidProposalInputs() {
		if err := schemas[tc.name].Validate(schemaInput(t, tc.input)); err == nil {
			t.Fatal("advertised schema accepts invalid input", tc.name)
		}
	}
	for _, tc := range []invalidProposalInput{
		{"prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "reverse_journal", Arguments: valueJSON(ledger.ReverseJournal{RequestKey: "reverse", EntryID: ledger.ID(), AccountingDate: "2026-01-02", Reason: "Example correction"})}},
		{"get_operation_proposal", ledger.ProposalQuery{ProposalID: ledger.ID()}},
		{"get_operation_proposal", ledger.ProposalQuery{RequestKey: "prepare"}},
		{"execute_proposal", ledger.ExecuteProposal{ProposalID: ledger.ID(), SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	} {
		if err := schemas[tc.name].Validate(schemaInput(t, tc.input)); err != nil {
			t.Fatal("advertised schema rejects supported input", tc.name, err)
		}
	}
	for _, name := range []string{"approve_operation", "reject_operation", "owner_approve", "bind_owner"} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !result.IsError {
			t.Fatal("agent gained owner administration", name)
		}
	}
	if called != 0 {
		t.Fatal("owner operation reached the agent handler")
	}
}

func TestProposalSchemaSharesNativeMeasurementContract(t *testing.T) {
	called := false
	client := connect(t, context.Background(), func(_ context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		var q ledger.PrepareOperation
		var journal ledger.PostJournal
		if method != "prepare_operation" || json.Unmarshal(input, &q) != nil || json.Unmarshal(q.Arguments, &journal) != nil || journal.Lines[0].Native == nil || journal.Lines[0].Native.AmountMinor != "1" || journal.Lines[0].Valuation.Rate.Rate != "0.001" {
			t.Fatal("nested measurements were lost")
		}
		called = true
		return json.RawMessage(`{}`), nil
	}, "write", Options{Documents: true})
	scale := 0
	native := ledger.CurrencyAmount{Currency: "JPY", Scale: &scale, AmountMinor: "1"}
	q := ledger.PostJournal{RequestKey: "accepted", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example native movement", Lines: []ledger.PostLine{
		{AccountID: ledger.ID(), Side: "debit", AmountMinor: "0", Native: &ledger.NativeMovement{Currency: "JPY", Scale: &scale, AmountMinor: "1"}, Valuation: &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &native, Rate: &ledger.AppliedRate{Base: "JPY", Quote: "USD", Rate: "0.001", RequestedDate: "2026-01-02", ObservationDate: "2026-01-02", Provenance: "Hypothetical quote"}}},
		{AccountID: ledger.ID(), Side: "credit", AmountMinor: "0"},
	}, Sources: []ledger.SourceInput{{DocumentID: ledger.ID()}}}
	prepared := ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: valueJSON(q)}
	if err := proposalInputSchemas(t, client)["prepare_operation"].Validate(schemaInput(t, prepared)); err != nil {
		t.Fatal("advertised schema rejects exact native/source fields", err)
	}
	invoke(t, context.Background(), client, "prepare_operation", prepared)
	if !called {
		t.Fatal("supported nested schema never reached its handler")
	}
}

func TestMCPProposalBackendRejectsInvalidInputsWithoutClientValidation(t *testing.T) {
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	client := connect(t, db.Context, func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		return db.Store.Call(ctx, scope, method, input)
	}, "write")
	for _, tc := range invalidProposalInputs() {
		result, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: tc.name, Arguments: tc.input})
		if err == nil && !result.IsError {
			t.Fatal("backend trusted unvalidated tool input", tc.name)
		}
	}
	var rows int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_proposals)+(SELECT count(*) FROM public.operation_consumptions)").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("invalid tool inputs changed financial or proposal records", err)
	}
}

func TestMCPProposalLostReplyAndReadOnlyOutcomeRecovery(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	a, b := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
	var lost atomic.Bool
	client := connect(t, db.Context, func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		result, err := db.Store.Call(ctx, scope, method, input)
		if err == nil && method == "execute_proposal" && lost.Swap(false) {
			return nil, ledger.ErrUncertain
		}
		return result, err
	}, "write")
	q := ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{
		{AccountID: a.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: b.ID, Side: "credit", AmountMinor: "9007199254740993"},
	}}
	var p ledger.OperationProposal
	if json.Unmarshal(invoke(t, db.Context, client, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: valueJSON(q)}), &p) != nil || p.Preview.Classes[0].DebitMinor != "9007199254740993" {
		t.Fatal("exact preview missing")
	}
	execution := ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256}
	denied, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: "execute_proposal", Arguments: execution})
	if err != nil || !denied.IsError {
		t.Fatal("unapproved MCP operation committed", err)
	}
	operator := access.Operator{DB: db.Admin}
	id := access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner", AuthenticatedAt: time.Now().UTC()}
	if _, err = operator.BindOwner(db.Context, scope.TenantID, id); err != nil {
		t.Fatal(err)
	}
	var owner string
	var epoch int64
	if err = db.Admin.QueryRow(db.Context, "SELECT id::text,epoch FROM access.identities WHERE issuer=$1 AND subject=$2", id.Issuer, id.Subject).Scan(&owner, &epoch); err != nil {
		t.Fatal(err)
	}
	_, err = db.Admin.Exec(db.Context, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason,executor_actor_id,executor_connection_id)
        VALUES($1,$2,$3,1,$4,$5,'decision',$6,'approve','Example accepted effects',$7,$8)`, scope.TenantID, scope.BookID, p.ID, owner, epoch, p.SHA256, scope.ActorID, scope.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	lost.Store(true)
	uncertain, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: "execute_proposal", Arguments: execution})
	if err != nil || !uncertain.IsError {
		t.Fatal("lost response presented as success", err)
	}
	var state ledger.ProposalState
	if json.Unmarshal(invoke(t, db.Context, client, "get_operation_proposal", ledger.ProposalQuery{RequestKey: "prepare"}), &state) != nil || state.Status != "committed" || state.Outcome == nil {
		t.Fatal("committed proposal outcome lost")
	}
	retry := invoke(t, db.Context, client, "execute_proposal", execution)
	if !bytes.Equal(retry, state.Outcome.Result) {
		t.Fatal("execution retry changed its journal")
	}
	c, err := db.Owner.CreateConnection(db.Context, book.ID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	readScope := ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
	reader := connect(t, db.Context, func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		return db.Store.Call(ctx, readScope, method, input)
	}, "read")
	if json.Unmarshal(invoke(t, db.Context, reader, "get_operation_proposal", ledger.ProposalQuery{ProposalID: p.ID}), &state) != nil || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, retry) {
		t.Fatal("new Book reader cannot inspect the retained outcome")
	}
	for _, name := range []string{"prepare_operation", "execute_proposal", "approve_operation"} {
		result, err := reader.CallTool(db.Context, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !result.IsError {
			t.Fatal("reader gained proposal authority", name)
		}
	}
}

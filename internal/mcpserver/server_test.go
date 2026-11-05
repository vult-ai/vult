package mcpserver

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func connect(t *testing.T, ctx context.Context, call Call, access string, options ...Options) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, e := New(call, access, options...).Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "ledger-client", Version: "1.0.0"}, nil)
	cs, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestClientFileExportIsDeclaredWithoutGrantingLedgerWrites(t *testing.T) {
	ctx := context.Background()
	client := connect(t, ctx, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}, "read", Options{Documents: true, LocalFiles: true})
	catalog, err := client.ListTools(ctx, nil)
	if err != nil || len(catalog.Tools) != 12 {
		t.Fatal("wrong document read catalog", err)
	}
	found := false
	for _, tool := range catalog.Tools {
		if ledger.Writing(tool.Name) {
			t.Fatal("client file export enabled a ledger write")
		}
		if tool.Name == "get_document" {
			found = true
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint {
				t.Fatal("client filesystem effect hidden")
			}
		}
	}
	if !found {
		t.Fatal("missing document export")
	}
}
func invoke(t *testing.T, ctx context.Context, c *mcp.ClientSession, method string, input any) []byte {
	t.Helper()
	r, e := c.CallTool(ctx, &mcp.CallToolParams{Name: method, Arguments: input})
	if e != nil || r.IsError || len(r.Content) != 1 {
		t.Fatal("MCP call failed", method, e, r)
	}
	text, ok := r.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("missing JSON text content")
	}
	if r.StructuredContent == nil {
		t.Fatal("missing structured result")
	}
	return []byte(text.Text)
}
func TestMCPBookJourneysAndLostReplyRecovery(t *testing.T) {
	db := testutil.New(t)
	for _, name := range []string{"Personal", "Business"} {
		t.Run(name, func(t *testing.T) {
			book, scope := db.Book(t, name)
			var lose atomic.Bool
			c := connect(t, db.Context, func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
				out, e := db.Store.Call(ctx, scope, method, input)
				if e == nil && method == "post_journal" && lose.Swap(false) {
					return nil, ledger.ErrUncertain
				}
				return out, e
			}, "write")
			catalog, e := c.ListTools(db.Context, nil)
			if e != nil || len(catalog.Tools) != 11 {
				t.Fatal("unexpected tool catalog", e)
			}
			var got ledger.Book
			if json.Unmarshal(invoke(t, db.Context, c, "get_book", struct{}{}), &got) != nil || got.ID != book.ID {
				t.Fatal("MCP selected the wrong Book")
			}
			var debit, credit ledger.Account
			json.Unmarshal(invoke(t, db.Context, c, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Code: "1000", Name: "Example cash"}), &debit)
			json.Unmarshal(invoke(t, db.Context, c, "create_account", ledger.CreateAccount{RequestKey: "capital", Class: "equity", Code: "3000", Name: "Example capital"}), &credit)
			q := ledger.PostJournal{RequestKey: "funding", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example opening", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "9007199254740993"}}}
			lose.Store(true)
			failed, e := c.CallTool(db.Context, &mcp.CallToolParams{Name: "post_journal", Arguments: q})
			if e != nil || !failed.IsError {
				t.Fatal("lost response was reported as success", e)
			}
			var outcome ledger.Outcome
			json.Unmarshal(invoke(t, db.Context, c, "get_request_outcome", ledger.OutcomeQuery{RequestKey: q.RequestKey}), &outcome)
			if outcome.Status != "recorded" {
				t.Fatal("committed outcome was lost")
			}
			retry := invoke(t, db.Context, c, "post_journal", q)
			if string(retry) != string(outcome.Result) {
				t.Fatal("retry changed its retained result")
			}
			var trial ledger.Trial
			json.Unmarshal(invoke(t, db.Context, c, "get_trial_balance", ledger.TrialQuery{}), &trial)
			if len(trial.Balances) != 2 || trial.Balances[0].DebitPositiveMinor != "9007199254740993" || trial.Balances[1].DebitPositiveMinor != "-9007199254740993" {
				t.Fatal("MCP lost exact integer balances")
			}
			bad := map[string]any{"request_key": "scope", "tenant_id": ledger.ID()}
			if r, e := c.CallTool(db.Context, &mcp.CallToolParams{Name: "post_journal", Arguments: bad}); e == nil && !r.IsError {
				t.Fatal("MCP accepted scope injection")
			}
			if e := db.Owner.Revoke(db.Context, scope.ConnectionID); e != nil {
				t.Fatal(e)
			}
			if r, e := c.CallTool(db.Context, &mcp.CallToolParams{Name: "get_book", Arguments: map[string]any{}}); e == nil && !r.IsError {
				t.Fatal("revoked MCP session returned financial data")
			}
			v, e := db.Owner.Verify(db.Context, book.ID)
			if e != nil || v.Entries != 1 || v.Lines != 2 {
				t.Fatal("MCP journey posted twice", e)
			}
		})
	}
}
func TestReadOnlyMCPHasNoWriteOrOwnerTools(t *testing.T) {
	c := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}, "read")
	catalog, e := c.ListTools(context.Background(), nil)
	if e != nil || len(catalog.Tools) != 7 {
		t.Fatal("unexpected read catalog", e)
	}
	for _, tool := range catalog.Tools {
		if ledger.Writing(tool.Name) || !ledger.Supported(tool.Name) || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("unsafe read catalog", tool.Name)
		}
	}
	for _, method := range []string{"post_journal", "connection_create", "backup", "period_close", "uninstall", "_uninstall"} {
		if r, e := c.CallTool(context.Background(), &mcp.CallToolParams{Name: method, Arguments: map[string]any{}}); e == nil && !r.IsError {
			t.Fatal("unlisted tool was callable", method)
		}
	}
}

func TestSoftwareRemovalIsNeverAnAgentTool(t *testing.T) {
	called := false
	c := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{}`), nil
	}, "write")
	for _, method := range []string{"uninstall", "_uninstall", "_stop", "delete_data", "purge"} {
		if r, e := c.CallTool(context.Background(), &mcp.CallToolParams{Name: method, Arguments: map[string]any{}}); e == nil && !r.IsError {
			t.Fatal("software removal was callable", method)
		}
	}
	if called {
		t.Fatal("unlisted owner operation reached the service")
	}
}

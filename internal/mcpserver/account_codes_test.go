package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestMCPOptionalCodesDoNotInventOrSilentlyClearLabels(t *testing.T) {
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	client := connect(t, db.Context, func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		return db.Store.Call(ctx, scope, method, input)
	}, "write")
	var account ledger.Account
	create := map[string]any{"request_key": "uncoded", "class": "asset", "name": "Example account"}
	if json.Unmarshal(invoke(t, db.Context, client, "create_account", create), &account) != nil || account.Code != "" || !ledger.ValidID(account.ID) {
		t.Fatal("MCP required or invented an account code")
	}
	revise := map[string]any{"request_key": "assign", "account_id": account.ID, "expected_revision": 1, "code": "1000", "name": account.Name}
	if json.Unmarshal(invoke(t, db.Context, client, "revise_account", revise), &account) != nil || account.Code != "1000" || account.Revision != 2 {
		t.Fatal("code could not be explicitly assigned")
	}
	revise["request_key"], revise["expected_revision"] = "omitted", 2
	delete(revise, "code")
	result, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: "revise_account", Arguments: revise})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatal("MCP silently cleared an omitted revision code")
	}
	revise["request_key"], revise["code"] = "clear", ""
	if json.Unmarshal(invoke(t, db.Context, client, "revise_account", revise), &account) != nil || account.Code != "" || account.Revision != 3 {
		t.Fatal("code could not be explicitly removed")
	}
	for _, remote := range []bool{false, true} {
		server := New
		if remote {
			server = NewRemote
		}
		st, ct := mcp.NewInMemoryTransports()
		ss, err := server(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}, "write").Connect(db.Context, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ss.Close()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "example", Version: "1"}, nil).Connect(db.Context, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		catalog, err := cs.ListTools(db.Context, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range catalog.Tools {
			if tool.Name != "create_account" && tool.Name != "revise_account" {
				continue
			}
			data, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Required []string `json:"required"`
			}
			if json.Unmarshal(data, &schema) != nil {
				t.Fatal("invalid catalog schema")
			}
			required := false
			for _, field := range schema.Required {
				required = required || field == "code"
			}
			if required != (tool.Name == "revise_account") {
				t.Fatal("catalog code semantics differ across transports")
			}
		}
	}
}

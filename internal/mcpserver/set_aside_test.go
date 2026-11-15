package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/setaside"
)

func TestASetAsideToolIsNeitherListedNorCallable(t *testing.T) {
	call := func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	options := Options{Documents: true, LocalFiles: true, OwnerUploads: true, ProposeReadings: true}
	all, e := connect(t, context.Background(), call, "write", options).ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	options.SetAside = true
	c := connect(t, context.Background(), call, "write", options)
	kept, e := c.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	listed := map[string]bool{}
	for _, tool := range kept.Tools {
		if setaside.Tools[tool.Name] {
			t.Fatal("a set-aside tool is listed:", tool.Name)
		}
		listed[tool.Name] = true
	}
	for _, tool := range all.Tools {
		if !setaside.Tools[tool.Name] && !listed[tool.Name] {
			t.Fatal("a tool this release keeps is missing:", tool.Name)
		}
	}
	if r, e := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_bank_activity", Arguments: map[string]any{}}); e == nil && !r.IsError {
		t.Fatal("a set-aside tool was callable")
	}
	if !listed["post_journal"] || !listed["get_book"] {
		t.Fatal("the ledger's own tools must stay")
	}
}

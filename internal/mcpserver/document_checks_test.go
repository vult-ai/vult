package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Only Vult writes document checks. An agent of any access reads them and
// has no tool that writes, passes or changes one.
func TestAgentsCanOnlyReadDocumentChecks(t *testing.T) {
	ctx := context.Background()
	for _, access := range []string{"read", "propose", "write"} {
		var called []string
		client := connect(t, ctx, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
			called = append(called, method)
			return json.RawMessage(`{}`), nil
		}, access, Options{Documents: true, OwnerUploads: true, ProposeReadings: true})
		catalog, err := client.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		reads := false
		for _, tool := range catalog.Tools {
			if tool.Name == "list_document_checks" {
				reads = true
				continue
			}
			if strings.Contains(tool.Name, "check") || strings.Contains(tool.Name, "receipt_details") {
				t.Fatalf("%s agent has %s", access, tool.Name)
			}
		}
		if !reads {
			t.Fatalf("%s agent can't read checks", access)
		}
		for _, name := range []string{"record_receipt_details", "write_document_check", "record_document_check", "set_document_check"} {
			r, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"document_id": "00000000-0000-0000-0000-000000000001", "result": "ok"}})
			if err == nil && !r.IsError {
				t.Fatalf("%s agent called %s", access, name)
			}
		}
		for _, method := range called {
			if method != "list_document_checks" && strings.Contains(method, "check") {
				t.Fatalf("a check write reached the ledger: %s", method)
			}
		}
	}
}

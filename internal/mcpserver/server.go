// Package mcpserver exposes the general ledger through a generic MCP transport.
package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/ledger"
	"io"
	"log/slog"
	"strings"
)

type Call func(context.Context, string, json.RawMessage) (json.RawMessage, error)
type definition struct {
	Name, Description, Properties string
	Required                      []string
}

var definitions = []definition{
	{"get_book", "Read this connection's Book, currency and precision.", "{}", nil},
	{"list_accounts", "Read permanent account IDs and their current display revisions.", "{}", nil},
	{"create_account", "Create one account in this Book. Reuse the exact request key and input on retries.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"class":{"type":"string","enum":["asset","liability","equity","income","expense"]},"code":{"type":"string","minLength":1,"maxLength":32},"name":{"type":"string","minLength":1,"maxLength":120}}`,
		[]string{"request_key", "class", "code", "name"}},
	{"revise_account", "Revise an account display code/name without changing its permanent identity or accounting class.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"account_id":{"type":"string"},"expected_revision":{"type":"integer","minimum":1},"code":{"type":"string","minLength":1,"maxLength":32},"name":{"type":"string","minLength":1,"maxLength":120}}`,
		[]string{"request_key", "account_id", "expected_revision", "code", "name"}},
	{"post_journal", "Record a balanced journal. Amounts are positive integer minor-unit strings; never decimal or floating-point amounts. Retrying the identical request key/input returns the original result. A lost reply is unresolved: look up the same key and retry unchanged.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"economic_date":{"type":"string","format":"date"},"accounting_date":{"type":"string","format":"date"},"description":{"type":"string","minLength":1,"maxLength":4000},"lines":{"type":"array","minItems":2,"maxItems":256,"items":{"type":"object","additionalProperties":false,"required":["account_id","side","amount_minor"],"properties":{"account_id":{"type":"string"},"side":{"type":"string","enum":["debit","credit"]},"amount_minor":{"type":"string","pattern":"^[1-9][0-9]{0,18}$"},"memo":{"type":"string","maxLength":1000}}}}}`,
		[]string{"request_key", "economic_date", "accounting_date", "description", "lines"}},
	{"get_journal", "Read one complete journal by permanent ID.", `{"entry_id":{"type":"string"}}`, []string{"entry_id"}},
	{"list_journals", "Read journals in stable sequence order. Pass next_after for the next page.",
		`{"after":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":100}}`, nil},
	{"reverse_journal", "Create an exact linked reversal in an open period. Original lines remain immutable. Reuse the same key/input on retries.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"entry_id":{"type":"string"},"accounting_date":{"type":"string","format":"date"},"reason":{"type":"string","minLength":1,"maxLength":4000}}`,
		[]string{"request_key", "entry_id", "accounting_date", "reason"}},
	{"get_balance", "Read an account's debit-positive balance in integer minor units.",
		`{"account_id":{"type":"string"},"through":{"type":"string","format":"date"}}`, []string{"account_id"}},
	{"get_trial_balance", "Read all account balances through an optional accounting date. Values use the Book currency.",
		`{"through":{"type":"string","format":"date"}}`, nil},
	{"get_request_outcome", "Read this connection actor's retained result. Unresolved does not mean rejected: retry only the original unchanged key/input.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128}}`, []string{"request_key"}},
}

func New(call Call, access string) *mcp.Server {
	return makeServer(call, access, false)
}

func NewRemote(call Call, access string) *mcp.Server { return makeServer(call, access, true) }
func makeServer(call Call, access string, remote bool) *mcp.Server {
	version := "0.2.0"
	options := &mcp.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if remote {
		options.Capabilities = &mcp.ServerCapabilities{}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "vult", Version: version}, options)
	for _, item := range definitions {
		if ledger.Writing(item.Name) && access != "write" {
			continue
		}
		item := item
		if remote && item.Name == "list_journals" {
			item.Properties = strings.ReplaceAll(item.Properties, `"maximum":100`, `"maximum":20`)
		}
		var properties map[string]json.RawMessage
		if json.Unmarshal([]byte(item.Properties), &properties) != nil {
			panic("invalid built-in tool schema")
		}
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(item.Required) > 0 {
			schema["required"] = item.Required
		}
		destructive := false
		annotations := &mcp.ToolAnnotations{ReadOnlyHint: !ledger.Writing(item.Name), IdempotentHint: true, DestructiveHint: &destructive}
		server.AddTool(&mcp.Tool{Name: item.Name, Description: item.Description, InputSchema: schema, Annotations: annotations},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				input := req.Params.Arguments
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				result, e := call(ctx, item.Name, input)
				if e != nil {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}, nil
				}
				return &mcp.CallToolResult{StructuredContent: json.RawMessage(result), Content: []mcp.Content{&mcp.TextContent{Text: string(result)}}}, nil
			})
	}
	return server
}

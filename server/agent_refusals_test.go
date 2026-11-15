package server

import (
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// Refusals a weak model can act on: over real MCP, a value that isn't an
// ID names the argument and the shape it needs, and an ID that isn't in
// the Book is named with what it is.
func TestRefusalsNameTheArgumentOverMCP(t *testing.T) {
	driver, _ := bankPaths(t, "mcp")
	d := driver.(mcpDriver)
	missing, other := ledger.ID(), ledger.ID()
	for _, tc := range []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"link_bank_activity", map[string]any{"request_key": "k", "transaction_id": "made-up-transaction", "entry_id": "1e30"},
			[]string{`transaction_id must be an ID as Vult returns it`, `got "made-up-transaction"`, `entry_id must be an ID as Vult returns it`}},
		{"record_by_rule", map[string]any{"request_key": "k", "transaction_id": "made-up-transaction", "rule_id": missing},
			[]string{`transaction_id must be an ID as Vult returns it`}},
		{"record_bank_transfer", map[string]any{"request_key": "k", "transaction_ids": []any{"1e30", missing}},
			[]string{`transaction_ids[0] must be an ID as Vult returns it`}},
		{"record_by_rule", map[string]any{"request_key": "k", "transaction_id": missing, "rule_id": other},
			[]string{missing, "a bank item (list_bank_activity)", other, "a recording rule (list_recording_rules)"}},
		{"record_bank_transfer", map[string]any{"request_key": "k", "transaction_ids": []any{missing, other}},
			[]string{missing, other, "a bank item (list_bank_activity)"}},
	} {
		_, err := d.call(t, tc.tool, tc.args)
		if err == nil {
			t.Fatalf("%s was accepted", tc.tool)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal %q lacks %q", tc.tool, err, want)
			}
		}
	}
}

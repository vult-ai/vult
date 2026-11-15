package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/ledger"
)

func refusal(t *testing.T, ctx context.Context, c *mcp.ClientSession, method string, input any) string {
	t.Helper()
	r, e := c.CallTool(ctx, &mcp.CallToolParams{Name: method, Arguments: input})
	if e != nil || !r.IsError || len(r.Content) != 1 {
		t.Fatal("expected a refusal", method, e, r)
	}
	return r.Content[0].(*mcp.TextContent).Text
}

// Wrong types, formats and unknown names are refused before Vult acts, the
// same way on every tool, naming the argument, what it expects with an
// example, and what was given.
func TestArgumentRefusalsNameTheArgumentAndAnExample(t *testing.T) {
	ctx := context.Background()
	called := 0
	client := connect(t, ctx, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		called++
		return json.RawMessage(`{}`), nil
	}, "write")
	id := "0190a8f2-6c1e-7d4b-9a63-2f5e8c1b7d40"
	for _, tc := range []struct {
		tool  string
		input any
		want  []string
	}{
		{"post_journal", map[string]any{"request_key": "k", "economic_date": "2026-02-30", "accounting_date": "02/01/2026", "description": "x",
			"lines": []any{map[string]any{"account_id": id, "side": "debit", "amount_minor": 1999}, map[string]any{"account_id": id, "side": "left", "amount_minor": "19.99"}}},
			[]string{"These arguments don't fit post_journal:", `economic_date "2026-02-30" isn't a real date`,
				`accounting_date must be a date as YYYY-MM-DD, like "2026-01-31"; got "02/01/2026"`,
				`lines[0].amount_minor must be an amount in minor units as a string of digits, like "1999" for 19.99; got the number 1999`,
				`lines[1].side must be one of "debit", "credit"; got "left"`,
				`lines[1].amount_minor must be an amount in minor units as a string of digits, like "1999" for 19.99; got "19.99"`}},
		{"list_journals", map[string]any{"limit": "ten"}, []string{`limit must be a whole number from 1 to 100, like 1; got "ten"`}},
		{"list_journals", map[string]any{"limit": 2.5}, []string{"limit must be a whole number from 1 to 100, like 1; got the number 2.5"}},
		{"list_recording_rules", map[string]any{"extra_field": 1}, []string{"These arguments don't fit list_recording_rules: it doesn't take extra_field"}},
		{"prepare_bank_connection", map[string]any{"extra_field": true}, []string{"it doesn't take extra_field"}},
		{"record_by_rule", map[string]any{"request_key": "k", "rule_id": id, "transaction_id": id, "amount": "5"},
			[]string{"it doesn't take amount (its arguments are request_key, rule_id, transaction_id)"}},
		{"record_by_rule", map[string]any{"rule_id": id}, []string{"it needs request_key, transaction_id"}},
		{"create_account", map[string]any{"request_key": "k", "class": "asset", "name": "", "currency": "usd"},
			[]string{"name can't be empty", `currency must be a three-letter currency code in capitals, like "USD"; got "usd"`}},
		{"prepare_operation", map[string]any{"request_key": "p", "operation": "record_bank_activity",
			"arguments": map[string]any{"request_key": "k", "transaction_id": id, "description": "x", "other_account": id}},
			[]string{"These arguments don't fit prepare_operation: arguments doesn't take other_account (its arguments are"}},
		{"prepare_operation", map[string]any{"request_key": "p", "operation": "record_bank_activity",
			"arguments": map[string]any{"request_key": "k", "transaction_id": id, "description": "x", "other_account_id": id, "assumptions": []any{"a"}}},
			[]string{"assumptions goes beside arguments, not inside it"}},
		{"prepare_operation", map[string]any{"request_key": "p", "operation": "record_bank_activity", "assumptions": "one thing",
			"arguments": map[string]any{"request_key": "k", "transaction_id": id, "description": "x", "other_account_id": id}},
			[]string{`assumptions must be a list of text, like ["First point.", "Second point."]; got "one thing"`}},
	} {
		text := refusal(t, ctx, client, tc.tool, tc.input)
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Fatalf("%s refusal %q lacks %q", tc.tool, text, want)
			}
		}
	}
	if called != 0 {
		t.Fatal("a call that doesn't fit its schema reached Vult", called)
	}
}

// Refusals that say too little name what the call gave.
func TestNotFoundNamesTheIDAndWhatItIs(t *testing.T) {
	ctx := context.Background()
	client := connect(t, ctx, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if method == "prepare_operation" {
			return nil, ledger.ErrInvalid
		}
		return nil, ledger.ErrNotFound
	}, "write")
	id, other := "0190a8f2-6c1e-7d4b-9a63-2f5e8c1b7d40", "0190a8f2-6c1e-7d4b-9a63-2f5e8c1b7d41"
	if text := refusal(t, ctx, client, "get_journal", map[string]any{"entry_id": id}); text != `Not found in this Book: entry_id "`+id+`", an entry (list_journals).` {
		t.Fatal("one ID", text)
	}
	text := refusal(t, ctx, client, "record_by_rule", map[string]any{"request_key": "k", "rule_id": id, "transaction_id": other})
	if text != `One of these isn't in this Book: rule_id "`+id+`", a recording rule (list_recording_rules); transaction_id "`+other+`", a bank item (list_bank_activity).` {
		t.Fatal("two IDs", text)
	}
	text = refusal(t, ctx, client, "prepare_operation", map[string]any{"request_key": "p", "operation": "record_bank_activity",
		"arguments": map[string]any{"request_key": "k", "transaction_id": id, "description": "x", "other_account_id": other}})
	if !strings.HasPrefix(text, "Vult can't accept these values for prepare_operation.") {
		t.Fatal("a bare invalid request", text)
	}
}

// A write says what it did in a plain sentence of counts and IDs.
func TestWritesSayWhatTheyDidInPlainWords(t *testing.T) {
	for _, tc := range []struct {
		tool, result, want string
	}{
		{"prepare_operation", `{"id":"p1","operation":"record_bank_activity"}`, "Prepared 1 proposal: p1 (record_bank_activity). Recorded 0: nothing is in the Book until the owner approves it in Vult."},
		{"record_by_rule", `{"links":[{"transaction_id":"t1","entry_id":"e1"}],"entry":{"id":"e1"}}`, "Recorded 1 entry: e1, linked to 1 bank item: t1."},
		{"link_bank_activity", `{"links":[{"transaction_id":"t1","entry_id":"e1"},{"transaction_id":"t2","entry_id":"e1"}]}`, "Linked 2 bank items to entry e1: t1, t2. Recorded 0 new entries."},
		{"post_journal", `{"id":"e2","lines":[]}`, "Recorded 1 entry: e2."},
		{"list_accounts", `{"accounts":[]}`, ""},
	} {
		if got := summarize(tc.tool, json.RawMessage(tc.result)); got != tc.want {
			t.Fatalf("%s: %q", tc.tool, got)
		}
	}
}

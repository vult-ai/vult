package mcpserver

import "encoding/json"

var proposalDefinitions = []definition{
	{"prepare_operation", "Validate and queue an exact journal or reversal for the owner's approval without posting. Supply a separate preparation request_key and the fixed financial request_key inside arguments. Effects and source reuse are derived by Vult; assumptions and open questions do not establish accounting facts. The proposal waits in Vult until the owner decides; their approval posts the exact entry. Show the owner approval.message and approval.href, then continue other work. Retrying unchanged preparation returns its original record.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"operation":{"type":"string","enum":["post_journal","reverse_journal"]},"arguments":{"type":"object"},"assumptions":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":1000}},"open_questions":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":1000}}}`,
		[]string{"request_key", "operation", "arguments"}},
	{"get_operation_proposal", "Read exact prepared work, current status, decision and any posted outcome in this Book. Supply either proposal_id or your original preparation request_key. committed means the owner's approval posted the entry shown in outcome; rejected carries the owner's reason; stale means dependencies changed and a fresh proposal is needed. Another authorized Book reader can inspect a known proposal and recover its outcome after the original agent is gone.",
		`{"proposal_id":{"type":"string","format":"uuid"},"request_key":{"type":"string","minLength":1,"maxLength":128}}`, nil},
	{"execute_proposal", "Complete a proposal approved under the earlier contract, where the owner selected this connection to post it. New approvals post directly and never need this. Existing live Book write authority, unexpired approval and unchanged dependencies remain required. Exact retries return the original result. After a lost reply inspect the proposal or get_request_outcome with its original operation_key; never invent a replacement key.",
		`{"proposal_id":{"type":"string","format":"uuid"},"sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}}`, []string{"proposal_id", "sha256"}},
}

func addSourceProperties(method string, properties map[string]json.RawMessage, enabled bool) {
	if enabled && method == "post_journal" {
		properties["sources"] = json.RawMessage(`{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["document_id"],"properties":{"document_id":{"type":"string"},"location":` + locationSchema + `}}}`)
	}
}

func operationArgumentSchema(method string, documents bool) map[string]any {
	for _, item := range definitions {
		if item.Name != method {
			continue
		}
		var properties map[string]json.RawMessage
		if json.Unmarshal([]byte(item.Properties), &properties) != nil {
			panic("invalid operation schema")
		}
		addMeasurementProperties(method, properties)
		addSourceProperties(method, properties, documents)
		return map[string]any{"type": "object", "properties": properties, "required": item.Required, "additionalProperties": false}
	}
	panic("unsupported operation schema")
}

func addProposalConstraints(name string, schema map[string]any, documents bool) {
	switch name {
	case "prepare_operation":
		choices := []any{}
		for _, operation := range []string{"post_journal", "reverse_journal"} {
			choices = append(choices, map[string]any{"properties": map[string]any{
				"operation": map[string]string{"const": operation}, "arguments": operationArgumentSchema(operation, documents),
			}})
		}
		schema["oneOf"] = choices
	case "get_operation_proposal":
		schema["oneOf"] = []any{
			map[string]any{"required": []string{"proposal_id"}, "not": map[string]any{"required": []string{"request_key"}}},
			map[string]any{"required": []string{"request_key"}, "not": map[string]any{"required": []string{"proposal_id"}}},
		}
	}
}

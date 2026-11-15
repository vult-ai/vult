package mcpserver

import "encoding/json"

var proposalDefinitions = []definition{
	{"prepare_opening_balance", "Prepare this Book's half of an opening balance between linked Books for the owner's approval: what one owed the other as of a date before their first bank line, from the owner's records. Prepare the same pair_key in the other Book through your connection there, with owed_by from its side; the owner approves both at once. owed_by is this_book when this Book owes the other, or other_book. Each half posts the owed direction's account against the Book's opening balances account (equity, role opening_balance). Entered once per direction; undoing either half undoes both.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":120},"other_book_id":{"type":"string","format":"uuid"},"owed_by":{"type":"string","enum":["this_book","other_book"]},"as_of":{"type":"string","format":"date"},"amount_minor":{"type":"string","pattern":"^[1-9][0-9]{0,17}$"},"pair_key":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,64}$"},"description":{"type":"string","minLength":1,"maxLength":4000},"reason":{"type":"string","minLength":1,"maxLength":4000}}`,
		[]string{"request_key", "other_book_id", "owed_by", "as_of", "amount_minor", "pair_key", "reason"}},
	{"prepare_operation", "Validate and queue exact work for the owner's approval without recording it: a journal or reversal, an account change (create, revise, retire with its transfer, reactivate, or apply a starter chart), a payee change (create, revise, or an entry's payee), a statement decision, recording or linking bank activity, a bank management decision (link a bank account, disconnect, pause, resume), or closing or reopening a period. Supply a separate preparation request_key and the fixed request_key inside arguments. Effects and source reuse are derived by Vult; assumptions and open questions do not establish accounting facts. The proposal waits in Vult until the owner decides; their approval records exactly the previewed work. Show the owner approval.message and approval.href, then continue other work. Retrying unchanged preparation returns its original record. Example: {\"request_key\":\"prepare-fee-0402\",\"operation\":\"record_bank_activity\",\"arguments\":{\"request_key\":\"record-fee-0402\",\"transaction_id\":\"<id>\",\"description\":\"Monthly bank fee\",\"other_account_id\":\"<expense account_id>\",\"reason\":\"The bank charges this fee monthly\"},\"assumptions\":[\"The fee is an ordinary operating expense.\"]}. assumptions and open_questions go beside arguments, each a list of text.",
		`{"request_key":{"type":"string","minLength":1,"maxLength":128},"operation":{"type":"string","enum":["post_journal","reverse_journal","create_account","revise_account","retire_account","reactivate_account","apply_starter_chart","create_payee","revise_payee","set_entry_payee","import_statement","mark_statement_duplicate","match_statement","revoke_statement_match","record_statement_adjustment","review_statement","close_period","reopen_period","link_bank_activity","unlink_bank_activity","record_bank_activity","record_bank_transfer","link_bank_account","disconnect_bank","pause_bank_connection","resume_bank_connection","mark_bank_duplicate"]},"arguments":{"type":"object"},"assumptions":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":1000}},"open_questions":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":1000}}}`,
		[]string{"request_key", "operation", "arguments"}},
	{"get_operation_proposal", "Read exact prepared work, current status, decision and any posted outcome in this Book. Supply either proposal_id or the original preparation request_key. committed means the owner's approval posted the entry shown in outcome; rejected carries the owner's reason; stale means dependencies changed and a fresh proposal is needed. Another authorized Book reader can inspect a known proposal and recover its outcome after the original agent is gone.",
		`{"proposal_id":{"type":"string","format":"uuid"},"request_key":{"type":"string","minLength":1,"maxLength":128}}`, nil},
	{"execute_proposal", "Complete a proposal approved under the earlier contract, where the owner selected this connection to post it. New approvals post directly and never need this. Existing live Book write authority, unexpired approval and unchanged dependencies remain required. Exact retries return the original result. After a lost reply inspect the proposal or get_request_outcome with its original operation_key; never invent a replacement key.",
		`{"proposal_id":{"type":"string","format":"uuid"},"sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}}`, []string{"proposal_id", "sha256"}},
}

func addSourceProperties(method string, properties map[string]json.RawMessage, enabled bool) {
	if enabled && (method == "post_journal" || method == "retire_account") {
		properties["sources"] = json.RawMessage(`{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["document_id"],"properties":{"document_id":{"type":"string"},"location":` + locationSchema + `}}}`)
	}
}

// addPairProperties adds, to a proposal's arguments only, what makes it half
// of a pair between linked Books (see get_book_links).
func addPairProperties(method string, properties map[string]json.RawMessage) {
	key := json.RawMessage(`{"type":"string","pattern":"^[A-Za-z0-9_-]{1,64}$"}`)
	id := json.RawMessage(`{"type":"string","format":"uuid"}`)
	by := json.RawMessage(`{"type":"string","enum":["rule","known_vendor","model","answer"]}`)
	switch method {
	case "post_journal":
		properties["pair_key"], properties["paired_transaction_id"], properties["payer_book_id"] = key, id, id
		properties["reason"] = json.RawMessage(`{"type":"string","maxLength":4000}`)
		properties["assigned_by"], properties["opening"], properties["other_book_id"] = by, json.RawMessage(`{"type":"boolean"}`), id
	case "record_bank_activity":
		properties["pair_key"], properties["assigned_by"] = key, by
		properties["settlement"] = json.RawMessage(`{"type":"boolean"}`)
		properties["repays"] = json.RawMessage(`{"type":"array","maxItems":50,"items":{"type":"string","pattern":"^[A-Za-z0-9_-]{1,64}$"}}`)
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
		addPairProperties(method, properties)
		return map[string]any{"type": "object", "properties": properties, "required": item.Required, "additionalProperties": false}
	}
	panic("unsupported operation schema")
}

func addProposalConstraints(name string, schema map[string]any, documents bool) {
	switch name {
	case "prepare_operation":
		choices := []any{}
		for _, operation := range []string{"post_journal", "reverse_journal", "create_account", "revise_account", "retire_account", "reactivate_account", "apply_starter_chart", "create_payee", "revise_payee", "set_entry_payee", "import_statement", "mark_statement_duplicate", "match_statement", "revoke_statement_match", "record_statement_adjustment", "review_statement", "close_period", "reopen_period", "link_bank_activity", "unlink_bank_activity", "record_bank_activity", "record_bank_transfer", "link_bank_account", "disconnect_bank", "pause_bank_connection", "resume_bank_connection", "mark_bank_duplicate"} {
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

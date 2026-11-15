package mcpserver

import (
	"encoding/json"

	"github.com/vult-ai/vult/ledger"
)

// titles are the names agents' apps show for each tool, such as "Post journal
// entry" instead of post_journal. Every tool has one.
var titles = map[string]string{
	"get_book":                      "Get Book",
	"set_book_logo":                 "Set Book logo",
	"list_accounts":                 "List accounts",
	"create_account":                "Create account",
	"revise_account":                "Revise account",
	"retire_account":                "Retire account",
	"reactivate_account":            "Reactivate account",
	"list_starter_charts":           "List starter charts",
	"apply_starter_chart":           "Apply starter chart",
	"list_payees":                   "List payees",
	"create_payee":                  "Create payee",
	"revise_payee":                  "Revise payee",
	"set_entry_payee":               "Set entry payee",
	"get_entry_payee":               "Get entry payee",
	"list_recurring_charges":        "List recurring charges",
	"list_projects":                 "List projects",
	"label_project":                 "Label spending by project",
	"get_project_report":            "Read the projects report",
	"post_journal":                  "Post journal entry",
	"get_journal":                   "Get journal entry",
	"list_journals":                 "List journal entries",
	"reverse_journal":               "Reverse journal entry",
	"get_balance":                   "Get account balance",
	"list_closed_periods":           "List closed periods",
	"get_exchange_rate":             "Get exchange rate",
	"get_cash_flow":                 "Get cash flow",
	"import_statement":              "Import statement",
	"list_statements":               "List statements",
	"get_statement_reconciliation":  "Get statement reconciliation",
	"suggest_statement_matches":     "Suggest statement matches",
	"match_statement":               "Match statement rows",
	"revoke_statement_match":        "Undo statement match",
	"record_statement_adjustment":   "Record statement adjustment",
	"mark_statement_duplicate":      "Mark statement duplicate",
	"review_statement":              "Review statement",
	"get_period_review":             "Review period before closing",
	"list_bank_activity":            "List bank activity",
	"suggest_bank_matches":          "Suggest bank matches",
	"link_bank_activity":            "Link bank activity to entry",
	"unlink_bank_activity":          "Unlink bank activity",
	"record_bank_activity":          "Record bank activity",
	"record_bank_transfer":          "Record transfer between own accounts",
	"mark_bank_duplicate":           "Mark bank activity as a repeat",
	"get_entry_origin":              "Why is this entry here",
	"list_recording_rules":          "List recording rules",
	"list_agent_kinds":              "List agent trust",
	"get_book_link":                 "Read linked Book",
	"get_book_links":                "Read linked Books",
	"get_linked_balances":           "Read who owes whom",
	"list_assignment_rules":         "Read where charges go",
	"get_agent_summary":             "Get agent summary",
	"list_agent_questions":          "List questions",
	"ask_question":                  "Ask a question",
	"withdraw_question":             "Withdraw a question",
	"list_agent_loops":              "List loose ends",
	"open_loop":                     "Open a loose end",
	"close_loop":                    "Close a loose end",
	"get_agent_profile":             "Get the person's profile",
	"update_agent_profile":          "Update the person's profile",
	"list_operation_proposals":      "List my proposals",
	"record_by_rule":                "Record by rule",
	"prepare_bank_connection":       "Ask the owner to connect a bank",
	"link_bank_account":             "Link bank account to Book account",
	"disconnect_bank":               "Disconnect bank",
	"pause_bank_connection":         "Pause bank sync",
	"resume_bank_connection":        "Resume bank sync",
	"close_period":                  "Close period",
	"reopen_period":                 "Reopen period",
	"get_trial_balance":             "Get trial balance",
	"search_book":                   "Search Book",
	"get_request_outcome":           "Get request outcome",
	"prepare_operation":             "Prepare work for approval",
	"prepare_opening_balance":       "Prepare an opening balance",
	"get_operation_proposal":        "Get entry awaiting approval",
	"execute_proposal":              "Post approved entry",
	"wait_for_document":             "Wait for owner upload",
	"report_unusable_document":      "Report unusable upload",
	"list_document_checks":          "Read document checks",
	"propose_document_reading":      "Propose a document's reading",
	"add_document":                  "Add source document",
	"get_document":                  "Get source document",
	"list_documents":                "List source documents",
	"set_document_group":            "File a document",
	"correct_source_citation":       "Correct source citation",
	"attach_evidence":               "Attach evidence",
	"detach_evidence":               "Detach evidence",
	"get_entry_documents":           "Get entry documents",
	"get_source_usage":              "Get source usage",
	"list_entries_missing_evidence": "List entries missing evidence",
}

func writes(name string) bool {
	return ledger.Writing(name) || name == "report_unusable_document" || name == ProposeReadingTool
}

// Allowed reports whether an agent with this access level gets the tool: read
// gets tools that don't write, propose also prepares entries for approval, and
// write gets every tool.
func Allowed(name, access string) bool {
	switch access {
	case "write":
		return true
	case "propose":
		// The owner may grant a proposing connection recording within the
		// rules; the ledger refuses these tools without that grant.
		return !writes(name) || name == "prepare_operation" || name == "prepare_opening_balance" || ledger.RuleRecordingMethod(name) || ledger.NoteMethod(name) || name == ProposeReadingTool
	}
	return !writes(name)
}

// Tool is one tool as an agent inside this program is offered it: the same
// name, description and input schema an agent over MCP gets.
type Tool struct {
	Name, Title, Description string
	Schema                   map[string]any
}

// Tools lists the Book tools a connection with this access level gets,
// without the document tools.
func Tools(access string) []Tool {
	var out []Tool
	for _, item := range append(append([]definition{}, definitions...), proposalDefinitions...) {
		if !Allowed(item.Name, access) || preparedOnly[item.Name] {
			continue
		}
		var properties map[string]json.RawMessage
		if json.Unmarshal([]byte(item.Properties), &properties) != nil {
			panic("invalid built-in tool schema")
		}
		addMeasurementProperties(item.Name, properties)
		addSourceProperties(item.Name, properties, false)
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(item.Required) > 0 {
			schema["required"] = item.Required
		}
		addProposalConstraints(item.Name, schema, false)
		out = append(out, Tool{Name: item.Name, Title: titles[item.Name], Description: item.Description, Schema: schema})
	}
	return out
}

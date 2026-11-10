package mcpserver

import "github.com/vult-ai/vult/ledger"

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
	"close_period":                  "Close period",
	"reopen_period":                 "Reopen period",
	"get_trial_balance":             "Get trial balance",
	"search_book":                   "Search Book",
	"get_request_outcome":           "Get request outcome",
	"prepare_operation":             "Prepare work for approval",
	"get_operation_proposal":        "Get entry awaiting approval",
	"execute_proposal":              "Post approved entry",
	"wait_for_document":             "Wait for owner upload",
	"report_unusable_document":      "Report unusable upload",
	"add_document":                  "Add source document",
	"get_document":                  "Get source document",
	"list_documents":                "List source documents",
	"correct_source_citation":       "Correct source citation",
	"attach_evidence":               "Attach evidence",
	"detach_evidence":               "Detach evidence",
	"get_entry_documents":           "Get entry documents",
	"get_source_usage":              "Get source usage",
	"list_entries_missing_evidence": "List entries missing evidence",
}

func writes(name string) bool { return ledger.Writing(name) || name == "report_unusable_document" }

// Allowed reports whether an agent with this access level gets the tool: read
// gets tools that don't write, propose also prepares entries for approval, and
// write gets every tool.
func Allowed(name, access string) bool {
	switch access {
	case "write":
		return true
	case "propose":
		return !writes(name) || name == "prepare_operation"
	}
	return !writes(name)
}

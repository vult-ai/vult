// Package setaside names what this release keeps out of reach: features
// built on the way to it that wait for later releases, with their screens.
// Programs that ship turn it on; tests and other callers leave it off, so
// the features keep their tests and come back by leaving this list.
package setaside

import "strings"

// Tools are the agent tools set aside: bank connections and bank activity,
// linked Books, projects, document checks, and the questions and memory of
// Vult's own agent.
var Tools = map[string]bool{
	"ask_question":             true,
	"close_loop":               true,
	"disconnect_bank":          true,
	"get_agent_profile":        true,
	"get_agent_summary":        true,
	"get_book_link":            true,
	"get_book_links":           true,
	"get_entry_origin":         true,
	"get_linked_balances":      true,
	"get_project_report":       true,
	"label_project":            true,
	"link_bank_account":        true,
	"link_bank_activity":       true,
	"list_agent_kinds":         true,
	"list_agent_loops":         true,
	"list_agent_questions":     true,
	"list_assignment_rules":    true,
	"list_bank_activity":       true,
	"list_document_checks":     true,
	"list_operation_proposals": true,
	"list_projects":            true,
	"list_recording_rules":     true,
	"mark_bank_duplicate":      true,
	"open_loop":                true,
	"pause_bank_connection":    true,
	"prepare_bank_connection":  true,
	"prepare_opening_balance":  true,
	"propose_document_reading": true,
	"record_bank_activity":     true,
	"record_bank_transfer":     true,
	"record_by_rule":           true,
	"resume_bank_connection":   true,
	"set_document_group":       true,
	"suggest_bank_matches":     true,
	"unlink_bank_activity":     true,
	"update_agent_profile":     true,
	"withdraw_question":        true}

// paths are the server addresses set aside, each with everything under it.
// Backups, restore and archiving a Book are this release's and stay.
var paths = []string{
	"/api/v1/agent-chat", "/api/v1/agent-notes", "/api/v1/agent-settings",
	"/api/v1/agent-owner/approve-batch", "/api/v1/agent-owner/plans/details",
	"/api/v1/assignment-rules", "/api/v1/recording-rules",
	"/api/v1/bank", "/api/v1/book-links", "/api/v1/linked-balances", "/api/v1/pairs", "/api/v1/settlements",
	"/api/v1/books/deletion",
	"/api/v1/document-checks", "/api/v1/document-reading", "/api/v1/documents/group", "/api/v1/statement-files",
	"/api/v1/entry-origin", "/api/v1/events", "/api/v1/expense-reports", "/api/v1/projects", "/api/v1/reminders",
}

// Commands are the terminal commands set aside; the program that ships
// has no other way to run them.
var Commands = map[string]bool{"book delete": true}

// Path says whether an address is set aside.
func Path(path string) bool {
	for _, p := range paths {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

// idKinds names what each ID argument refers to and where to read it.
var idKinds = map[string]string{
	"account_id":             "an account (list_accounts)",
	"destination_account_id": "an account (list_accounts)",
	"other_account_id":       "an account (list_accounts)",
	"bank_account_id":        "a bank account (prepare_bank_connection)",
	"connection_id":          "a bank connection (prepare_bank_connection)",
	"transaction_id":         "a bank item (list_bank_activity)",
	"rule_id":                "a recording rule (list_recording_rules)",
	"entry_id":               "an entry (list_journals)",
	"journal_id":             "an entry (list_journals)",
	"proposal_id":            "a proposal (list_operation_proposals)",
	"payee_id":               "a payee (list_payees)",
	"statement_id":           "a statement (list_statements)",
	"row_id":                 "a statement line (get_statement_reconciliation)",
	"document_id":            "an original (list_documents)",
	"upload_id":              "an upload (get_document)",
	"link_id":                "a link (get_entry_origin or get_entry_documents)",
	"source_id":              "a source (get_entry_documents)",
	"period_id":              "a period (list_closed_periods)",
	"question_id":            "a question (list_agent_questions)",
	"loop_id":                "a loose end (list_agent_loops)",
}

// explain turns a refusal that says too little into one an agent can act
// on. Other refusals already say what to do and pass through unchanged.
func explain(tool string, input json.RawMessage, e error) string {
	switch {
	case errors.Is(e, ledger.ErrNotFound) && e.Error() == ledger.ErrNotFound.Error():
		ids := idArguments(input)
		switch len(ids) {
		case 0:
			return "Not found in this Book."
		case 1:
			return "Not found in this Book: " + ids[0] + "."
		}
		return "One of these isn't in this Book: " + strings.Join(ids, "; ") + "."
	case errors.Is(e, ledger.ErrInvalid) && e.Error() == ledger.ErrInvalid.Error():
		return "Vult can't accept these values for " + tool + ". The arguments have the right names and types, so a value doesn't fit this Book: check that each ID was read from this Book, that dates fall in an open period, and that the amounts and accounts fit the operation."
	}
	return e.Error()
}

// idArguments lists the ID arguments given, with what each one names.
func idArguments(input json.RawMessage) []string {
	var value any
	if json.Unmarshal(input, &value) != nil {
		return nil
	}
	var out []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range sortedKeys(x) {
				if _, ok := x[k].(map[string]any); ok {
					walk(path+k+".", x[k])
				} else {
					walk(path+k, x[k])
				}
			}
		case []any:
			for i, item := range x {
				if _, ok := item.(string); ok {
					walk(fmt.Sprintf("%s[%d]", path, i), item)
				} else {
					walk(fmt.Sprintf("%s[%d].", path, i), item)
				}
			}
		case string:
			name := path
			if i := strings.LastIndex(name, "["); i >= 0 && strings.HasSuffix(name, "]") {
				name = strings.TrimSuffix(name[:i], "s")
			}
			if i := strings.LastIndexAny(name, ".]"); i >= 0 {
				name = name[i+1:]
			}
			if kind, ok := idKinds[name]; ok {
				out = append(out, path+" "+quote(x)+", "+kind)
			}
		}
	}
	walk("", value)
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(list []string) []string {
	var out []string
	for i, s := range list {
		if i == 0 || s != list[i-1] {
			out = append(out, s)
		}
	}
	return out
}

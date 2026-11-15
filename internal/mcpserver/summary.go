package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// summarize says in a plain sentence what a write did, with counts and
// IDs. It goes beside the result, which stays exactly as recorded, so a
// retry still returns the same bytes. An agent can repeat the sentence as
// it is, leaving less room to report work that didn't happen.
func summarize(tool string, result json.RawMessage) string {
	trimmed := bytes.TrimSpace(result)
	if !writes(tool) || len(trimmed) < 2 || trimmed[0] != '{' {
		return ""
	}
	var v struct {
		ID        string  `json:"id"`
		Operation string  `json:"operation"`
		Summary   *string `json:"summary"`
		Approval  *struct {
			Message string `json:"message"`
		} `json:"approval"`
		Entry *struct {
			ID string `json:"id"`
		} `json:"entry"`
		Links []struct {
			TransactionID string `json:"transaction_id"`
			EntryID       string `json:"entry_id"`
		} `json:"links"`
	}
	if json.Unmarshal(trimmed, &v) != nil || v.Summary != nil {
		return ""
	}
	items := make([]string, 0, len(v.Links))
	for _, l := range v.Links {
		items = append(items, l.TransactionID)
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	var text string
	switch {
	case tool == "prepare_operation" && v.ID != "":
		text = fmt.Sprintf("Prepared 1 proposal: %s (%s). Recorded 0: nothing is in the Book until the owner approves it in Vult.", v.ID, v.Operation)
	case v.Entry != nil && v.Entry.ID != "":
		text = fmt.Sprintf("Recorded 1 entry: %s, linked to %d bank %s: %s.", v.Entry.ID, len(items), plural(len(items), "item", "items"), strings.Join(items, ", "))
	case len(v.Links) > 0 && tool == "unlink_bank_activity":
		text = fmt.Sprintf("Unlinked %d bank %s: %s. Recorded 0 new entries; the entry stays in the Book.", len(items), plural(len(items), "item", "items"), strings.Join(items, ", "))
	case len(v.Links) > 0:
		text = fmt.Sprintf("Linked %d bank %s to entry %s: %s. Recorded 0 new entries.", len(items), plural(len(items), "item", "items"), v.Links[0].EntryID, strings.Join(items, ", "))
	case tool == "post_journal" || tool == "reverse_journal":
		if v.ID != "" {
			text = "Recorded 1 entry: " + v.ID + "."
		}
	case v.ID != "":
		title := titles[tool]
		if title == "" {
			title = tool
		}
		text = fmt.Sprintf("Done: %s, ID %s.", title, v.ID)
	}
	return text
}

package local

import (
	"strings"
	"testing"
)

func TestNativeSessionPromptNamesExactApprovedFolders(t *testing.T) {
	q := SessionRequest{ConnectionID: "11111111-1111-4111-8111-111111111111", ImportDirectory: "/example/imports", ExportDirectory: "/example/exports"}
	prompt, err := OwnerPrompt("open_session", q)
	if err != nil || !strings.Contains(prompt, q.ImportDirectory) || !strings.Contains(prompt, q.ExportDirectory) || !strings.Contains(prompt, q.ConnectionID) {
		t.Fatal("approval omitted folder authority", err)
	}
	for _, path := range []string{"/", "relative", "/example/../private", "/example\nmisleading"} {
		bad := q
		bad.ImportDirectory = path
		if _, err := OwnerPrompt("open_session", bad); err == nil {
			t.Fatal("ambiguous session folder accepted")
		}
	}
	changed := q
	changed.ExportDirectory = "/example/elsewhere"
	if Digest(changed) == Digest(q) {
		t.Fatal("folder authority not bound into exact authorization")
	}
}

func TestPlainArchiveApprovalDisclosesOriginalsAndBook(t *testing.T) {
	q := ArchiveRequest{BookID: "11111111-1111-4111-8111-111111111111", Encrypted: false}
	prompt, err := OwnerPrompt("archive_backup", q)
	if err != nil || !strings.Contains(prompt, "unencrypted") || !strings.Contains(prompt, "original documents") || !strings.Contains(prompt, q.BookID) {
		t.Fatal("plain export approval omitted its effect", err)
	}
}

func TestPeriodClosePromptNamesTheRangeAndReason(t *testing.T) {
	q := map[string]string{"book_id": "11111111-1111-4111-8111-111111111111", "request_key": "example-close",
		"from": "2026-01-01", "through": "2026-01-31", "reason": "The final receipt is still outstanding"}
	prompt, err := OwnerPrompt("period_close", q)
	if err != nil || !strings.Contains(prompt, q["book_id"]) || !strings.Contains(prompt, q["from"]) || !strings.Contains(prompt, q["through"]) || !strings.Contains(prompt, q["reason"]) {
		t.Fatal("approval omitted the close decision", prompt, err)
	}
}

package pdfwrite

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/pdftext"
)

// A document reads back as its text, page by page, and the same text
// always gives the same bytes.
func TestDocumentsReadBackAndAreStable(t *testing.T) {
	build := func() []byte {
		d := New("Expense report ER-7Q2M-K4HD-9W1C")
		d.Text(16, true, "Expense report ER-7Q2M-K4HD-9W1C")
		d.Text(10, false, "Example Labs Inc. pays back Kit Blanche (shareholder-employee) — café receipt (ok)")
		for i := 0; i < 80; i++ {
			d.Text(10, false, "3 Nov  Example Air  Flight to the customer meeting in Denver  $420.00")
		}
		return d.Bytes()
	}
	a, b := build(), build()
	if !bytes.Equal(a, b) || !bytes.HasPrefix(a, []byte("%PDF-1.4")) || !bytes.HasSuffix(a, []byte("%%EOF\n")) {
		t.Fatal("not a stable PDF")
	}
	if strings.Count(string(a), "/Type /Page ") < 2 {
		t.Fatal("long text didn't flow onto a second page")
	}
	pages, err := pdftext.Pages(a)
	if err != nil || len(pages) < 2 {
		t.Fatal(len(pages), err)
	}
	text := strings.Join(pages, "\n")
	if !strings.Contains(text, "ER-7Q2M-K4HD-9W1C") || !strings.Contains(text, "Kit Blanche") {
		t.Fatal("text", text[:min(len(text), 200)])
	}
}

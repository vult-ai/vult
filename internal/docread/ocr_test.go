package docread

import (
	"context"
	"os"
	"testing"
)

// A scan is read on this machine and then like any statement's text. The
// synthetic scan says what an English statement would.
func TestAScanIsReadLocally(t *testing.T) {
	ocr := LocalOCR()
	if ocr == nil {
		t.Skip("no local text recognition installed")
	}
	scan, err := os.ReadFile("testdata/scan.jpg")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := ocr.Pages(context.Background(), "image/jpeg", scan)
	if err != nil {
		t.Fatal(err)
	}
	r := Text(pages)
	if r.Holder.Value != "Eightball Inc" || r.Last4() != "0001" || r.PeriodStart.Value != "2026-09-01" || r.PeriodEnd.Value != "2026-09-30" {
		t.Fatalf("fields: %+v", r)
	}
	if r.Opening.Value != "1000" || r.Closing.Value != "2149.99" || len(r.Lines) != 2 || r.Lines[0].Amount != "-50.01" || r.Lines[1].Amount != "1200" {
		t.Fatalf("numbers: %+v %+v %+v", r.Opening, r.Closing, r.Lines)
	}
	if err := Verify(r, pages); err != nil {
		t.Fatal(err)
	}
}

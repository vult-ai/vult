package pdftext_test

import (
	"errors"
	"testing"

	"github.com/vult-ai/vult/internal/pdftext"
	"github.com/vult-ai/vult/internal/pdftext/pdftest"
)

func TestPagesReadTheTextLayer(t *testing.T) {
	pages := [][]string{
		{"Example Bank", "Łucja Wójcik-Nowak", "RYNEK 50/7, 31-001 KRAKÓW", "Account ending 0001"},
		{"2026-09-05  Example supplies  -50.00", "Closing balance 950.00 €"},
	}
	for _, o := range []pdftest.Options{{}, {Packed: true}} {
		got, err := pdftext.Pages(pdftest.PDF(pages, o))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatal("two pages", got)
		}
		want := []string{"Example Bank\nŁucja Wójcik-Nowak\nRYNEK 50/7, 31-001 KRAKÓW\nAccount ending 0001", "2026-09-05  Example supplies  -50.00\nClosing balance 950.00 €"}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("page %d (%+v):\n%q\nwant\n%q", i+1, o, got[i], want[i])
			}
		}
	}
	plain, err := pdftext.Pages(pdftest.PDF([][]string{{"Café (Main) Street"}}, pdftest.Options{Plain: true}))
	if err != nil || len(plain) != 1 || plain[0] != "Café (Main) Street" {
		t.Fatal("a simple font reads as Latin-1", plain, err)
	}
}

func TestPagesRefuseWhatIsNoPDF(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("hello"), []byte("\x89PNG\r\n\x1a\n")} {
		if _, err := pdftext.Pages(b); !errors.Is(err, pdftext.ErrUnreadable) {
			t.Fatal("not a PDF", err)
		}
	}
}

// A damaged file reads as unreadable or partial, never a crash.
func TestDamagedFilesNeverCrash(t *testing.T) {
	whole := pdftest.PDF([][]string{{"Example Bank", "Zoë Brontë"}, {"Closing 1.00"}}, pdftest.Options{Packed: true})
	for cut := 0; cut < len(whole); cut += 7 {
		_, _ = pdftext.Pages(whole[:cut])
	}
	broken := append([]byte{}, whole...)
	for i := 40; i < len(broken); i += 13 {
		broken[i] ^= 0x5a
		_, _ = pdftext.Pages(broken)
	}
}

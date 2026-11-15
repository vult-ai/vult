package docread

import "testing"

// A statement may print both balances on one line; each is read.
func TestBothBalancesOnOneLine(t *testing.T) {
	for _, page := range []string{
		"Example Bank\nAccount ending 9876\nStatement period 2026-10-01 to 2026-10-31\nOpening balance: $0.00 · Closing balance: $7,825.01\n2026-10-03 EXAMPLE CLIENT 7,825.01",
		"Przykładowy Bank\nRachunek ****9876\nOkres 2026-10-01 - 2026-10-31\nSaldo początkowe: 0,00 · Saldo końcowe: 7 825,01\n2026-10-03 PRZYKŁADOWY KLIENT 7 825,01",
	} {
		r := Text([]string{page})
		if r.Opening.Value != "0" && r.Opening.Value != "0.00" || r.Closing.Value != "7825.01" {
			t.Fatalf("opening %q, closing %q", r.Opening.Value, r.Closing.Value)
		}
		for _, m := range r.Missing() {
			if m == "closing_balance" || m == "opening_balance" {
				t.Fatal("missing", m)
			}
		}
	}
}

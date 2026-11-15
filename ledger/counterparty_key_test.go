package ledger_test

import "testing"

// A counterparty's key drops what changes between charges and keeps its
// letters, in whatever alphabet the bank wrote them.
func TestACounterpartyKeyKeepsLettersOfEveryAlphabet(t *testing.T) {
	db, _, _, _, _ := setup(t)
	for raw, want := range map[string]string{
		"EXAMPLE CAFE #1021 10/14": "example cafe",
		"Example Cafe #1187":       "example cafe",
		"ŻABKA Z4521 WARSZAWA":     "żabka z warszawa",
		"Żabka z4599 Warszawa":     "żabka z warszawa",
		"MÜLLER GmbH & Co.":        "müller gmbh co",
		"ЯНДЕКС*ТАКСИ 7788":        "яндекс такси",
		"カフェ 渋谷店 0412":             "カフェ 渋谷店",
		"Example Cafe — Centre":    "example cafe centre",
		"0412 / 10-14":             "",
	} {
		var got string
		if err := db.Admin.QueryRow(db.Context, "SELECT public.rule_key($1)", raw).Scan(&got); err != nil || got != want {
			t.Errorf("%q has the key %q, want %q (%v)", raw, got, want, err)
		}
	}
	// A key is its own key, as a rule's stored key must be.
	var same bool
	if err := db.Admin.QueryRow(db.Context, "SELECT public.rule_key(public.rule_key($1)) = public.rule_key($1)", "ŻABKA Z4521 — Warszawa").Scan(&same); err != nil || !same {
		t.Fatal("a key isn't stable under the function that made it", err)
	}
}

package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestKindHistorySaysHowEachKindWasRecordedBefore(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		f.tx("acc-checking", "a", "2026-03-02", -4500, "Example Office Supply #1042"),
		f.tx("acc-checking", "b", "2026-04-02", -6100, "EXAMPLE OFFICE SUPPLY #2277"),
		f.tx("acc-checking", "c", "2026-05-02", -5200, "Example Office Supply"),
		f.tx("acc-checking", "d", "2026-06-02", -5000, "Example Office Supply"),
		f.tx("acc-checking", "e", "2026-06-03", -900, "Example Cafe"),
	}, nil)
	page := f.inbox(t, f.s, "")
	keys := map[string]string{}
	for _, v := range page.Items {
		keys[v.Description] = v.KindKey
	}
	supply := keys["Example Office Supply"]
	if supply == "" || keys["Example Office Supply #1042"] != supply || keys["EXAMPLE OFFICE SUPPLY #2277"] != supply {
		t.Fatalf("one supplier's items aren't one kind: %v", keys)
	}
	// Three recorded to office expenses, the fourth reversed afterwards; the
	// cafe is left waiting.
	var reversed string
	for _, v := range page.Items {
		if v.KindKey != supply {
			continue
		}
		amount := v.AmountMinor[1:]
		var posted ledger.BankLinkResult
		value := f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "r-" + v.ID, TransactionID: v.ID, Description: "Office supplies",
			Lines: []ledger.PostLine{{AccountID: f.office.ID, Side: "debit", AmountMinor: amount}, {AccountID: f.checking.ID, Side: "credit", AmountMinor: amount}}})
		if v.Date == "2026-06-02" {
			if json.Unmarshal(value, &posted) != nil || posted.Entry == nil {
				t.Fatalf("no entry: %s", value)
			}
			reversed = posted.Entry.ID
		}
	}
	f.db.Call(t, f.s, "reverse_journal", ledger.ReverseJournal{RequestKey: "undo", EntryID: reversed, AccountingDate: "2026-06-05", Reason: "Example mistake"})

	got, err := f.owner.KindHistory(f.db.Context, f.book.ID, []string{supply, keys["Example Cafe"]})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].KindKey != supply || len(got[0].Accounts) != 1 {
		t.Fatalf("history: %+v", got)
	}
	past := got[0].Accounts[0]
	if past.AccountID != f.office.ID || past.Count != 3 || past.MinMinor != "4500" || past.MaxMinor != "6100" || past.LastOn != "2026-05-02" {
		t.Fatalf("history of the supplier: %+v", past)
	}
	if _, err := f.owner.KindHistory(f.db.Context, f.book.ID, make([]string, ledger.MaxKindHistoryKeys+1)); err == nil {
		t.Fatal("too many kinds were read")
	}
}

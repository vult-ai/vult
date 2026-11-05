package ledger_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestSingleAccountBalancePreservesExactDatesReversalsAndScope(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	unused := db.Account(t, scope, "1300", "asset")
	var opening ledger.Entry
	if err := json.Unmarshal(db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "opening")), &opening); err != nil {
		t.Fatal(err)
	}
	later := post(debit.ID, credit.ID, "later")
	later.AccountingDate = "2026-01-30"
	for i := range later.Lines {
		later.Lines[i].AmountMinor = "500"
	}
	db.Call(t, scope, "post_journal", later)
	db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: opening.ID, AccountingDate: "2026-02-01", Reason: "Example correction"})
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: debit.ID, ExpectedRevision: 1, Code: "1010", Name: "Example renamed account"})
	reader, err := db.Owner.CreateConnection(db.Context, book.ID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	read := ledger.Scope{TenantID: reader.TenantID, BookID: book.ID, ActorID: reader.ActorID, ConnectionID: reader.ID, Access: "read"}
	for _, test := range []struct {
		through, debit, credit string
	}{{"2026-01-02", "0", "0"}, {"2026-01-03", "1250", "-1250"}, {"2026-01-29", "1250", "-1250"}, {"2026-01-30", "1750", "-1750"}, {"2026-02-01", "500", "-500"}, {"", "500", "-500"}} {
		t.Run(test.through, func(t *testing.T) {
			for _, expected := range []struct{ id, amount string }{{debit.ID, test.debit}, {credit.ID, test.credit}, {unused.ID, "0"}} {
				var result ledger.Balance
				if err := json.Unmarshal(db.Call(t, read, "get_balance", ledger.AccountQuery{AccountID: expected.id, Through: test.through}), &result); err != nil {
					t.Fatal(err)
				}
				if result.AccountID != expected.id || result.DebitPositiveMinor != expected.amount {
					t.Fatal("single balance differs from independently calculated amount", result)
				}
				if expected.id == debit.ID && (result.Code != "1010" || result.Name != "Example renamed account") {
					t.Fatal("single balance lost current account identity")
				}
			}
		})
	}
	_, other := db.Book(t, "Other Book")
	foreign := db.Account(t, other, "9999", "asset")
	for _, id := range []string{foreign.ID, ledger.ID()} {
		if _, err := db.Store.Call(db.Context, read, "get_balance", value(ledger.AccountQuery{AccountID: id})); !errors.Is(err, ledger.ErrNotFound) {
			t.Fatal("foreign or absent account was disclosed", err)
		}
	}
	if _, err := db.Store.Call(db.Context, read, "get_balance", value(ledger.AccountQuery{AccountID: debit.ID, Through: "2026-02-30"})); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("invalid date was accepted", err)
	}
}

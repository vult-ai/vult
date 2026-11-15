package ledger_test

import (
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// The owner adds a statement file read in plain code. Its lines join the
// same transactions the bank reported, the rest are added, and without
// balances in the file the statement starts from the Book's own balance.
func TestOwnerAddsAStatementFileToTheInbox(t *testing.T) {
	f := newBankFixture(t)
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "cafe", "2026-03-02", -1850, "EXAMPLE CAFE 0042")}, nil)
	doc := statementDocument(t, f.db, f.s, "march")
	out, err := f.owner.ImportStatementFile(f.db.Context, f.book.ID, f.actor, ledger.StatementFile{AccountID: f.checking.ID, DocumentID: doc,
		PeriodStart: "2026-03-02", PeriodEnd: "2026-03-05", Rows: []ledger.StatementRowInput{
			{RowNumber: 2, Date: "2026-03-02", AmountMinor: "-1850", Description: "EXAMPLE CAFE 0042", Raw: "03/02/2026,EXAMPLE CAFE 0042,-18.50"},
			{RowNumber: 3, Date: "2026-03-05", AmountMinor: "120000", Description: "EXAMPLE CLIENT CO", Raw: "03/05/2026,EXAMPLE CLIENT CO,1200.00"},
		}})
	succeeded(t, err)
	_ = out
	page := f.inbox(t, f.s, "all")
	if len(page.Items) != 2 {
		t.Fatalf("inbox = %+v", page.Items)
	}
	cafe := byDescription(page, "EXAMPLE CAFE 0042")
	if cafe.StatementID == "" || cafe.Source != "bank" {
		t.Fatalf("the bank item didn't join its statement line: %+v", cafe)
	}
	statements, err := f.owner.Statements(f.db.Context, f.book.ID, f.checking.ID)
	succeeded(t, err)
	if len(statements) != 1 || statements[0].OpeningMinor != "0" || statements[0].ClosingMinor != "118150" || statements[0].EvidenceState != "incomplete" {
		t.Fatalf("statements = %+v", statements)
	}
}

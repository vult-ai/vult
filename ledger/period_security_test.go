package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestPreviewSettingNeverAuthorizesOwnerPeriodDecisions(t *testing.T) {
	db := testutil.New(t)
	book, writer, accounts := chartBook(t, db, "business-v1")
	proposer := connectionScope(t, db, book.ID, "propose")
	var period ledger.AccountingPeriod
	if json.Unmarshal(db.Call(t, writer, "close_period", ledger.ClosePeriod{RequestKey: "clean", From: "2026-02-01", Through: "2026-02-28"}), &period) != nil {
		t.Fatal("invalid close")
	}
	journal(t, db, writer, "open-items", "2026-01-12", accounts["6200"].ID, accounts["1110"].ID, "100")
	for _, scope := range []ledger.Scope{writer, proposer} {
		t.Run(scope.Access, func(t *testing.T) {
			for _, operation := range []string{"reopen_period", "close_period"} {
				err := write(db, db.Runtime, scope, func(tx pgx.Tx) error {
					if _, err := tx.Exec(db.Context, "SELECT set_config('vult.proposing','preview',true)"); err != nil {
						return err
					}
					r, err := receipt(db, tx, scope, operation, operation+"-"+scope.Access)
					if err != nil {
						return err
					}
					id, revision, event := period.ID, 2, "reopened"
					if operation == "close_period" {
						id, revision, event = ledger.ID(), 1, "closed"
						if _, err := tx.Exec(db.Context, `INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date)
 VALUES($1,$2,$3,'2026-01-01','2026-01-31')`, scope.TenantID, scope.BookID, id); err != nil {
							return err
						}
					}
					_, err = tx.Exec(db.Context, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,'decision',$6,'Example reason',$7)`, scope.TenantID, scope.BookID, id, revision, event, scope.ActorID, r)
					return err
				})
				refused(t, err, "only the owner")
			}
		})
	}
	if periods := periodList(t, db, writer); len(periods) != 1 || periods[0].Status != "closed" || periods[0].Revision != 1 {
		t.Fatal("a database preview committed an owner-only decision", periods)
	}
}

func TestPeriodPreviewCommitsOnlyAReadModel(t *testing.T) {
	db := testutil.New(t)
	book, writer, _ := chartBook(t, db, "business-v1")
	proposer := connectionScope(t, db, book.ID, "propose")
	var period ledger.AccountingPeriod
	if json.Unmarshal(db.Call(t, writer, "close_period", ledger.ClosePeriod{RequestKey: "clean", From: "2026-02-01", Through: "2026-02-28"}), &period) != nil {
		t.Fatal("invalid close")
	}
	args := value(ledger.ReopenPeriod{RequestKey: "reopen", PeriodID: period.ID, ExpectedRevision: 1, Reason: "Example correction"})
	var preview ledger.OperationPeriodChange
	succeeded(t, write(db, db.Runtime, proposer, func(tx pgx.Tx) error {
		var data []byte
		if err := tx.QueryRow(db.Context, "SELECT public.accounting_period_preview($1,$2,'reopen_period',$3::jsonb)", proposer.TenantID, proposer.BookID, string(args)).Scan(&data); err != nil {
			return err
		}
		return json.Unmarshal(data, &preview)
	}))
	if preview.Change != "reopen" || preview.PeriodID != period.ID {
		t.Fatal("the read model omitted the proposed decision", preview)
	}
	if periods := periodList(t, db, writer); len(periods) != 1 || periods[0].Revision != 1 || periods[0].Status != "closed" {
		t.Fatal("committing the read model changed the period", periods)
	}
	var receipts int
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.requests WHERE operation='reopen_period'").Scan(&receipts))
	if receipts != 0 {
		t.Fatal("the read model recorded an operation receipt")
	}
}

func TestCombinedMatchProposalKeepsTheExactTotal(t *testing.T) {
	db := testutil.New(t)
	book, writer, accounts := chartBook(t, db, "business-v1")
	bank, equity := accounts["1110"].ID, accounts["3900"].ID
	const amount = "5000000000000000000"
	first := journal(t, db, writer, "first-deposit", "2026-01-12", bank, equity, amount)
	// A separate withdrawal keeps both accounts' running balances in range.
	// It is not a reversal and remains outstanding on this statement.
	journal(t, db, writer, "withdrawal", "2026-01-13", equity, bank, amount)
	second := journal(t, db, writer, "second-deposit", "2026-01-14", bank, equity, amount)
	statement := importStatement(t, db, writer, ledger.ImportStatement{RequestKey: "statement", AccountID: bank,
		DocumentID: statementDocument(t, db, writer, "large-match"), PeriodStart: "2026-01-01", PeriodEnd: "2026-01-31",
		OpeningMinor: "-5000000000000000000", ClosingMinor: "5000000000000000000", Rows: []ledger.StatementRowInput{
			row(1, "2026-01-12", amount, "Example first deposit"), row(2, "2026-01-14", amount, "Example second deposit"),
		}})
	match := ledger.MatchStatement{RequestKey: "match", Lines: []ledger.StatementMatchPart{
		{LineID: entryLine(t, db, writer, first, bank)}, {LineID: entryLine(t, db, writer, second, bank)},
	}}
	for _, r := range reconciliation(t, db, writer, statement.ID).Rows {
		match.Rows = append(match.Rows, ledger.StatementMatchPart{RowID: r.ID})
	}
	proposer := connectionScope(t, db, book.ID, "propose")
	proposal := prepareOperation(t, db, proposer, "prepare-match", "match_statement", match)
	if proposal.Preview.StatementChange == nil || proposal.Preview.StatementChange.AmountMinor != "10000000000000000000" {
		t.Fatal("the owner's approval must show the exact combined amount", proposal.Preview.StatementChange)
	}
}

func TestMatchProposalUsesTheStatementCurrencyAndPrecision(t *testing.T) {
	for _, tc := range []struct {
		name, currency string
		bookScale      int
	}{{"foreign currency", "EUR", 2}, {"same currency different precision", "USD", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.New(t)
			book, err := db.Owner.CreateBook(db.Context, "Example Book", "USD", tc.bookScale)
			succeeded(t, err)
			writer := connectionScope(t, db, book.ID, "write")
			bank := measuredAccount(t, db, writer, "1000", tc.currency, 2, true)
			equity := db.Account(t, writer, "3000", "equity")
			var entry ledger.Entry
			if json.Unmarshal(db.Call(t, writer, "post_journal", measuredPost(bank, equity.ID, "deposit", "100", "1000")), &entry) != nil {
				t.Fatal("invalid entry")
			}
			statement := importStatement(t, db, writer, ledger.ImportStatement{RequestKey: "statement", AccountID: bank.ID,
				DocumentID: statementDocument(t, db, writer, "native-match"), PeriodStart: "2026-01-01", PeriodEnd: "2026-01-31",
				OpeningMinor: "0", ClosingMinor: "100", Rows: []ledger.StatementRowInput{row(1, "2026-01-03", "100", "Example deposit")}})
			rows := reconciliation(t, db, writer, statement.ID).Rows
			proposer := connectionScope(t, db, book.ID, "propose")
			proposal := prepareOperation(t, db, proposer, "prepare-match", "match_statement", ledger.MatchStatement{RequestKey: "match",
				Rows: []ledger.StatementMatchPart{{RowID: rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, writer, entry, bank.ID)}}})
			if proposal.Preview.Currency != tc.currency || proposal.Preview.Scale != 2 || proposal.Preview.StatementChange == nil || proposal.Preview.StatementChange.AmountMinor != "100" {
				t.Fatal("the approval must format the matched amount in its statement unit", proposal.Preview)
			}
		})
	}
}

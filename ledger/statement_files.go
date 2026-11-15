package ledger

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// StatementFile is a statement Vult read from a structured file in plain
// code, for the owner to add to an account. Its lines reach the bank
// activity inbox like any statement's.
type StatementFile struct {
	AccountID   string
	DocumentID  string
	PeriodStart string
	PeriodEnd   string
	Rows        []StatementRowInput
	// Balances says the file gave its own opening and closing balances.
	Balances     bool
	OpeningMinor string
	ClosingMinor string
}

// ImportStatementFile records a read file as a statement for the owner.
// When the file gives no balances, the opening is the Book's own balance
// the day before the file starts, the closing follows from the lines, and
// the statement is marked incomplete so no one takes it as the bank's.
func (a TenantAdmin) ImportStatementFile(ctx context.Context, book, actor string, f StatementFile) (json.RawMessage, error) {
	if !ValidID(actor) || !ValidID(f.AccountID) || !ValidID(f.DocumentID) || !ValidDate(f.PeriodStart) || !ValidDate(f.PeriodEnd) {
		return nil, ErrInvalid
	}
	var result json.RawMessage
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_decision_allowed($1,$2)", scope.TenantID, scope.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2), set_config('vult.statement_file','on',true)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		q := ImportStatement{RequestKey: "statement-file-" + f.DocumentID, AccountID: f.AccountID, DocumentID: f.DocumentID,
			PeriodStart: f.PeriodStart, PeriodEnd: f.PeriodEnd, OpeningMinor: f.OpeningMinor, ClosingMinor: f.ClosingMinor, Rows: f.Rows}
		if !f.Balances {
			start, _ := time.Parse(time.DateOnly, f.PeriodStart)
			var opening int64
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN (l.side='debit') = (a.class='asset') THEN l.amount_minor ELSE -l.amount_minor END),0)
 FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.account_id=$3 AND e.accounting_date <= $4::date`,
				scope.TenantID, scope.BookID, f.AccountID, start.AddDate(0, 0, -1).Format(time.DateOnly)).Scan(&opening); err != nil {
				return dbError(err)
			}
			closing := opening
			for _, r := range f.Rows {
				n, _ := strconv.ParseInt(r.AmountMinor, 10, 64)
				closing += n
			}
			q.OpeningMinor, q.ClosingMinor = strconv.FormatInt(opening, 10), strconv.FormatInt(closing, 10)
			q.EvidenceState = "incomplete"
			q.Note = "The file gives no balances. The opening is this Book's balance the day before the file starts, and the closing adds the file's lines."
		}
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		result, err = dispatch(ctx, tx, scope, "import_statement", raw, nil)
		return err
	})
	return result, err
}

// StatementAccount says whether an account takes statements, as a bank
// account (cash) or a card, and its decimal places.
func (a TenantAdmin) StatementAccount(ctx context.Context, book, account string) (kind string, scale int, err error) {
	if !ValidID(account) {
		return "", 0, ErrInvalid
	}
	err = a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, b Book) error {
		return dbError(tx.QueryRow(ctx, `SELECT coalesce(a.role,''),coalesce(a.native_scale,$4) FROM public.accounts a WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.id=$3`,
			s.TenantID, s.BookID, account, b.Scale).Scan(&kind, &scale))
	})
	if err == nil && kind != "cash" && kind != "card" {
		return "", 0, &RefusedError{Reason: "Statements go to a bank account or a card account. Choose one of those."}
	}
	return kind, scale, err
}

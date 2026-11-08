package ledger

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type accountHistoryLine struct {
	ID, EntryID, AccountID, Date, EconomicDate, RecordedAt string
	Description, Memo, Side, Amount                        string
	Sequence                                               int64
	Position                                               int
}

type accountHistory struct {
	State    RecordedState
	Book     Book
	AsOf     string
	Accounts []AccountPositionRead
	Lines    []accountHistoryLine
	Revision string
}

func accountIDs(ids []string) ([]string, error) {
	if len(ids) > reportAccountLimit {
		return nil, ErrLimit
	}
	if ids == nil {
		return nil, nil
	}
	if len(ids) == 0 {
		return nil, ErrInvalid
	}
	out := append([]string{}, ids...)
	sort.Strings(out)
	for i, id := range out {
		if !ValidID(id) || i > 0 && out[i-1] == id {
			return nil, ErrInvalid
		}
	}
	return out, nil
}

// readAccountHistory captures only an explicit account population. Both queries
// use the same owner transaction and recorded-time cutoff; neither pages totals.
func readAccountHistory(ctx context.Context, tx pgx.Tx, scope Scope, book Book, asOf string, ids []string, detail bool) (accountHistory, error) {
	state, err := captureReadState(ctx, tx, scope, book, recordedReadScope{})
	if err != nil {
		return accountHistory{}, err
	}
	text := state.StateKnowledgeCutoff
	positions, err := readAccountPositions(ctx, tx, scope, positionReadScope{Through: asOf, Cutoff: text, AccountIDs: ids, Limit: reportAccountLimit})
	if err != nil {
		return accountHistory{}, err
	}
	if ids != nil && len(positions) != len(ids) {
		return accountHistory{}, ErrNotFound
	}
	in := accountHistory{State: state.RecordedState, Book: book, AsOf: asOf, Accounts: positions}
	in.Lines, err = readAccountHistoryLines(ctx, tx, scope, asOf, text, ids, detail)
	if err != nil {
		return accountHistory{}, err
	}
	if detail {
		in.Revision = archiveHash(raw(struct {
			Book     Book
			AsOf     string
			Accounts []AccountPositionRead
			Lines    []accountHistoryLine
		}{book, asOf, positions, in.Lines}))
	}
	return in, nil
}

func readAccountHistoryLines(ctx context.Context, tx pgx.Tx, scope Scope, asOf, cutoff string, ids []string, detail bool) ([]accountHistoryLine, error) {
	projection := `SELECT DISTINCT l.account_id::text,e.id::text,e.accounting_date::text`
	order := ` ORDER BY l.account_id::text,e.id::text,e.accounting_date::text`
	if detail {
		projection = `SELECT l.id::text,e.id::text,l.account_id::text,e.accounting_date::text,
 e.economic_date::text,e.recorded_at,e.description,l.memo,l.side,l.amount_minor::text,e.sequence,l.position`
		order = ` ORDER BY e.accounting_date,e.sequence,l.position`
	}
	rows, err := tx.Query(ctx, projection+` FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND e.accounting_date<=$3::date AND e.recorded_at<=$4::timestamptz
 AND ($5::uuid[] IS NULL OR l.account_id=ANY($5::uuid[]))`+order+` LIMIT 100001`, scope.TenantID, scope.BookID, asOf, cutoff, ids)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	lines := []accountHistoryLine{}
	for rows.Next() {
		var line accountHistoryLine
		if detail {
			var recorded time.Time
			err = rows.Scan(&line.ID, &line.EntryID, &line.AccountID, &line.Date, &line.EconomicDate, &recorded, &line.Description, &line.Memo, &line.Side, &line.Amount, &line.Sequence, &line.Position)
			line.RecordedAt = recorded.UTC().Format(time.RFC3339Nano)
		} else {
			err = rows.Scan(&line.AccountID, &line.EntryID, &line.Date)
		}
		if err != nil {
			return nil, dbError(err)
		}
		lines = append(lines, line)
		if len(lines) > reportPostingLimit {
			return nil, ErrLimit
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	return lines, nil
}

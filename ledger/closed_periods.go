package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ClosedPeriod is a date range of one Book that accepts no further postings.
// Closing is permanent: the database refuses updates, deletes and overlaps.
type ClosedPeriod struct {
	ID       string    `json:"id"`
	From     string    `json:"from"`
	Through  string    `json:"through"`
	ClosedAt time.Time `json:"closed_at"`
}

const maxClosedPeriods = 1000

func readClosedPeriods(ctx context.Context, tx pgx.Tx, scope Scope) ([]ClosedPeriod, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,start_date::text,end_date::text,closed_at FROM public.closed_periods
 WHERE tenant_id=$1 AND book_id=$2 ORDER BY start_date,id LIMIT $3`, scope.TenantID, scope.BookID, maxClosedPeriods+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []ClosedPeriod{}
	for rows.Next() {
		var p ClosedPeriod
		if err = rows.Scan(&p.ID, &p.From, &p.Through, &p.ClosedAt); err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, dbError(err)
	}
	if len(out) > maxClosedPeriods {
		return nil, ErrLimit
	}
	return out, nil
}

// ClosedPeriods lists a Book's closed periods, oldest first.
func (a TenantAdmin) ClosedPeriods(ctx context.Context, book string) ([]ClosedPeriod, error) {
	var out []ClosedPeriod
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = readClosedPeriods(ctx, tx, scope)
		return err
	})
	return out, err
}

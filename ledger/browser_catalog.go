package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func readScopedCatalog(ctx context.Context, tx pgx.Tx, scope Scope) ([]Account, error) {
	rows, err := tx.Query(ctx, "SELECT "+accountColumns+" FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 ORDER BY id LIMIT $3", scope.TenantID, scope.BookID, reportAccountLimit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, a)
		if len(out) > reportAccountLimit {
			return nil, ErrLimit
		}
	}
	return out, dbError(rows.Err())
}

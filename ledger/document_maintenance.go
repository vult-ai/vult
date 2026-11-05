package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/documents"
)

// ExpiredDocumentBooks selects a bounded operator maintenance page, ordered by
// each Book's oldest unused original. Selecting a Book does not authorize a
// deletion; ExpireBookDocuments rechecks eligibility under its write lock.
func (a Admin) ExpiredDocumentBooks(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 20 {
		return nil, ErrInvalid
	}
	var operator bool
	if err := a.DB.QueryRow(ctx, "SELECT public.ledger_import_owner() AND current_user=session_user").Scan(&operator); err != nil {
		return nil, dbError(err)
	}
	if !operator {
		return nil, ErrDenied
	}
	rows, err := a.DB.Query(ctx, `SELECT b.id::text FROM public.books b CROSS JOIN LATERAL(
 SELECT d.expires_at FROM public.documents d WHERE d.tenant_id=b.tenant_id AND d.book_id=b.id
 AND d.used_at IS NULL AND d.state<>'expired' ORDER BY d.expires_at,d.id LIMIT 1) oldest
 WHERE oldest.expires_at<=clock_timestamp() ORDER BY oldest.expires_at,b.id LIMIT $1`, limit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	books := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, dbError(err)
		}
		books = append(books, id)
	}
	return books, dbError(rows.Err())
}

// ExpireBookDocuments is trusted operator maintenance, never tenant-owner or
// agent authority. It removes at most limit already-expired unused originals
// and holds the same Book write lock as linking, posting and backup exclusion.
func (a Admin) ExpireBookDocuments(ctx context.Context, book string, storage documents.Storage, limit int) (int, error) {
	if !ValidID(book) || storage == nil || limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	tx, err := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, ErrUncertain
	}
	defer tx.Rollback(context.Background())
	var tenant string
	if err = tx.QueryRow(ctx, "SELECT tenant_id::text FROM public.books WHERE id=$1", book).Scan(&tenant); err != nil {
		return 0, dbError(err)
	}
	scope := Scope{TenantID: tenant, BookID: book}
	if err = setScope(ctx, tx, scope); err != nil {
		return 0, dbError(err)
	}
	if _, err = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", tenant, book); err != nil {
		return 0, dbError(err)
	}
	count, err := expireDocumentBatch(ctx, tx, scope, storage, limit)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, ErrUncertain
	}
	return count, nil
}

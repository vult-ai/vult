package access

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

func (s *Service) BookTenant(ctx context.Context, raw, book string) (string, error) {
	return ownerResourceTenant(ctx, s.auth, raw, s.origin, "book", book)
}

func (s *Service) ConnectionTenant(ctx context.Context, raw, id string) (string, error) {
	return ownerResourceTenant(ctx, s.auth, raw, s.origin, "connection", id)
}

func ownerResourceTenant(ctx context.Context, pool *pgxpool.Pool, raw, origin, kind, id string) (string, error) {
	if !validToken(raw) {
		return "", ErrDenied
	}
	if !ledger.ValidID(id) {
		return "", ledger.ErrInvalid
	}
	var tenant *string
	var owner bool
	err := pool.QueryRow(ctx, `SELECT tenant_id::text,is_owner FROM access.owner_resource_tenant($1,$2,$3,$4)`,
		Hash(raw), origin, kind, id).Scan(&tenant, &owner)
	if err != nil || !owner {
		return "", ErrDenied
	}
	if tenant == nil {
		return "", ledger.ErrNotFound
	}
	return *tenant, nil
}

// WithBookExportSnapshot stages a repeatable Book read without locking the
// identity, session or membership rows. The caller must check BookTenant in a
// fresh transaction before releasing any staged bytes.
func (s *Service) WithBookExportSnapshot(ctx context.Context, raw, tenant string, read func(ledger.TenantAdmin) error) error {
	return readOwnerSnapshot(ctx, s.owners, s.origin, raw, tenant, pgx.ReadOnly, read)
}

func readOwnerSnapshot(ctx context.Context, pool *pgxpool.Pool, origin, raw, tenant string, mode pgx.TxAccessMode, read func(ledger.TenantAdmin) error) error {
	if !validToken(raw) || !ledger.ValidID(tenant) || read == nil {
		return ErrDenied
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: mode})
	if err != nil {
		return ErrDenied
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true)", Hash(raw), origin, tenant); err != nil {
		return ErrDenied
	}
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT access.owner_allowed($1)", tenant).Scan(&allowed); err != nil || !allowed {
		return ErrDenied
	}
	owner, err := ledger.TenantTransaction(tx, tenant)
	if err != nil {
		return err
	}
	if err = read(owner); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

package ledger

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
)

//go:embed migrations/0001_ledger.sql
var migrations embed.FS

func SchemaSHA256() string {
	b, e := migrations.ReadFile("migrations/0001_ledger.sql")
	if e != nil {
		panic(e)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Apply initializes an empty database, or verifies its existing exact receipt.
func Apply(ctx context.Context, admin *pgxpool.Pool, runtimeRole string) error {
	if runtimeRole == "" || strings.ContainsAny(runtimeRole, "\x00") {
		return ErrInvalid
	}
	tx, e := admin.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305013)"); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('public.schema_receipts') IS NOT NULL").Scan(&exists); e != nil {
		return e
	}
	if exists {
		var sum string
		var n int
		if e = tx.QueryRow(ctx, "SELECT count(*),coalesce(min(sha256),'') FROM public.schema_receipts").Scan(&n, &sum); e != nil || n != 1 || sum != SchemaSHA256() {
			return errors.New("installed schema does not match this release")
		}
	} else {
		var occupied bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public')").Scan(&occupied); e != nil || occupied {
			return errors.New("a fresh database is required")
		}
		b, _ := migrations.ReadFile("migrations/0001_ledger.sql")
		if _, e = tx.Exec(ctx, string(b)); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES('0001_ledger.sql',$1)", SchemaSHA256()); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "REVOKE ALL ON public.schema_receipts FROM PUBLIC"); e != nil {
			return e
		}
	}
	role := pgx.Identifier{runtimeRole}.Sanitize()
	statements := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT USAGE ON SCHEMA public TO " + role,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts TO " + role,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + role,
		"GRANT UPDATE(write_epoch) ON public.books TO " + role,
		"GRANT UPDATE(id) ON public.connections TO " + role,
	}
	for _, name := range []string{"ledger_context(uuid,uuid)", "ledger_allowed(uuid,uuid,boolean)", "ledger_lock(uuid,uuid)", "ledger_import_owner()"} {
		statements = append(statements, "GRANT EXECUTE ON FUNCTION public."+name+" TO "+role)
	}
	for _, s := range statements {
		if _, e = tx.Exec(ctx, s); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	var name, sum string
	var n int
	e := pool.QueryRow(ctx, "SELECT count(*),coalesce(min(filename),''),coalesce(min(sha256),'') FROM public.schema_receipts").Scan(&n, &name, &sum)
	if e != nil || n != 1 || name != "0001_ledger.sql" || sum != SchemaSHA256() {
		return fmt.Errorf("schema verification failed")
	}
	return nil
}

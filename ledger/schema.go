package ledger

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

func SchemaSHA256() string {
	b, e := migrations.ReadFile("migrations/0001_ledger.sql")
	if e != nil {
		panic(e)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func migrationNames() []string {
	entries, e := migrations.ReadDir("migrations")
	if e != nil {
		panic(e)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func migrationSum(name string) string {
	b, _ := migrations.ReadFile("migrations/" + name)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Apply verifies installed receipts and applies only missing forward migrations.
func Apply(ctx context.Context, admin *pgxpool.Pool, runtimeRole string) error {
	return apply(ctx, admin, runtimeRole, false)
}

// Upgrade applies forward migrations only to a recognized, initialized ledger.
// Missing or invalid schema receipts require recovery instead of initialization.
func Upgrade(ctx context.Context, admin *pgxpool.Pool, runtimeRole string) error {
	return apply(ctx, admin, runtimeRole, true)
}

func apply(ctx context.Context, admin *pgxpool.Pool, runtimeRole string, initialized bool) error {
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
		if e = verifyReceipts(ctx, tx, false); e != nil {
			return e
		}
	} else {
		if initialized {
			return errors.New("stored schema is missing; recovery is required")
		}
		var occupied bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public')").Scan(&occupied); e != nil || occupied {
			return errors.New("a fresh database is required")
		}
		if _, e = tx.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "REVOKE ALL ON public.schema_receipts FROM PUBLIC"); e != nil {
			return e
		}
	}
	for _, name := range migrationNames() {
		var applied bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.schema_receipts WHERE filename=$1)", name).Scan(&applied); e != nil {
			return e
		}
		if applied {
			continue
		}
		b, _ := migrations.ReadFile("migrations/" + name)
		if _, e = tx.Exec(ctx, string(b)); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, migrationSum(name)); e != nil {
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
		"GRANT SELECT,INSERT ON public.documents,public.document_uploads TO " + role,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at) ON public.documents TO " + role,
		"GRANT UPDATE(used_at) ON public.documents TO " + role,
		"GRANT SELECT,INSERT ON public.source_citations TO " + role,
		"GRANT SELECT,INSERT ON public.evidence_links TO " + role,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_validate_balances(uuid,uuid,uuid) TO " + role,
		"GRANT USAGE ON SCHEMA access TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_write_allowed(uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.delegation_allowed(uuid,boolean),access.require_agent(text,text,uuid,uuid,uuid,uuid,text) TO " + role,
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
	return verifyReceipts(ctx, pool, true)
}

type receiptReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func verifyReceipts(ctx context.Context, db receiptReader, complete bool) error {
	rows, e := db.Query(ctx, "SELECT filename,sha256 FROM public.schema_receipts ORDER BY filename")
	if e != nil {
		return errors.New("schema verification failed")
	}
	defer rows.Close()
	expected := migrationNames()
	n := 0
	for rows.Next() {
		var name, sum string
		if rows.Scan(&name, &sum) != nil || n >= len(expected) || name != expected[n] || sum != migrationSum(name) {
			return errors.New("installed schema does not match this release")
		}
		n++
	}
	if rows.Err() != nil || n == 0 || complete && n != len(expected) {
		return errors.New("schema verification failed")
	}
	return nil
}

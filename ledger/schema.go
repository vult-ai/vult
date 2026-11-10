package ledger

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

const recordedBookMigration = "0004_recorded_book.sql"
const foundationsMigration = "0007_accounting_foundations.sql"

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
	names := migrationNames()
	applied := 0
	if exists {
		if applied, e = verifyReceipts(ctx, tx, names, false); e != nil {
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
	for _, name := range names[applied:] {
		b, _ := migrations.ReadFile("migrations/" + name)
		if _, e = tx.Exec(ctx, string(b)); e != nil {
			var p *pgconn.PgError
			if errors.As(e, &p) && p.Code == "23514" {
				return &UpgradeRefusal{Message: p.Message, Hint: p.Hint}
			}
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, migrationSum(name)); e != nil {
			return e
		}
	}
	role := pgx.Identifier{runtimeRole}.Sanitize()
	statements := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT USAGE ON SCHEMA public TO " + role,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts,public.book_revisions TO " + role,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + role,
		"GRANT UPDATE(write_epoch) ON public.books TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_set_book_logo(uuid,uuid,text,text,text,bytea) TO " + role,
		"GRANT UPDATE(id) ON public.connections TO " + role,
		"GRANT SELECT,INSERT ON public.documents,public.document_uploads TO " + role,
		"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at) ON public.documents TO " + role,
		"GRANT UPDATE(used_at) ON public.documents TO " + role,
		"GRANT SELECT,INSERT ON public.source_citations TO " + role,
		"GRANT SELECT,INSERT ON public.evidence_links TO " + role,
		"GRANT SELECT ON public.search_words,public.search_totals TO " + role,
		"GRANT EXECUTE ON FUNCTION public.search_text(text),public.search_tokens(text) TO " + role,
		"GRANT SELECT,INSERT ON public.operation_proposals,public.operation_consumptions TO " + role,
		"GRANT SELECT ON public.operation_decisions TO " + role,
		"GRANT EXECUTE ON FUNCTION access.operation_owner_allowed(uuid,uuid,uuid,bigint),access.operation_executor_active(uuid,uuid,uuid,uuid),access.hold_operation_owner(uuid,uuid,uuid,bigint) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.operation_dependencies(uuid,uuid,text,jsonb,uuid),public.operation_dependencies_settled(jsonb) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_posting_consumer(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_validate_balances(uuid,uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) TO " + role,
		"GRANT USAGE ON SCHEMA access TO " + role,
		// Accounting foundations. Row security applies each connection's
		// level; the period guard keeps reopening and closing with open
		// review items for the owner.
		"GRANT SELECT,INSERT ON public.payees,public.payee_revisions,public.entry_payees,public.statements,public.statement_rows,public.statement_duplicates,public.statement_matches,public.statement_match_parts,public.statement_match_revocations,public.statement_adjustments,public.statement_reviews,public.accounting_periods,public.accounting_period_events TO " + role,
		"GRANT EXECUTE ON FUNCTION public.ledger_restoring(uuid,uuid),public.ledger_role_fits(text,text),public.ledger_period_closed(uuid,uuid,date),public.statement_line_amount(uuid,uuid,uuid,text,integer),public.statement_row_check(uuid,uuid,uuid),public.statement_line_check(uuid,uuid,uuid,text,integer),public.ledger_owner_decision(uuid,uuid),public.period_open_items(uuid,uuid,date,date),public.statement_entry_reversed(uuid,uuid,uuid),public.ledger_written_here(xid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.statement_row_coverages(uuid,uuid,uuid,timestamptz),public.statement_explained(uuid,uuid,uuid,timestamptz),public.statement_current_reconciled(uuid,uuid,uuid),public.statement_line_allocated(uuid,uuid,uuid,text,integer,date,uuid[]),public.statement_line_allocations(uuid,uuid,uuid[],text,integer,date,uuid[]) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_decision_allowed(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION public.accounting_period_preview(uuid,uuid,text,jsonb) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.owner_write_allowed(uuid),access.owner_document_allowed(uuid,uuid) TO " + role,
		"GRANT EXECUTE ON FUNCTION access.delegation_allowed(uuid,boolean),access.require_agent(text,text,uuid,uuid,uuid,uuid,text) TO " + role,
	}
	for _, name := range []string{"ledger_context(uuid,uuid)", "ledger_allowed(uuid,uuid,boolean)", "ledger_lock(uuid,uuid)", "ledger_import_owner()"} {
		statements = append(statements, "GRANT EXECUTE ON FUNCTION public."+name+" TO "+role)
	}
	if _, e = tx.Exec(ctx, strings.Join(statements, ";\n")); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// UpgradeRefusal is a schema change the database declined on purpose
// because stored records don't fit it yet. Nothing was changed; the hint says
// what to change before upgrading again.
type UpgradeRefusal struct{ Message, Hint string }

func (e *UpgradeRefusal) Error() string {
	if e.Hint == "" {
		return "the upgrade was refused and nothing was changed: " + e.Message
	}
	return "the upgrade was refused and nothing was changed: " + e.Message + ". " + e.Hint
}

func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := verifyReceipts(ctx, pool, migrationNames(), true)
	return err
}

type receiptReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func verifyReceipts(ctx context.Context, db receiptReader, expected []string, complete bool) (int, error) {
	rows, e := db.Query(ctx, "SELECT filename,sha256 FROM public.schema_receipts ORDER BY filename")
	if e != nil {
		return 0, errors.New("schema verification failed")
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, sum string
		if rows.Scan(&name, &sum) != nil || n >= len(expected) || name != expected[n] || sum != migrationSum(name) {
			return 0, errors.New("installed schema does not match this release")
		}
		n++
	}
	if rows.Err() != nil || n == 0 || complete && n != len(expected) {
		return 0, errors.New("schema verification failed")
	}
	return n, nil
}

package ledger_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// releasedThrough builds a database as a release applied it: 0.7.0's
// migrations, then 0.8's steps one by one through last ("" for none), each
// with its receipt, then runs seed on it.
func releasedThrough(t *testing.T, last string, seed func(context.Context, *pgxpool.Pool) error) *testutil.Database {
	t.Helper()
	return testutil.NewWithInitializer(t, func(ctx context.Context, pool *pgxpool.Pool, role string) error {
		if _, err := pool.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp()); REVOKE ALL ON public.schema_receipts FROM PUBLIC"); err != nil {
			return err
		}
		var files []string
		base, _ := filepath.Glob("migrations/0*.sql")
		for _, f := range base {
			if filepath.Base(f) < "0008" {
				files = append(files, f)
			}
		}
		steps, _ := filepath.Glob("migrations/bridge/*.sql")
		for _, f := range steps {
			if last != "" && filepath.Base(f) <= last {
				files = append(files, f)
			}
		}
		sort.Slice(files, func(i, j int) bool { return filepath.Base(files[i]) < filepath.Base(files[j]) })
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if _, err = pool.Exec(ctx, string(data)); err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			if _, err = pool.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", filepath.Base(f), hex.EncodeToString(sum[:])); err != nil {
				return err
			}
		}
		if seed != nil {
			return seed(ctx, pool)
		}
		return nil
	})
}

// catalog is everything the schema is made of, in one comparable text:
// tables and columns, constraints, indexes, functions with their bodies and
// settings, triggers, policies, row security, grants on tables and functions,
// and sequences. The runtime role's name differs per database.
func catalog(t *testing.T, db *testutil.Database) string {
	t.Helper()
	return catalogOf(t, db.Context, db.Admin, db.Role)
}

func catalogOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role string) string {
	t.Helper()
	var rows []string
	for _, q := range []string{
		`SELECT 'column '||c.table_schema||'.'||c.table_name||'.'||c.column_name||' '||c.data_type||' '||c.is_nullable||' '||coalesce(c.column_default,'')
 FROM information_schema.columns c WHERE c.table_schema IN ('public','access')`,
		`SELECT 'constraint '||n.nspname||'.'||r.relname||'.'||k.conname||' '||pg_get_constraintdef(k.oid)
 FROM pg_constraint k JOIN pg_class r ON r.oid=k.conrelid JOIN pg_namespace n ON n.oid=r.relnamespace WHERE n.nspname IN ('public','access')`,
		`SELECT 'index '||schemaname||'.'||indexname||' '||indexdef FROM pg_indexes WHERE schemaname IN ('public','access')`,
		`SELECT 'function '||n.nspname||'.'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||') '||md5(p.prosrc)||' '||p.prosecdef||' '||coalesce(array_to_string(p.proconfig,','),'')||' '||coalesce(array_to_string(p.proacl,','),'')
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname IN ('public','access')`,
		`SELECT 'trigger '||r.relname||'.'||g.tgname||' '||pg_get_triggerdef(g.oid)||' '||g.tgenabled::text
 FROM pg_trigger g JOIN pg_class r ON r.oid=g.tgrelid JOIN pg_namespace n ON n.oid=r.relnamespace WHERE n.nspname IN ('public','access') AND NOT g.tgisinternal`,
		`SELECT 'policy '||schemaname||'.'||tablename||'.'||policyname||' '||cmd||' '||coalesce(qual,'')||' '||coalesce(with_check,'') FROM pg_policies WHERE schemaname IN ('public','access')`,
		`SELECT 'table '||n.nspname||'.'||r.relname||' '||r.relkind::text||' '||r.relrowsecurity||' '||r.relforcerowsecurity||' '||coalesce(array_to_string(r.relacl,','),'')
 FROM pg_class r JOIN pg_namespace n ON n.oid=r.relnamespace WHERE n.nspname IN ('public','access') AND r.relkind IN ('r','v','S','m')`,
		`SELECT 'sequence '||schemaname||'.'||sequencename||' '||data_type||' '||start_value||' '||increment_by FROM pg_sequences WHERE schemaname IN ('public','access')`,
		`SELECT 'schema '||nspname||' '||coalesce(array_to_string(nspacl,','),'') FROM pg_namespace WHERE nspname IN ('public','access')`,
	} {
		r, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var s string
			if err := r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, strings.ReplaceAll(s, role, "runtime"))
		}
		r.Close()
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// contents is every row of every table but the receipts, in one text.
func contents(t *testing.T, db *testutil.Database) string {
	t.Helper()
	var tables []string
	if err := db.Admin.QueryRow(db.Context, `SELECT array_agg(table_schema||'.'||table_name ORDER BY table_schema,table_name) FROM information_schema.tables
 WHERE table_schema IN ('public','access') AND table_type='BASE TABLE' AND table_name<>'schema_receipts'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, table := range tables {
		var rows []string
		if err := db.Admin.QueryRow(db.Context, "SELECT coalesce(array_agg(to_jsonb(t)::text ORDER BY to_jsonb(t)::text),'{}') FROM "+table+" t").Scan(&rows); err != nil {
			t.Fatal(table, err)
		}
		out = append(out, table+": "+strings.Join(rows, "; "))
	}
	return strings.Join(out, "\n")
}

func receiptNames(t *testing.T, db *testutil.Database) string {
	t.Helper()
	var names []string
	if err := db.Admin.QueryRow(db.Context, "SELECT array_agg(filename ORDER BY filename) FROM public.schema_receipts").Scan(&names); err != nil {
		t.Fatal(err)
	}
	return strings.Join(names, ",")
}

// seedBook records a synthetic Book as the release it came from stored it.
func seedBook(tenant, book, account string) func(context.Context, *pgxpool.Pool) error {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"SET LOCAL session_replication_role = replica", nil},
			{"INSERT INTO public.tenants(id) VALUES($1)", []any{tenant}},
			{"INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example Household','USD',2)", []any{tenant, book}},
			{"INSERT INTO public.accounts(tenant_id,book_id,id,class,code,name) VALUES($1,$2,$3,'asset','1100','Example Checking')", []any{tenant, book, account}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
}

// Every Vult reaches the same 0.8 schema: a new one through the squashed
// migration, and one from 0.7.0, a release candidate or the development
// Vult through 0.8's steps as they were released, with the squashed
// migration then recorded and never run. Schema, grants, policies and the
// rows migrations write are identical; a Vult's own rows come through
// unchanged.
func TestEveryVultReachesTheSameSchema(t *testing.T) {
	fresh := testutil.New(t)
	want, wantRows := catalog(t, fresh), contents(t, fresh)
	if got := receiptNames(t, fresh); got != "0001_ledger.sql,0002_server_access.sql,0003_documents.sql,0004_recorded_book.sql,0005_proposing_agents.sql,0006_owner_recovery_codes.sql,0007_accounting_foundations.sql,0008_bank_activity_agents_and_linked_books.sql" {
		t.Fatal("a new Vult's receipts", got)
	}
	steps, _ := filepath.Glob("migrations/bridge/*.sql")
	var stepNames []string
	for _, f := range steps {
		stepNames = append(stepNames, filepath.Base(f))
	}
	names := append([]string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql", "0004_recorded_book.sql", "0005_proposing_agents.sql", "0006_owner_recovery_codes.sql", "0007_accounting_foundations.sql", "0008_bank_activity_agents_and_linked_books.sql"}, stepNames...)
	sort.Strings(names)
	bridged := strings.Join(names, ",")
	for name, last := range map[string]string{
		"0.7.0":       "",
		"rc.3":        "0011_agent_autonomy.sql",
		"development": "0020_document_checks.sql",
		"rc.4":        "0030_linked_balance_totals.sql",
		"rc.5":        "0032_carried_costs.sql",
	} {
		t.Run(name, func(t *testing.T) {
			db := releasedThrough(t, last, nil)
			if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
				t.Fatal(err)
			}
			if got := catalog(t, db); got != want {
				t.Fatal("the schema differs from a new Vult's:\n" + diffLines(want, got))
			}
			if got := contents(t, db); got != wantRows {
				t.Fatal("the rows differ from a new Vult's:\n" + diffLines(wantRows, got))
			}
			if got := receiptNames(t, db); got != bridged {
				t.Fatal("receipts", got)
			}
			// Starting again changes nothing, and the Vult is current.
			if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
				t.Fatal("again", err)
			}
			if err := ledger.Ready(db.Context, db.Runtime); err != nil {
				t.Fatal("ready", err)
			}
		})
	}
	// A Vult's own Books come through as they were.
	for name, last := range map[string]string{"0.7.0": "", "rc.5": "0032_carried_costs.sql"} {
		t.Run(name+" with a Book", func(t *testing.T) {
			tenant, book, account := ledger.ID(), ledger.ID(), ledger.ID()
			db := releasedThrough(t, last, seedBook(tenant, book, account))
			before := contents(t, db)
			if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
				t.Fatal(err)
			}
			var row string
			if err := db.Admin.QueryRow(db.Context, "SELECT to_jsonb(b)::text FROM public.books b WHERE id=$1", book).Scan(&row); err != nil || !strings.Contains(before, row) {
				t.Fatal("the Book changed", row, err)
			}
			if err := db.Admin.QueryRow(db.Context, "SELECT to_jsonb(a)::text FROM public.accounts a WHERE id=$1", account).Scan(&row); err != nil || !strings.Contains(before, row) {
				t.Fatal("the account changed", row, err)
			}
		})
	}
}

func diffLines(want, got string) string {
	w := map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		w[l] = true
	}
	g := map[string]bool{}
	for _, l := range strings.Split(got, "\n") {
		g[l] = true
	}
	var out []string
	for l := range w {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range g {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		out = append(out[:40], "…")
	}
	return strings.Join(out, "\n")
}

// An earlier build of 0.8 recorded 0.8 as one file, 0008_v08.sql: run on a
// new Vult, or recorded without running over a release candidate's steps.
// Either reaches the same schema and rows as every other Vult, and a file of
// that name with other contents is still refused.
func TestAnEarlier08BuildReachesTheSameSchema(t *testing.T) {
	fresh := testutil.New(t)
	want, wantRows := catalog(t, fresh), contents(t, fresh)
	squash, err := os.ReadFile("testdata/earlier-0.8/0008_v08.sql")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(squash)
	record := func(run bool, receipt string) func(context.Context, *pgxpool.Pool) error {
		return func(ctx context.Context, pool *pgxpool.Pool) error {
			if run {
				if _, err := pool.Exec(ctx, string(squash)); err != nil {
					return err
				}
			}
			_, err := pool.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES('0008_v08.sql',$1)", receipt)
			return err
		}
	}
	for name, c := range map[string]struct {
		last string
		run  bool
	}{
		"over rc.5": {"0032_carried_costs.sql", false},
		"new":       {"", true},
	} {
		t.Run(name, func(t *testing.T) {
			db := releasedThrough(t, c.last, record(c.run, hex.EncodeToString(sum[:])))
			if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
				t.Fatal(err)
			}
			if got := catalog(t, db); got != want {
				t.Fatal("the schema differs from a new Vult's:\n" + diffLines(want, got))
			}
			if got := contents(t, db); got != wantRows {
				t.Fatal("the rows differ from a new Vult's:\n" + diffLines(wantRows, got))
			}
			if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
				t.Fatal("again", err)
			}
			if err := ledger.Ready(db.Context, db.Runtime); err != nil {
				t.Fatal("ready", err)
			}
		})
	}
	t.Run("another file of that name", func(t *testing.T) {
		db := releasedThrough(t, "0032_carried_costs.sql", record(false, strings.Repeat("0", 64)))
		if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err == nil || err.Error() != "installed schema does not match this release" {
			t.Fatal("an unknown 0008_v08.sql was taken", err)
		}
	})
}

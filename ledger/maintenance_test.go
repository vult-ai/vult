package ledger_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// Every migration must run inside the one transaction the maintenance step
// opens, so a failure part way leaves the database exactly as it was.
func TestMigrationsRunInsideOneTransaction(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)(\bBEGIN\s*;|\b(?:BEGIN\s+TRANSACTION|START\s+TRANSACTION|COMMIT|ROLLBACK|SAVEPOINT|CONCURRENTLY|VACUUM|ALTER\s+SYSTEM|CREATE\s+DATABASE|DROP\s+DATABASE|CREATE\s+TABLESPACE|REINDEX\s+(?:DATABASE|SYSTEM))\b)`)
	// Comments and string literals (messages) aren't statements.
	comment := regexp.MustCompile(`--[^\n]*|'(?:[^']|'')*'`)
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("no migrations found", err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sql := comment.ReplaceAllString(string(data), "")
		if m := forbidden.FindString(sql); m != "" {
			t.Errorf("%s uses %q, which can't run inside the migration transaction", filepath.Base(file), m)
		}
	}
	// The check itself must catch what it looks for.
	for _, bad := range []string{"CREATE INDEX CONCURRENTLY x ON t(a);", "COMMIT;", "begin;", "VACUUM t;"} {
		if !forbidden.MatchString(bad) {
			t.Errorf("not caught: %s", bad)
		}
	}
	for _, fine := range []string{"BEGIN\n  RETURN 1;", "-- commit later", "RAISE EXCEPTION 'must commit first';"} {
		if forbidden.MatchString(comment.ReplaceAllString(fine, "")) {
			t.Errorf("caught in error: %s", fine)
		}
	}
}

func receiptCount(t *testing.T, db *testutil.Database) int {
	t.Helper()
	var n int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.schema_receipts").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedFacts(t *testing.T, db *testutil.Database) ledger.Scope {
	t.Helper()
	_, s := db.Book(t, "Example maintained Book")
	bank := db.Account(t, s, "1000", "asset")
	equity := db.Account(t, s, "3000", "equity")
	expense := db.Account(t, s, "5000", "expense")
	for _, posting := range []struct {
		date  string
		lines []rawLine
	}{
		{"2026-01-02", []rawLine{{bank.ID, "debit", 100000}, {equity.ID, "credit", 100000}}},
		{"2026-01-09", []rawLine{{expense.ID, "debit", 4200}, {bank.ID, "credit", 4200}}},
	} {
		succeeded(t, write(db, db.Runtime, s, func(tx pgx.Tx) error {
			_, _, err := postWithSources(db, tx, s, posting.date, nil, posting.lines...)
			return err
		}))
	}
	return s
}

// Startup never changes the schema: it refuses a database from an earlier
// release until the maintenance step has run, which keeps every fact.
func TestEarlierReleasesNeedTheMaintenanceStep(t *testing.T) {
	for release, last := range map[string]string{
		"0.5.1": "0005_proposing_agents.sql",
		"0.5.2": "0006_owner_recovery_codes.sql",
	} {
		t.Run(release, func(t *testing.T) {
			db := testutil.NewWithInitializer(t, releasedSchemaThrough(last))
			seedFacts(t, db)
			before := receiptCount(t, db)
			if err := ledger.RequireCurrent(db.Context, db.Admin); !errors.Is(err, ledger.ErrNeedsMaintenance) {
				t.Fatalf("startup check on %s: %v", release, err)
			}
			if receiptCount(t, db) != before {
				t.Fatal("the startup check changed the schema")
			}
			facts, err := ledger.Fingerprint(db.Context, db.Admin)
			if err != nil {
				t.Fatal(err)
			}
			if facts.Counts["entries"] != 2 || facts.Counts["lines"] != 4 {
				t.Fatalf("fingerprint counts %v", facts.Counts)
			}
			result, err := ledger.Migrate(db.Context, db.Admin, db.Role, false)
			if err != nil {
				t.Fatal(err)
			}
			state, err := ledger.Schema(db.Context, db.Admin)
			if err != nil || !state.Current() || result.From != before || result.To != state.Known {
				t.Fatalf("after migrating: %+v, %+v, %v", result, state, err)
			}
			if result.Fingerprint.SHA256 != facts.SHA256 {
				t.Fatal("the migration reported a different fingerprint from before")
			}
			after, err := ledger.Fingerprint(db.Context, db.Admin)
			if err != nil || after.SHA256 != facts.SHA256 {
				t.Fatalf("facts changed: %v", err)
			}
			if err := ledger.RequireCurrent(db.Context, db.Admin); err != nil {
				t.Fatal(err)
			}
			if err := ledger.Ready(db.Context, db.Runtime); err != nil {
				t.Fatal("the runtime role can't read the migrated schema", err)
			}
			again, err := ledger.Migrate(db.Context, db.Admin, db.Role, false)
			if err != nil || again.From != again.To {
				t.Fatalf("a second run changed something: %+v, %v", again, err)
			}
		})
	}
}

// If a migration would change a recorded fact, the whole transaction rolls
// back: the schema and every fact stay as they were.
func TestMigrationThatChangesFactsIsRolledBack(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedBookSchema)
	seedFacts(t, db)
	before, err := ledger.Fingerprint(db.Context, db.Admin)
	if err != nil {
		t.Fatal(err)
	}
	receipts := receiptCount(t, db)
	defer ledger.SetBeforeFactCheck(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica; UPDATE public.lines SET amount_minor=amount_minor+1 WHERE amount_minor=4200")
		return err
	})()
	if _, err := ledger.Migrate(db.Context, db.Admin, db.Role, false); !errors.Is(err, ledger.ErrFactsChanged) {
		t.Fatalf("Migrate: %v", err)
	}
	after, err := ledger.Fingerprint(db.Context, db.Admin)
	if err != nil || after.SHA256 != before.SHA256 || receiptCount(t, db) != receipts {
		t.Fatal("the refused migration left changes behind", err)
	}
}

// The fingerprint covers every recorded fact and nothing that may change.
func TestFingerprintCoversFacts(t *testing.T) {
	db := testutil.New(t)
	s := seedFacts(t, db)
	base, err := ledger.Fingerprint(db.Context, db.Admin)
	if err != nil {
		t.Fatal(err)
	}
	changed := func(statement string) bool {
		t.Helper()
		tx, err := db.Admin.Begin(db.Context)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(db.Context, "SET LOCAL session_replication_role = replica"); err != nil {
			t.Fatal(err)
		}
		tag, err := tx.Exec(db.Context, statement, s.BookID)
		if err != nil || tag.RowsAffected() == 0 {
			t.Fatalf("%s: %v (%d rows)", statement, err, tag.RowsAffected())
		}
		f, err := ledger.Fingerprint(db.Context, tx)
		if err != nil {
			t.Fatal(err)
		}
		return f.SHA256 != base.SHA256
	}
	for _, fact := range []string{
		"UPDATE public.lines SET amount_minor=amount_minor+1 WHERE book_id=$1 AND amount_minor=4200",
		"UPDATE public.lines SET side=CASE side WHEN 'debit' THEN 'credit' ELSE 'debit' END WHERE book_id=$1 AND amount_minor=4200",
		"UPDATE public.lines SET account_id=(SELECT id FROM public.accounts WHERE book_id=$1 AND class='equity') WHERE book_id=$1 AND amount_minor=4200 AND side='debit'",
		"UPDATE public.entries SET economic_date=economic_date+1 WHERE book_id=$1",
		"UPDATE public.entries SET accounting_date=accounting_date+1 WHERE book_id=$1",
		"DELETE FROM public.lines WHERE book_id=$1 AND amount_minor=4200",
		"UPDATE public.accounts SET class='liability' WHERE book_id=$1 AND class='equity'",
		"UPDATE public.books SET currency='EUR' WHERE id=$1",
	} {
		if !changed(fact) {
			t.Errorf("not covered: %s", fact)
		}
	}
	for _, other := range []string{
		"UPDATE public.entries SET description='Example renamed' WHERE book_id=$1",
		"UPDATE public.lines SET memo='Example memo' WHERE book_id=$1",
		"UPDATE public.books SET name='Example renamed Book' WHERE id=$1",
	} {
		if changed(other) {
			t.Errorf("covers something that isn't a fact: %s", other)
		}
	}
}

func TestMigrateRefusesAMissingSchema(t *testing.T) {
	db := testutil.New(t)
	if _, err := db.Admin.Exec(db.Context, "DROP TABLE public.schema_receipts"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Migrate(db.Context, db.Admin, db.Role, false); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("Migrate on a missing schema: %v", err)
	}
}

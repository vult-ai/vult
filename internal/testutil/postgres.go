// Package testutil creates disposable logical databases for synthetic tests.
package testutil

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
	"os"
	"strings"
	"testing"
	"time"
)

type Database struct {
	Context context.Context
	Admin   *pgxpool.Pool
	Runtime *pgxpool.Pool
	Owner   ledger.Admin
	Store   ledger.Store
	Role    string
	Name    string
}

func New(t *testing.T) *Database {
	t.Helper()
	file := os.Getenv("VULT_TEST_DSN_FILE")
	if file == "" || os.Getenv("VULT_TEST_ISOLATED") != "1" {
		t.Fatal("VULT_TEST_DSN_FILE and VULT_TEST_ISOLATED=1 are required; database coverage must not skip")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal("synthetic database configuration unavailable")
	}
	config, e := pgxpool.ParseConfig(strings.TrimSpace(string(b)))
	if e != nil {
		t.Fatal("invalid synthetic database configuration")
	}
	host := config.ConnConfig.Host
	if host != "127.0.0.1" && host != "localhost" && !strings.HasPrefix(host, "/") {
		t.Fatal("synthetic database must be local")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	root, e := pgxpool.NewWithConfig(ctx, config.Copy())
	if e != nil {
		t.Fatal("synthetic PostgreSQL unavailable")
	}
	t.Cleanup(root.Close)
	var version int
	if root.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version) != nil || version < 180000 || version >= 190000 {
		t.Fatal("isolated PostgreSQL18 is required")
	}
	suffix := strings.ReplaceAll(ledger.ID(), "-", "")
	db := &Database{Context: ctx, Name: "ledger_test_" + suffix, Role: "ledger_role_" + suffix}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := root.Exec(ctx, sql, args...); e != nil {
			t.Fatalf("synthetic setup failed: %T", e)
		}
	}
	exec("CREATE DATABASE " + pgx.Identifier{db.Name}.Sanitize())
	t.Cleanup(func() {
		if db.Runtime != nil {
			db.Runtime.Close()
		}
		if db.Admin != nil {
			db.Admin.Close()
		}
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if _, e := root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{db.Name}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Errorf("owned test database cleanup failed: %T", e)
		}
		if _, e := root.Exec(cleanup, "DROP ROLE IF EXISTS "+pgx.Identifier{db.Role}.Sanitize()); e != nil {
			t.Errorf("owned test role cleanup failed: %T", e)
		}
	})
	password := suffix + suffix
	exec("CREATE ROLE " + pgx.Identifier{db.Role}.Sanitize() + " LOGIN PASSWORD '" + password + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS")
	adminConfig := config.Copy()
	adminConfig.ConnConfig.Database = db.Name
	adminConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	db.Admin, e = pgxpool.NewWithConfig(ctx, adminConfig)
	if e != nil {
		t.Fatal(e)
	}
	if e = ledger.Apply(ctx, db.Admin, db.Role); e != nil {
		if p, ok := e.(*pgconn.PgError); ok {
			t.Logf("schema SQL context: %s; position %d; internal position %d", p.Where, p.Position, p.InternalPosition)
		}
		t.Fatalf("fresh schema failed: %v", e)
	}
	appConfig := adminConfig.Copy()
	appConfig.ConnConfig.User = db.Role
	appConfig.ConnConfig.Password = password
	appConfig.MaxConns = 4
	appConfig.ConnConfig.RuntimeParams["application_name"] = "ledger-synthetic-runtime"
	db.Runtime, e = pgxpool.NewWithConfig(ctx, appConfig)
	if e != nil {
		t.Fatal(e)
	}
	var current, session string
	var unsafe bool
	if e = db.Runtime.QueryRow(ctx, "SELECT current_user,session_user,rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR rolreplication FROM pg_roles WHERE rolname=current_user").Scan(&current, &session, &unsafe); e != nil || current != db.Role || session != db.Role || unsafe {
		t.Fatal("actual restricted LOGIN required")
	}
	db.Owner = ledger.Admin{DB: db.Admin}
	db.Store = ledger.Store{Runtime: db.Runtime}
	return db
}
func (d *Database) Book(t *testing.T, name string) (ledger.Book, ledger.Scope) {
	t.Helper()
	book, e := d.Owner.CreateBook(d.Context, name, "USD", 2)
	if e != nil {
		t.Fatal(e)
	}
	c, e := d.Owner.CreateConnection(d.Context, book.ID, "synthetic connection", "write")
	if e != nil {
		t.Fatal(e)
	}
	return book, ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
}
func (d *Database) Call(t *testing.T, s ledger.Scope, method string, payload any) json.RawMessage {
	t.Helper()
	b, e := json.Marshal(payload)
	if e != nil {
		t.Fatal(e)
	}
	out, e := d.Store.Call(d.Context, s, method, b)
	if e != nil {
		t.Fatalf("%s failed: %v", method, e)
	}
	return out
}
func (d *Database) Account(t *testing.T, s ledger.Scope, code, class string) ledger.Account {
	t.Helper()
	b := d.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: "create-" + code, Class: class, Code: code, Name: "Example " + code})
	var a ledger.Account
	if json.Unmarshal(b, &a) != nil {
		t.Fatal("invalid account result")
	}
	return a
}

package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func tenantOwner(t *testing.T, db *testutil.Database) (ledger.TenantAdmin, ledger.Book, ledger.Scope) {
	t.Helper()
	tenant := ledger.ID()
	if _, e := db.Admin.Exec(db.Context, "INSERT INTO public.tenants(id) VALUES($1)", tenant); e != nil {
		t.Fatal(e)
	}
	owner, e := db.Owner.ForTenant(db.Context, tenant)
	if e != nil {
		t.Fatal(e)
	}
	book, e := owner.CreateBook(db.Context, "Example Book", "USD", 2)
	if e != nil || book.TenantID != tenant {
		t.Fatal("Book did not use its owner's tenant", e)
	}
	c, e := owner.CreateConnection(db.Context, book.ID, "Example connection", "write")
	if e != nil || c.TenantID != tenant {
		t.Fatal("connection did not use its owner's tenant", e)
	}
	if e := db.EnsureBookOwner(db.Context, book.ID); e != nil {
		t.Fatal(e)
	}
	return owner, book, ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
}

func TestTenantAdministrationBoundaries(t *testing.T) {
	db := testutil.New(t)
	alpha, ab, as := tenantOwner(t, db)
	beta, bb, bs := tenantOwner(t, db)
	for _, s := range []ledger.Scope{as, bs} {
		debit := db.Account(t, s, "1000", "asset")
		credit := db.Account(t, s, "3000", "equity")
		db.Call(t, s, "post_journal", post(debit.ID, credit.ID, "funding"))
	}
	before, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name        string
		owner       ledger.TenantAdmin
		own, other  ledger.Book
		ownScope    ledger.Scope
		foreignConn string
	}{{"alpha", alpha, ab, bb, as, bs.ConnectionID}, {"beta", beta, bb, ab, bs, as.ConnectionID}} {
		t.Run(tc.name, func(t *testing.T) {
			books, e := tc.owner.Books(db.Context)
			if e != nil || len(books) != 1 || books[0].ID != tc.own.ID {
				t.Fatal("owner listing crossed tenants", e)
			}
			connections, e := tc.owner.Connections(db.Context)
			if e != nil || len(connections) != 1 || connections[0].ID != tc.ownScope.ConnectionID {
				t.Fatal("connection listing crossed tenants", e)
			}
			if b, e := tc.owner.Book(db.Context, tc.own.ID); e != nil || b.ID != tc.own.ID {
				t.Fatal("own Book unavailable", e)
			}
			if c, e := tc.owner.Connection(db.Context, tc.ownScope.ConnectionID); e != nil || c.ActorID != tc.ownScope.ActorID {
				t.Fatal("own connection unavailable", e)
			}
			exported, e := tc.owner.Export(db.Context, tc.own.ID)
			if e != nil || exported.Book.ID != tc.own.ID || len(exported.Entries) != 1 || len(exported.Accounts) != 2 || exported.Entries[0].ActorID != tc.ownScope.ActorID {
				t.Fatal("own export was unavailable or mixed tenants", e)
			}
			verified, e := tc.owner.Verify(db.Context, tc.own.ID)
			if e != nil || verified.BookID != tc.own.ID || verified.Entries != 1 || verified.Lines != 2 {
				t.Fatal("own verification failed", e)
			}
			for _, id := range []string{tc.other.ID, ledger.ID()} {
				if b, e := tc.owner.Book(db.Context, id); !errors.Is(e, ledger.ErrNotFound) || !reflect.DeepEqual(b, ledger.Book{}) {
					t.Fatal("foreign or missing Book was disclosed", e)
				}
				if c, e := tc.owner.CreateConnection(db.Context, id, "Example grant", "write"); !errors.Is(e, ledger.ErrNotFound) || !reflect.DeepEqual(c, ledger.Connection{}) {
					t.Fatal("foreign or missing Book accepted a grant", e)
				}
				if e := tc.owner.ClosePeriod(db.Context, id, "2026-01-01", "2026-01-31", ""); !errors.Is(e, ledger.ErrNotFound) {
					t.Fatal("foreign or missing Book accepted a close", e)
				}
				if v, e := tc.owner.Export(db.Context, id); !errors.Is(e, ledger.ErrNotFound) || !reflect.DeepEqual(v, ledger.Export{}) {
					t.Fatal("foreign or missing Book returned an export", e)
				}
				if v, e := tc.owner.Verify(db.Context, id); !errors.Is(e, ledger.ErrNotFound) || !reflect.DeepEqual(v, ledger.Verification{}) {
					t.Fatal("foreign or missing Book returned verification", e)
				}
			}
			for _, id := range []string{tc.foreignConn, ledger.ID()} {
				if c, e := tc.owner.Connection(db.Context, id); !errors.Is(e, ledger.ErrNotFound) || !reflect.DeepEqual(c, ledger.Connection{}) {
					t.Fatal("foreign or missing connection was disclosed", e)
				}
				if e := tc.owner.Revoke(db.Context, id); !errors.Is(e, ledger.ErrNotFound) {
					t.Fatal("foreign or missing connection was revoked", e)
				}
			}
		})
	}
	after, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	for table, rows := range before.Tables {
		if !bytes.Equal(rows, after.Tables[table]) {
			t.Fatal("denied administration changed retained rows", table)
		}
	}
	// A foreign revoke must leave the connection live, including its actor.
	for _, s := range []ledger.Scope{as, bs} {
		db.Call(t, s, "get_book", struct{}{})
	}
	second, e := alpha.CreateBook(db.Context, "Second example Book", "EUR", 2)
	if e != nil || second.TenantID != ab.TenantID {
		t.Fatal("another owned Book was not created", e)
	}
	reader, e := alpha.CreateConnection(db.Context, second.ID, "Example reader", "read")
	if e != nil || reader.TenantID != ab.TenantID || reader.Access != "read" {
		t.Fatal("another owned Book did not receive its connection", e)
	}
	ownedBooks, e := alpha.Books(db.Context)
	if e != nil || len(ownedBooks) != 2 {
		t.Fatal("multiple owned Books were not listed", e)
	}
	for _, owned := range ownedBooks {
		if owned.TenantID != ab.TenantID || owned.ID != ab.ID && owned.ID != second.ID {
			t.Fatal("multiple-Book listing included another tenant")
		}
	}
	if e = alpha.ClosePeriod(db.Context, ab.ID, "2026-01-01", "2026-01-31", "Example close with review items acknowledged"); e != nil {
		t.Fatal("own period close failed", e)
	}
	var otherClosed int
	if e = db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.closed_periods WHERE tenant_id=$1", bb.TenantID).Scan(&otherClosed); e != nil || otherClosed != 0 {
		t.Fatal("own close changed another tenant", e)
	}
	for i := 0; i < 2; i++ {
		if e = alpha.Revoke(db.Context, as.ConnectionID); e != nil {
			t.Fatal("own revocation was not idempotent", e)
		}
	}
	if _, e = alpha.Connection(db.Context, as.ConnectionID); !errors.Is(e, ledger.ErrNotFound) {
		t.Fatal("revoked connection was returned", e)
	}
	if _, e = db.Store.Call(db.Context, as, "get_book", value(struct{}{})); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("revoked connection remained usable", e)
	}
	db.Call(t, bs, "get_book", struct{}{})
}

func TestTenantAdministrationRequiresExplicitScope(t *testing.T) {
	db := testutil.New(t)
	for _, id := range []string{"", "invalid", ledger.ID()} {
		if _, e := db.Owner.ForTenant(db.Context, id); !errors.Is(e, ledger.ErrDenied) {
			t.Fatal("invalid or missing tenant was bound", e)
		}
	}
	if _, e := (ledger.Admin{}).ForTenant(db.Context, ledger.ID()); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("uninitialized administration was bound", e)
	}
	var owner ledger.TenantAdmin
	id := ledger.ID()
	cases := map[string]func() error{
		"create_book":       func() error { _, e := owner.CreateBook(db.Context, "Example", "USD", 2); return e },
		"books":             func() error { _, e := owner.Books(db.Context); return e },
		"book":              func() error { _, e := owner.Book(db.Context, id); return e },
		"create_connection": func() error { _, e := owner.CreateConnection(db.Context, id, "Example", "write"); return e },
		"connections":       func() error { _, e := owner.Connections(db.Context); return e },
		"connection":        func() error { _, e := owner.Connection(db.Context, id); return e },
		"revoke":            func() error { return owner.Revoke(db.Context, id) },
		"close":             func() error { return owner.ClosePeriod(db.Context, id, "2026-01-01", "2026-01-31", "") },
		"export":            func() error { _, e := owner.Export(db.Context, id); return e },
		"verify":            func() error { _, e := owner.Verify(db.Context, id); return e },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if e := call(); !errors.Is(e, ledger.ErrDenied) {
				t.Fatal("missing scope did not fail closed", e)
			}
		})
	}
}

func TestTenantAdministrationReusesOnePooledConnection(t *testing.T) {
	db := testutil.New(t)
	_, ab, _ := tenantOwner(t, db)
	_, bb, _ := tenantOwner(t, db)
	config := db.Admin.Config()
	config.MaxConns, config.MinConns = 1, 1
	pool, e := pgxpool.NewWithConfig(db.Context, config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	operator := ledger.Admin{DB: pool}
	var pid int
	for i, book := range []ledger.Book{ab, bb, ab, bb, ab, bb} {
		owner, e := operator.ForTenant(db.Context, book.TenantID)
		if e != nil {
			t.Fatal(e)
		}
		books, e := owner.Books(db.Context)
		if e != nil || len(books) != 1 || books[0].ID != book.ID {
			t.Fatal("pooled owner listing crossed tenants", e)
		}
		day := fmt.Sprintf("2026-02-%02d", i+1)
		if e = owner.ClosePeriod(db.Context, book.ID, day, day, ""); e != nil {
			t.Fatal(e)
		}
		var current int
		var tenant, selectedBook string
		e = pool.QueryRow(db.Context, "SELECT pg_backend_pid(),coalesce(current_setting('vult.tenant_id',true),''),coalesce(current_setting('vult.book_id',true),'')").Scan(&current, &tenant, &selectedBook)
		if e != nil || tenant != "" || selectedBook != "" || pid != 0 && current != pid {
			t.Fatal("tenant context leaked or test did not reuse one connection", e)
		}
		pid = current
	}
}

func TestTenantAdministrationKeepsExistingInstallation(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "existing"))
	before, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	// Reapplying the released schema receipt must preserve an existing ledger.
	if e = ledger.Apply(db.Context, db.Admin, db.Role); e != nil {
		t.Fatal(e)
	}
	owner, e := db.Owner.ForTenant(db.Context, book.TenantID)
	if e != nil {
		t.Fatal(e)
	}
	exported, e := owner.Export(db.Context, book.ID)
	if e != nil || len(exported.Entries) != 1 || exported.Entries[0].ActorID != scope.ActorID {
		t.Fatal("existing journal was not retained", e)
	}
	legacy, e := db.Owner.Export(db.Context, book.ID)
	if e != nil || !bytes.Equal(value(legacy), value(exported)) {
		t.Fatal("local and tenant exports differ", e)
	}
	after, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	for table, rows := range before.Tables {
		if !bytes.Equal(rows, after.Tables[table]) {
			t.Fatal("existing rows changed", table)
		}
	}
	c, e := owner.Connection(db.Context, scope.ConnectionID)
	if e != nil || c.ActorID != scope.ActorID {
		t.Fatal("existing connection identity changed", e)
	}
	db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "continued"))
}

func TestTenantAdministrationSanitizesDatabaseErrors(t *testing.T) {
	db := testutil.New(t)
	owner, _, _ := tenantOwner(t, db)
	const privateDetail = "Example confidential amount 987654321"
	_, e := db.Admin.Exec(db.Context, `CREATE FUNCTION public.reject_book() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'Example confidential amount 987654321' USING DETAIL=NEW.name;END $$;
 CREATE TRIGGER reject_book BEFORE INSERT ON public.books FOR EACH ROW EXECUTE FUNCTION public.reject_book()`)
	if e != nil {
		t.Fatal(e)
	}
	b, e := owner.CreateBook(db.Context, "Example private name", "USD", 2)
	var databaseError *pgconn.PgError
	if e == nil || e.Error() != "database operation failed" || strings.Contains(e.Error(), privateDetail) || errors.As(e, &databaseError) || !reflect.DeepEqual(b, ledger.Book{}) {
		t.Fatal("database details or partial result escaped")
	}
	encoded, e := json.Marshal(b)
	if e != nil || bytes.Contains(encoded, []byte("Example private name")) {
		t.Fatal("failed operation retained financial data")
	}
}

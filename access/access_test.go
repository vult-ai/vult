package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type fixture struct {
	db           *testutil.Database
	auth, owners *pgxpool.Pool
	service      *access.Service
	operator     access.Operator
	roles        []string
}

func fresh(t *testing.T) *fixture {
	t.Helper()
	db := testutil.New(t)
	f := &fixture{db: db, operator: access.Operator{DB: db.Admin}}
	for i := 0; i < 2; i++ {
		suffix := strings.ReplaceAll(ledger.ID(), "-", "")
		role := "access_role_" + suffix
		password := suffix + suffix
		if _, e := db.Admin.Exec(db.Context, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+password+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"); e != nil {
			t.Fatal(e)
		}
		f.roles = append(f.roles, role)
		c := db.Admin.Config()
		c.ConnConfig.User, c.ConnConfig.Password = role, password
		c.MaxConns, c.MinConns = 1, 1
		pool, e := pgxpool.NewWithConfig(db.Context, c)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			f.auth = pool
		} else {
			f.owners = pool
		}
	}
	t.Cleanup(func() {
		f.auth.Close()
		f.owners.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, role := range f.roles {
			if _, e := db.Admin.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{role}.Sanitize()); e != nil {
				t.Error("owned role cleanup failed")
			}
			if _, e := db.Admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); e != nil {
				t.Error("owned role removal failed")
			}
		}
	})
	if e := f.operator.ConfigureRoles(db.Context, db.Role, f.roles[0], f.roles[1]); e != nil {
		t.Fatal(e)
	}
	s, e := access.New(f.auth, f.owners, "https://ledger.example.com")
	if e != nil {
		t.Fatal(e)
	}
	f.service = s
	return f
}

func (f *fixture) owner(t *testing.T, subject string) (string, string, access.Identity) {
	t.Helper()
	id := access.Identity{Issuer: "https://identity.example.com", Subject: subject, AuthenticatedAt: time.Now().UTC()}
	tenant, e := f.operator.BindOwner(f.db.Context, "", id)
	if e != nil {
		t.Fatal(e)
	}
	session, e := f.service.IssueSession(f.db.Context, id)
	if e != nil {
		t.Fatal(e)
	}
	return tenant, session, id
}

func (f *fixture) book(t *testing.T, tenant, session string) ledger.Book {
	t.Helper()
	var book ledger.Book
	e := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		book, err = owner.CreateBook(f.db.Context, "Example Book", "USD", 2)
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	return book
}

func TestDatabaseEnforcesOwnerSessionAndTenant(t *testing.T) {
	f := fresh(t)
	db := f.db
	a, sa, _ := f.owner(t, "alpha")
	b, sb, _ := f.owner(t, "beta")
	ab, bb := f.book(t, a, sa), f.book(t, b, sb)
	for _, tc := range []struct {
		tenant, session string
		own, other      ledger.Book
	}{{a, sa, ab, bb}, {b, sb, bb, ab}} {
		t.Run(tc.tenant[:8], func(t *testing.T) {
			e := f.service.WithOwner(db.Context, tc.session, tc.tenant, false, func(owner ledger.TenantAdmin) error {
				books, e := owner.Books(db.Context)
				if e != nil || len(books) != 1 || books[0].ID != tc.own.ID {
					t.Fatal("owner list escaped scope", e)
				}
				if _, e = owner.Book(db.Context, tc.other.ID); !errors.Is(e, ledger.ErrNotFound) {
					t.Fatal("foreign Book was disclosed", e)
				}
				if _, e = owner.Export(db.Context, tc.other.ID); !errors.Is(e, ledger.ErrNotFound) {
					t.Fatal("foreign export succeeded", e)
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
	called := false
	if e := f.service.WithOwner(db.Context, sa, b, true, func(ledger.TenantAdmin) error { called = true; return nil }); !errors.Is(e, access.ErrDenied) || called {
		t.Fatal("claimed tenant created authority")
	}
	if _, e := f.service.IssueSession(db.Context, access.Identity{Issuer: "https://identity.example.com", Subject: "unknown", AuthenticatedAt: time.Now()}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("unknown identity enrolled itself")
	}
	if _, e := f.owners.Exec(db.Context, "SELECT access.issue_session('https://identity.example.com','alpha','https://ledger.example.com',$1,clock_timestamp())", access.Hash(access.Random())); e == nil {
		t.Fatal("owner role minted a session")
	}
	if _, e := f.auth.Exec(db.Context, "INSERT INTO access.owners(tenant_id,identity_id) VALUES($1,$2)", ledger.ID(), ledger.ID()); e == nil {
		t.Fatal("identity role granted ownership")
	}
	var n int
	if e := f.owners.QueryRow(db.Context, "SELECT count(*) FROM public.books").Scan(&n); e != nil || n != 0 {
		t.Fatal("owner SQL worked without proof", e)
	}
	tx, e := f.owners.Begin(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true)", a); e != nil {
		t.Fatal(e)
	}
	if e = tx.QueryRow(db.Context, "SELECT count(*) FROM public.books").Scan(&n); e != nil || n != 0 {
		t.Fatal("tenant ID alone granted SQL authority", e)
	}
	if _, e = tx.Exec(db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example','USD',2)", a, ledger.ID()); e == nil {
		t.Fatal("unproved owner SQL wrote a Book")
	}
}

func TestOwnerOperationsKeepLedgerRulesAndRollback(t *testing.T) {
	f := fresh(t)
	db := f.db
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	var connection ledger.Connection
	if e := f.service.WithOwner(db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var e error
		connection, e = owner.CreateConnection(db.Context, book.ID, "Example agent", "write")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	debit, credit := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
	input := ledger.PostJournal{RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "1250"}}}
	before := db.Call(t, scope, "post_journal", input)
	if e := f.service.WithOwner(db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
		v, e := owner.Export(db.Context, book.ID)
		if e != nil || len(v.Entries) != 1 || v.Entries[0].ActorID != scope.ActorID {
			t.Fatal("owner export changed financial records", e)
		}
		verified, e := owner.Verify(db.Context, book.ID)
		if e != nil || verified.Entries != 1 {
			t.Fatal("owner verification failed", e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := f.service.WithOwner(db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		return owner.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31")
	}); e != nil {
		t.Fatal(e)
	}
	input.RequestKey = "closed"
	raw, _ := json.Marshal(input)
	if _, e := db.Store.Call(db.Context, scope, "post_journal", raw); e == nil {
		t.Fatal("restricted owner close did not protect the ledger")
	}
	query, _ := json.Marshal(ledger.EntryQuery{EntryID: func() string { var e ledger.Entry; json.Unmarshal(before, &e); return e.ID }()})
	after, e := db.Store.Call(db.Context, scope, "get_journal", query)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("owner operation rewrote a posting", e)
	}
	err := errors.New("example cancellation")
	if e := f.service.WithOwner(db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		if _, e := owner.CreateBook(db.Context, "Cancelled example", "USD", 2); e != nil {
			return e
		}
		return err
	}); !errors.Is(e, err) {
		t.Fatal(e)
	}
	if e := f.service.WithOwner(db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
		books, e := owner.Books(db.Context)
		if e != nil || len(books) != 1 {
			t.Fatal("cancelled transaction retained a Book", e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var selected, proof string
	if e := f.owners.QueryRow(db.Context, "SELECT coalesce(current_setting('vult.tenant_id',true),''),coalesce(current_setting('vult.owner_session',true),'')").Scan(&selected, &proof); e != nil || selected != "" || proof != "" {
		t.Fatal("pooled owner context leaked", e)
	}
}

func TestRevocationExpiryRealmAndFreshProof(t *testing.T) {
	f := fresh(t)
	db := f.db
	tenant, session, id := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	foreign, e := access.New(f.auth, f.owners, "https://other.example.com")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = foreign.Identity(db.Context, session); !errors.Is(e, access.ErrDenied) {
		t.Fatal("session crossed origin")
	}
	if e = foreign.WithOwner(db.Context, session, tenant, false, func(ledger.TenantAdmin) error { return nil }); !errors.Is(e, access.ErrDenied) {
		t.Fatal("owner lease crossed origin")
	}
	if _, e = db.Admin.Exec(db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(session)); e != nil {
		t.Fatal(e)
	}
	if e = f.service.WithOwner(db.Context, session, tenant, true, func(ledger.TenantAdmin) error { return nil }); !errors.Is(e, access.ErrDenied) {
		t.Fatal("old proof authorized a new owner change")
	}
	if e = f.service.WithOwner(db.Context, session, tenant, false, func(o ledger.TenantAdmin) error { _, e := o.Book(db.Context, book.ID); return e }); e != nil {
		t.Fatal("existing session could not read", e)
	}
	if e = f.operator.RevokeIdentity(db.Context, id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Identity(db.Context, session); !errors.Is(e, access.ErrDenied) {
		t.Fatal("revoked identity kept its session")
	}
	if e = f.service.WithOwner(db.Context, session, tenant, false, func(ledger.TenantAdmin) error { return nil }); !errors.Is(e, access.ErrDenied) {
		t.Fatal("revoked identity kept ownership access")
	}
	newSession, e := f.service.IssueSession(db.Context, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.service.EndSession(db.Context, newSession); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Identity(db.Context, newSession); !errors.Is(e, access.ErrDenied) {
		t.Fatal("logged-out session remained active")
	}
	if e = f.operator.ConfigureRoles(db.Context, db.Role, db.Role, f.roles[1]); !errors.Is(e, access.ErrDenied) {
		t.Fatal("role separation was optional")
	}
}

func TestStaleOwnerSnapshotCannotBypassRevocation(t *testing.T) {
	f := fresh(t)
	db := f.db
	tenant, session, id := f.owner(t, "alpha")
	f.book(t, tenant, session)
	tx, e := f.owners.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	var n int
	if e = tx.QueryRow(db.Context, "SELECT count(*) FROM public.books").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if e = f.operator.RevokeIdentity(db.Context, id); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(db.Context, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin','https://ledger.example.com',true),set_config('vult.tenant_id',$2,true)", access.Hash(session), tenant); e != nil {
		t.Fatal(e)
	}
	var who string
	if e = tx.QueryRow(db.Context, "SELECT access.require_owner($1,false)::text", tenant).Scan(&who); e == nil {
		t.Fatal("stale snapshot bypassed identity revocation")
	}
}

func TestForwardUpgradePreservesExistingBooks(t *testing.T) {
	db := testutil.New(t)
	// Construct the actual released database from its unchanged SQL rather than
	// retaining functions or policies installed by newer forward migrations.
	sql, e := os.ReadFile("../ledger/migrations/0001_ledger.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := db.Admin.Exec(db.Context, "DROP SCHEMA access CASCADE; DROP SCHEMA public CASCADE; CREATE SCHEMA public"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Admin.Exec(db.Context, string(sql)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Admin.Exec(db.Context, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Admin.Exec(db.Context, "INSERT INTO public.schema_receipts(filename,sha256) VALUES('0001_ledger.sql',$1)", ledger.SchemaSHA256()); e != nil {
		t.Fatal(e)
	}
	role := pgx.Identifier{db.Role}.Sanitize()
	for _, grant := range []string{"REVOKE CREATE ON SCHEMA public FROM PUBLIC", "GRANT USAGE ON SCHEMA public TO " + role, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + role, "GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + role, "GRANT UPDATE(write_epoch) ON public.books TO " + role, "GRANT UPDATE(id) ON public.connections TO " + role, "GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO " + role} {
		if _, e = db.Admin.Exec(db.Context, grant); e != nil {
			t.Fatal(e)
		}
	}
	book, scope := db.Book(t, "Existing example")
	debit, credit := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
	db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "existing", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "1250"}}})
	before, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	if e = ledger.Apply(db.Context, db.Admin, db.Role); e != nil {
		t.Fatal("forward upgrade failed", e)
	}
	after, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	for name, rows := range before.Tables {
		if !bytes.Equal(rows, after.Tables[name]) {
			t.Fatal("forward upgrade rewrote retained data", name)
		}
	}
	if before.SchemaSHA256 != after.SchemaSHA256 {
		t.Fatal("v1 backup compatibility changed")
	}
	if e = ledger.Ready(db.Context, db.Runtime); e != nil {
		t.Fatal(e)
	}
	if v, e := db.Owner.Verify(db.Context, book.ID); e != nil || v.Entries != 1 {
		t.Fatal("upgraded journal invalid", e)
	}
	if _, e = db.Admin.Exec(db.Context, "UPDATE public.schema_receipts SET sha256=repeat('0',64) WHERE filename='0001_ledger.sql'"); e != nil {
		t.Fatal(e)
	}
	if e = ledger.Apply(db.Context, db.Admin, db.Role); e == nil {
		t.Fatal("changed released checksum accepted")
	}
}

func TestActiveSessionsAreBoundedPerIdentity(t *testing.T) {
	f := fresh(t)
	_, _, id := f.owner(t, "alpha")
	for i := 1; i < 32; i++ {
		if _, e := f.service.IssueSession(f.db.Context, id); e != nil {
			t.Fatal("session capacity rejected early", e)
		}
	}
	if _, e := f.service.IssueSession(f.db.Context, id); !errors.Is(e, access.ErrDenied) {
		t.Fatal("active session limit was unbounded")
	}
	if e := f.operator.RevokeIdentity(f.db.Context, id); e != nil {
		t.Fatal(e)
	}
	if _, e := f.service.IssueSession(f.db.Context, id); e != nil {
		t.Fatal("old epoch blocked recovery", e)
	}
}

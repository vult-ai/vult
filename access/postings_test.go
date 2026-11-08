package access_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

type postingReadKey struct{}

type expireOwnerRead struct {
	expires     time.Time
	queryPrefix string
	reads       atomic.Int32
}

func (e *expireOwnerRead) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, e.queryPrefix) {
		return context.WithValue(ctx, postingReadKey{}, true)
	}
	return ctx
}

func (e *expireOwnerRead) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(postingReadKey{}) != true || data.Err != nil {
		return
	}
	// The database evaluates expiry using its own clock.
	ctx = context.WithValue(ctx, postingReadKey{}, false)
	if _, err := conn.Exec(ctx, "SELECT pg_sleep_until($1::timestamptz)", e.expires); err == nil {
		e.reads.Add(1)
	}
}

func TestScopedPostingsRejectSessionExpiryBeforeCompletion(t *testing.T) {
	f := fresh(t)
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	var connection ledger.Connection
	if err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	debit := f.db.Account(t, scope, "1000", "asset")
	credit := f.db.Account(t, scope, "3000", "equity")
	var entry ledger.Entry
	q := ledger.PostJournal{RequestKey: "two-pairs", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example balanced pairs"}
	for range 2 {
		q.Lines = append(q.Lines, ledger.PostLine{AccountID: debit.ID, Side: "debit", AmountMinor: "100"}, ledger.PostLine{AccountID: credit.ID, Side: "credit", AmountMinor: "100"})
	}
	if json.Unmarshal(f.db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid journal fixture")
	}
	var result ledger.ScopedPostings
	read := func(owner ledger.TenantAdmin) error {
		var err error
		result, err = owner.JournalPostings(f.db.Context, book.ID, []string{entry.ID})
		return err
	}
	if err := f.service.WithOwner(f.db.Context, session, tenant, false, read); err != nil || len(result.Entries) != 1 || len(result.Entries[0].Lines) != 4 {
		t.Fatal("live owner could not read complete entry", err)
	}
	trace := &expireOwnerRead{queryPrefix: "WITH selected AS MATERIALIZED"}
	if err := f.db.Admin.QueryRow(f.db.Context, "UPDATE access.sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1 RETURNING expires_at", access.Hash(session)).Scan(&trace.expires); err != nil {
		t.Fatal(err)
	}
	config := f.owners.Config()
	config.ConnConfig.Tracer = trace
	owners, err := pgxpool.NewWithConfig(f.db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer owners.Close()
	service, err := access.New(f.auth, owners, "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.WithOwner(f.db.Context, session, tenant, false, read); !errors.Is(err, ledger.ErrDenied) || len(result.Entries) != 0 || trace.reads.Load() != 1 {
		t.Fatal("expired read returned complete or partial postings", err, trace.reads.Load())
	}
}

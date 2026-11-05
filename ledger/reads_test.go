package ledger_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

type queryCounts struct {
	reads         atomic.Int64
	locks         atomic.Int64
	restoredBooks atomic.Int64
}

func (q *queryCounts) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM public.entries") || strings.Contains(data.SQL, "FROM public.lines") {
		q.reads.Add(1)
	}
	if data.SQL == "SELECT public.ledger_lock($1,$2)" {
		q.locks.Add(1)
	}
	if data.SQL == "SELECT public.ledger_validate_restored_book($1,$2)" {
		q.restoredBooks.Add(1)
	}
	return ctx
}

func (*queryCounts) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func traceQueries(t *testing.T, pool *pgxpool.Pool) (*pgxpool.Pool, *queryCounts) {
	t.Helper()
	counts := &queryCounts{}
	config := pool.Config()
	config.ConnConfig.Tracer = counts
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	return traced, counts
}

type cancelConnectionLookup struct{ calls atomic.Int64 }

func (c *cancelConnectionLookup) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT EXISTS(SELECT 1 FROM public.connections WHERE id=") {
		c.calls.Add(1)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	return ctx
}

func (*cancelConnectionLookup) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestConnectionRevocationDoesNotReportLookupFailureAsMissing(t *testing.T) {
	db, _, scope, _, _ := setup(t)
	for i := 0; i < 2; i++ {
		if err := db.Owner.Revoke(db.Context, scope.ConnectionID); err != nil {
			t.Fatal("revocation was not idempotent", err)
		}
	}
	if err := db.Owner.Revoke(db.Context, ledger.ID()); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("missing connection did not report not found", err)
	}
	trace := &cancelConnectionLookup{}
	config := db.Admin.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	err = (ledger.Admin{DB: pool}).Revoke(db.Context, scope.ConnectionID)
	if trace.calls.Load() != 1 || err == nil || errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("failed existence lookup was treated as an absent connection", err)
	}
}

func TestJournalPagesAndExportBatchReadsWithoutChangingRecords(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	expected := ledger.Export{Format: "vult-journal-v1", Book: book, Accounts: []ledger.Account{debit, credit}, Entries: []ledger.Entry{}}
	for i := 0; i < 101; i++ {
		var entry ledger.Entry
		if err := json.Unmarshal(db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, fmt.Sprintf("entry-%d", i))), &entry); err != nil {
			t.Fatal(err)
		}
		expected.Entries = append(expected.Entries, entry)
	}
	var reversal ledger.Entry
	if err := json.Unmarshal(db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: expected.Entries[0].ID, AccountingDate: "2026-01-04", Reason: "Example correction"}), &reversal); err != nil {
		t.Fatal(err)
	}
	expected.Entries = append(expected.Entries, reversal)
	runtime, counts := traceQueries(t, db.Runtime)
	store := db.Store
	store.Runtime = runtime
	var page struct {
		Entries   []ledger.Entry `json:"entries"`
		NextAfter int64          `json:"next_after"`
		HasMore   bool           `json:"has_more"`
	}
	result, err := store.Call(db.Context, scope, "list_journals", value(ledger.ListQuery{Limit: 100}))
	if err != nil || json.Unmarshal(result, &page) != nil {
		t.Fatal("page unavailable", err)
	}
	if counts.reads.Load() != 2 || !page.HasMore || page.NextAfter != expected.Entries[99].Sequence || !bytes.Equal(value(page.Entries), value(expected.Entries[:100])) {
		t.Fatal("batch changed records, order, pagination or query count", counts.reads.Load())
	}
	counts.reads.Store(0)
	result, err = store.Call(db.Context, scope, "list_journals", value(ledger.ListQuery{After: reversal.Sequence, Limit: 100}))
	if err != nil || json.Unmarshal(result, &page) != nil || page.Entries == nil || len(page.Entries) != 0 || page.HasMore || page.NextAfter != reversal.Sequence || counts.reads.Load() != 1 {
		t.Fatal("empty page changed cursor, array shape or query count", err)
	}
	admin, exportCounts := traceQueries(t, db.Admin)
	exporter := ledger.Admin{DB: admin}
	exported, err := exporter.Export(db.Context, book.ID)
	if err != nil || !bytes.Equal(value(exported), value(expected)) || exportCounts.reads.Load() != 5 {
		t.Fatal("export changed retained data or read work", err, exportCounts.reads.Load())
	}
	sum := sha256.Sum256(value(expected))
	verified, err := exporter.Verify(db.Context, book.ID)
	if err != nil || verified.JournalSHA256 != hex.EncodeToString(sum[:]) || verified.Entries != 102 || verified.Lines != 204 {
		t.Fatal("verification hash or counts changed", err)
	}
}

func TestJournalWriteUsesOneApplicationBookLock(t *testing.T) {
	db, _, scope, debit, credit := setup(t)
	runtime, counts := traceQueries(t, db.Runtime)
	store := db.Store
	store.Runtime = runtime
	request := post(debit.ID, credit.ID, "single-lock")
	first, err := store.Call(db.Context, scope, "post_journal", value(request))
	if err != nil || counts.locks.Load() != 1 {
		t.Fatal("write did not use one application lock", err, counts.locks.Load())
	}
	counts.locks.Store(0)
	retry, err := store.Call(db.Context, scope, "post_journal", value(request))
	if err != nil || !bytes.Equal(first, retry) || counts.locks.Load() != 1 {
		t.Fatal("retry changed the result or lock boundary", err)
	}
}

func TestMetadataBudgetRejectsOversizedTablesBeforeArchiveOutput(t *testing.T) {
	db, scope, _ := documentFixture(t)
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)`, scope.TenantID, scope.BookID); err != nil {
		t.Fatal(err)
	}
	// Bytea fields expand to hex in row JSON. Twenty valid, bounded receipts
	// exceed the metadata budget without creating any financial entry.
	if _, err = tx.Exec(db.Context, `INSERT INTO public.requests
 (tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 SELECT $1,$2,gen_random_uuid(),$3,'large-'||n,'example',
 convert_to(jsonb_build_object('request_key','large-'||n,'data',repeat('x',1048400))::text,'UTF8'),
 convert_to(jsonb_build_object('data',repeat('y',1048400))::text,'UTF8')
 FROM generate_series(1,20) n`, scope.TenantID, scope.BookID, scope.ActorID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Owner.Snapshot(db.Context); !errors.Is(err, ledger.ErrLimit) {
		t.Fatal("legacy metadata exceeded its budget", err)
	}
	var output bytes.Buffer
	if err = db.Owner.WriteArchive(db.Context, db.Store.Documents, &output, ""); !errors.Is(err, ledger.ErrLimit) || output.Len() != 0 {
		t.Fatal("oversized metadata reached archive output", err, output.Len())
	}
}

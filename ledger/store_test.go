package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
	"strings"
	"sync"
	"testing"
	"time"
)

func post(a, b string, key string) ledger.PostJournal {
	return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-02", AccountingDate: "2026-01-03", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: a, Side: "debit", AmountMinor: "1250", Memo: "Example debit"}, {AccountID: b, Side: "credit", AmountMinor: "1250", Memo: "Example credit"}}}
}
func value(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func setup(t *testing.T) (*testutil.Database, ledger.Book, ledger.Scope, ledger.Account, ledger.Account) {
	t.Helper()
	db := testutil.New(t)
	book, s := db.Book(t, "Example Book")
	a := db.Account(t, s, "1000", "asset")
	b := db.Account(t, s, "3000", "equity")
	return db, book, s, a, b
}
func TestJournalLifecycleAndExactRetry(t *testing.T) {
	db, book, s, a, b := setup(t)
	q := post(a.ID, b.ID, "funding")
	first := db.Call(t, s, "post_journal", q)
	second := db.Call(t, s, "post_journal", q)
	if !bytes.Equal(first, second) {
		t.Fatal("exact retry changed the result")
	}
	var entry ledger.Entry
	if json.Unmarshal(first, &entry) != nil || len(entry.Lines) != 2 || entry.ActorID != s.ActorID || entry.EconomicDate != "2026-01-02" || entry.AccountingDate != "2026-01-03" || entry.RecordedAt.IsZero() {
		t.Fatal("entry identity or dates lost")
	}
	changed := q
	changed.Description = "Different"
	if _, e := db.Store.Call(db.Context, s, "post_journal", value(changed)); !errors.Is(e, ledger.ErrConflict) {
		t.Fatal("changed retry did not conflict", e)
	}
	outcome := db.Call(t, s, "get_request_outcome", ledger.OutcomeQuery{RequestKey: "funding"})
	var o ledger.Outcome
	if json.Unmarshal(outcome, &o) != nil || o.Status != "recorded" || !bytes.Equal(o.Result, first) {
		t.Fatal("outcome did not retain original result")
	}
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: a.ID, ExpectedRevision: 1, Code: "1010", Name: "Example checking"})
	saved := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})
	if !bytes.Equal(saved, first) {
		t.Fatal("code revision rewrote a posting")
	}
	trial := db.Call(t, s, "get_trial_balance", ledger.TrialQuery{})
	var totals ledger.Trial
	if json.Unmarshal(trial, &totals) != nil || len(totals.Balances) != 2 || totals.Balances[0].DebitPositiveMinor != "1250" || totals.Balances[1].DebitPositiveMinor != "-1250" {
		t.Fatal("literal trial balance differs", string(trial))
	}
	rev := db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: entry.ID, AccountingDate: "2026-01-04", Reason: "Example correction"})
	var reversal ledger.Entry
	if json.Unmarshal(rev, &reversal) != nil || reversal.ReversalOf != entry.ID || reversal.EconomicDate != entry.EconomicDate || len(reversal.Lines) != 2 {
		t.Fatal("reversal identity differs")
	}
	for i, l := range reversal.Lines {
		old := entry.Lines[i]
		if l.ReversesLineID != old.ID || l.AccountID != old.AccountID || l.AmountMinor != old.AmountMinor || l.Side == old.Side || l.Memo != old.Memo {
			t.Fatal("reversal lost original values")
		}
	}
	if _, e := db.Store.Call(db.Context, s, "reverse_journal", value(ledger.ReverseJournal{RequestKey: "again", EntryID: entry.ID, AccountingDate: "2026-01-05", Reason: "Duplicate"})); e == nil {
		t.Fatal("duplicate reversal accepted")
	}
	verified, e := db.Owner.Verify(db.Context, book.ID)
	if e != nil || verified.Entries != 2 || verified.Lines != 4 {
		t.Fatal("independent verification failed", e)
	}
}
func TestConcurrentRetriesReturnOneResult(t *testing.T) {
	db, _, s, a, b := setup(t)
	q := value(post(a.ID, b.ID, "concurrent"))
	var wg sync.WaitGroup
	results := make([]json.RawMessage, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = db.Store.Call(db.Context, s, "post_journal", q) }(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || !bytes.Equal(results[0], results[1]) {
		t.Fatal("concurrent retry failed", errs)
	}
	var n int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&n) != nil || n != 1 {
		t.Fatal("duplicate posting")
	}
}
func TestInvalidInputsDoNotWrite(t *testing.T) {
	db, _, s, a, b := setup(t)
	cases := map[string][]byte{
		"fraction": value(func() ledger.PostJournal {
			q := post(a.ID, b.ID, "fraction")
			q.Lines[0].AmountMinor = "12.50"
			return q
		}()),
		"overflow": value(func() ledger.PostJournal {
			q := post(a.ID, b.ID, "overflow")
			q.Lines[0].AmountMinor = "9223372036854775808"
			return q
		}()),
		"zero": value(func() ledger.PostJournal { q := post(a.ID, b.ID, "zero"); q.Lines[0].AmountMinor = "0"; return q }()),
		"unbalanced": value(func() ledger.PostJournal {
			q := post(a.ID, b.ID, "unbalanced")
			q.Lines[0].AmountMinor = "1251"
			return q
		}()),
		"date":           value(func() ledger.PostJournal { q := post(a.ID, b.ID, "date"); q.AccountingDate = "2026-02-30"; return q }()),
		"duplicate-key":  []byte(`{"request_key":"first","request_key":"second"}`),
		"injected-scope": []byte(`{"request_key":"scope","tenant_id":"00000000-0000-4000-8000-000000000001"}`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := db.Store.Call(db.Context, s, "post_journal", input); !errors.Is(e, ledger.ErrInvalid) {
				t.Fatal("invalid input accepted", e)
			}
		})
	}
	var n int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&n) != nil || n != 0 {
		t.Fatal("rejected input wrote data")
	}
}
func TestTenantBookReadAndRevocationBoundaries(t *testing.T) {
	db, book, s, a, b := setup(t)
	_, other := db.Book(t, "Other Example")
	otherAccount := db.Account(t, other, "1000", "asset")
	q := post(a.ID, otherAccount.ID, "cross-book")
	if _, e := db.Store.Call(db.Context, s, "post_journal", value(q)); e == nil {
		t.Fatal("cross-Book reference accepted")
	}
	forged := s
	forged.BookID = other.BookID
	if out, e := db.Store.Call(db.Context, forged, "get_book", []byte("{}")); e == nil || out != nil {
		t.Fatal("forged Book returned data")
	}
	forged = s
	forged.TenantID = ledger.ID()
	if out, e := db.Store.Call(db.Context, forged, "get_book", []byte("{}")); e == nil || out != nil {
		t.Fatal("forged tenant returned data")
	}
	reader, e := db.Owner.CreateConnection(db.Context, book.ID, "Example read only", "read")
	if e != nil {
		t.Fatal(e)
	}
	r := ledger.Scope{TenantID: reader.TenantID, BookID: reader.BookID, ActorID: reader.ActorID, ConnectionID: reader.ID, Access: "read"}
	db.Call(t, r, "list_accounts", struct{}{})
	if _, e = db.Store.Call(db.Context, r, "post_journal", value(post(a.ID, b.ID, "read-denied"))); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("read access posted", e)
	}
	if e = db.Owner.Revoke(db.Context, s.ConnectionID); e != nil {
		t.Fatal(e)
	}
	if result, e := db.Store.Call(db.Context, s, "get_book", []byte("{}")); !errors.Is(e, ledger.ErrDenied) || result != nil {
		t.Fatal("revoked connection returned data")
	}
}
func TestDirectSQLBalanceImmutabilityAndIsolation(t *testing.T) {
	db, _, s, a, b := setup(t)
	q := post(a.ID, b.ID, "first")
	out := db.Call(t, s, "post_journal", q)
	var entry ledger.Entry
	json.Unmarshal(out, &entry)
	tx, e := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	_, e = tx.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)", s.TenantID, s.BookID, s.ActorID, s.ConnectionID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(db.Context, "INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor) VALUES($1,$2,$3,$4,3,$5,'debit',1)", s.TenantID, s.BookID, ledger.ID(), entry.ID, a.ID)
	if e == nil {
		t.Fatal("appended to an already posted journal")
	}
	tx.Rollback(context.Background())
	if _, e = db.Admin.Exec(db.Context, "UPDATE public.entries SET description='changed' WHERE id=$1", entry.ID); e == nil {
		t.Fatal("posted entry could be rewritten")
	}
	rr, e := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	defer rr.Rollback(context.Background())
	rr.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)", s.TenantID, s.BookID, s.ActorID, s.ConnectionID)
	if _, e = rr.Exec(db.Context, "SELECT public.ledger_lock($1,$2)", s.TenantID, s.BookID); e == nil {
		t.Fatal("repeatable-read write context accepted")
	}
}
func TestClosedPeriodAndReversalDate(t *testing.T) {
	db, book, s, a, b := setup(t)
	var entry ledger.Entry
	json.Unmarshal(db.Call(t, s, "post_journal", post(a.ID, b.ID, "original")), &entry)
	if e := db.Owner.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Store.Call(db.Context, s, "post_journal", value(post(a.ID, b.ID, "closed"))); e == nil {
		t.Fatal("posted in a closed period")
	}
	if _, e := db.Store.Call(db.Context, s, "reverse_journal", value(ledger.ReverseJournal{RequestKey: "closed-reverse", EntryID: entry.ID, AccountingDate: "2026-01-04", Reason: "Example"})); e == nil {
		t.Fatal("reversed into a closed period")
	}
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "open-reverse", EntryID: entry.ID, AccountingDate: "2026-02-01", Reason: "Example"})
}
func TestBackupRestoreKeepsExactRowsAndDropsGrants(t *testing.T) {
	db, book, s, a, b := setup(t)
	db.Call(t, s, "post_journal", post(a.ID, b.ID, "saved"))
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: a.ID, ExpectedRevision: 1, Code: "1010", Name: "Revised example"})
	if e := db.Owner.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"); e != nil {
		t.Fatal(e)
	}
	snapshot, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	target := testutil.New(t)
	if e = target.Owner.Restore(target.Context, snapshot); e != nil {
		t.Fatal("exact restore failed", e)
	}
	after, e := target.Owner.Snapshot(target.Context)
	if e != nil {
		t.Fatal(e)
	}
	for name, want := range snapshot.Tables {
		if !bytes.Equal(want, after.Tables[name]) {
			t.Fatal("restored table differs", name)
		}
	}
	var grants int
	if target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.connections").Scan(&grants) != nil || grants != 0 {
		t.Fatal("restore revived connection grants")
	}
	v, e := target.Owner.Verify(target.Context, book.ID)
	if e != nil || v.Entries != 1 {
		t.Fatal("restored journal invalid", e)
	}
	if e = target.Owner.Restore(target.Context, snapshot); e == nil {
		t.Fatal("restore overwrote a populated installation")
	}
	connection, e := target.Owner.CreateConnection(target.Context, book.ID, "Restored access", "write")
	if e != nil {
		t.Fatal(e)
	}
	newScope := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: connection.Access}
	var next ledger.Entry
	nextInput := post(a.ID, b.ID, "after-restore")
	nextInput.AccountingDate = "2026-02-01"
	json.Unmarshal(target.Call(t, newScope, "post_journal", nextInput), &next)
	var last int64
	if db.Admin.QueryRow(db.Context, "SELECT max(sequence) FROM public.entries").Scan(&last) != nil || next.Sequence <= last {
		t.Fatal("restored journal did not continue after its retained sequence")
	}
}
func TestBackupRestoreKeepsMultipleBooks(t *testing.T) {
	db, firstBook, firstScope, firstDebit, firstCredit := setup(t)
	secondBook, secondScope := db.Book(t, "Second example Book")
	secondDebit := db.Account(t, secondScope, "1000", "asset")
	secondCredit := db.Account(t, secondScope, "3000", "equity")
	for _, entry := range []struct {
		scope  ledger.Scope
		debit  ledger.Account
		credit ledger.Account
	}{{firstScope, firstDebit, firstCredit}, {secondScope, secondDebit, secondCredit}} {
		db.Call(t, entry.scope, "post_journal", post(entry.debit.ID, entry.credit.ID, "saved"))
	}
	if e := db.Owner.ClosePeriod(db.Context, firstBook.ID, "2026-01-01", "2026-01-31"); e != nil {
		t.Fatal(e)
	}
	snapshot, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	target := testutil.New(t)
	if e = target.Owner.Restore(target.Context, snapshot); e != nil {
		t.Fatal("multiple-Book restore failed", e)
	}
	after, e := target.Owner.Snapshot(target.Context)
	if e != nil {
		t.Fatal(e)
	}
	for table, before := range snapshot.Tables {
		if !bytes.Equal(before, after.Tables[table]) {
			t.Fatal("restore changed a retained row", table)
		}
	}
	for _, book := range []ledger.Book{firstBook, secondBook} {
		v, e := target.Owner.Verify(target.Context, book.ID)
		if e != nil || v.Entries != 1 || v.Lines != 2 {
			t.Fatal("restored Book failed verification", e)
		}
	}
	var grants int
	if e = target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.connections").Scan(&grants); e != nil || grants != 0 {
		t.Fatal("restore revived connection grants", e)
	}
	connection, e := target.Owner.CreateConnection(target.Context, firstBook.ID, "Restored access", "write")
	if e != nil {
		t.Fatal(e)
	}
	scope := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: connection.Access}
	q := post(firstDebit.ID, firstCredit.ID, "continued")
	q.AccountingDate = "2026-02-01"
	var continued ledger.Entry
	if e = json.Unmarshal(target.Call(t, scope, "post_journal", q), &continued); e != nil || continued.Sequence < 2 {
		t.Fatal("restored Book could not continue", e)
	}
}

func TestExactLargeIntegerAndBalanceLimit(t *testing.T) {
	db, _, s, a, b := setup(t)
	q := post(a.ID, b.ID, "large")
	for i := range q.Lines {
		q.Lines[i].AmountMinor = "9223372036854775807"
	}
	out := db.Call(t, s, "post_journal", q)
	if !strings.Contains(string(out), "\"9223372036854775807\"") {
		t.Fatal("large integer rounded")
	}
	next := post(a.ID, b.ID, "overflow")
	for i := range next.Lines {
		next.Lines[i].AmountMinor = "1"
	}
	if _, e := db.Store.Call(db.Context, s, "post_journal", value(next)); e == nil {
		t.Fatal("account balance overflow accepted")
	}
	result := db.Call(t, s, "get_request_outcome", ledger.OutcomeQuery{RequestKey: "overflow"})
	var o ledger.Outcome
	if json.Unmarshal(result, &o) != nil || o.Status != "unresolved" {
		t.Fatal("failed transaction left a saved outcome")
	}
}
func TestGatewayOverridesSnapshotDefaults(t *testing.T) {
	db, _, s, a, b := setup(t)
	if _, e := db.Admin.Exec(db.Context, "ALTER ROLE "+pgx.Identifier{db.Role}.Sanitize()+" SET default_transaction_isolation='repeatable read'"); e != nil {
		t.Fatal(e)
	}
	db.Runtime.Reset()
	var defaultLevel string
	if db.Runtime.QueryRow(db.Context, "SHOW default_transaction_isolation").Scan(&defaultLevel) != nil || defaultLevel != "repeatable read" {
		t.Fatal("test did not change the actual LOGIN default")
	}
	db.Call(t, s, "post_journal", post(a.ID, b.ID, "explicit-rc"))
}
func TestRealPeriodLockWaitRechecksCommittedClose(t *testing.T) {
	db, book, s, a, b := setup(t)
	closing, e := db.Admin.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer closing.Rollback(context.Background())
	closing.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)", s.TenantID, s.BookID)
	if _, e = closing.Exec(db.Context, "INSERT INTO public.closed_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,'2026-01-01','2026-01-31')", s.TenantID, book.ID, ledger.ID()); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := db.Store.Call(db.Context, s, "post_journal", value(post(a.ID, b.ID, "waiting")))
		result <- e
	}()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if db.Admin.QueryRow(db.Context, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=$1 AND application_name='ledger-synthetic-runtime' AND wait_event_type='Lock')", db.Name).Scan(&waiting) != nil {
			t.Fatal("lock observation failed")
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("test did not establish a real lock wait")
	}
	if e = closing.Commit(db.Context); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("stale writer bypassed close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not finish")
	}
	var n int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&n) != nil || n != 0 {
		t.Fatal("closed-period writer changed the ledger")
	}
}

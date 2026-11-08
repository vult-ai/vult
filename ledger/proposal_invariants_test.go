package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type proposalQueryTrace struct{ t *testing.T }

func (p proposalQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (p proposalQueryTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	var failure *pgconn.PgError
	if errors.As(data.Err, &failure) && failure.Code != "25P02" {
		p.t.Logf("synthetic SQL failure: %s %s %s", failure.Code, failure.Message, failure.Where)
	}
}
func traceProposalQueries(t *testing.T, db *testutil.Database) {
	t.Helper()
	config := db.Runtime.Config()
	config.ConnConfig.Tracer = proposalQueryTrace{t}
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db.Store.Runtime = pool
}

func directOperationRequest(a, b, key string) ledger.PostJournal {
	input := measuredMoney("USD", 2, "1250")
	input.UnitDefinition = "book"
	valuation := &ledger.Valuation{Basis: "manual", Policy: "supplied-book-value-v1", Input: &input}
	return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example entry", Lines: []ledger.PostLine{
		{AccountID: a, Side: "debit", AmountMinor: "1250", Valuation: valuation},
		{AccountID: b, Side: "credit", AmountMinor: "1250", Valuation: valuation},
	}}
}

func consumeDirectly(db *testutil.Database, tx pgx.Tx, s ledger.Scope, p ledger.OperationProposal, entry ledger.Entry) error {
	_, err := tx.Exec(db.Context, `INSERT INTO public.operation_consumptions(tenant_id,book_id,proposal_id,decision_revision,receipt_id,actor_id)
        VALUES($1,$2,$3,1,$4,$5)`, s.TenantID, s.BookID, p.ID, entry.ReceiptID, s.ActorID)
	return err
}

func TestOperationDatabaseRejectsHistoricalReceiptConsumption(t *testing.T) {
	db, _, s, a, b := setup(t)
	q := directOperationRequest(a.ID, b.ID, "accepted-post")
	p := prepareOperation(t, db, s, "prepare", "post_journal", q)
	approveOperation(t, db, s, p, s)
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, s, "post_journal", q), &entry) != nil {
		t.Fatal("invalid existing posting")
	}
	tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
	err := consumeDirectly(db, tx, s, p, entry)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "operation must commit with approval consumption" {
		t.Fatal("old journal attached to new approval", err)
	}
}

func TestOperationDatabaseAcceptsBalanceChangesAtCommit(t *testing.T) {
	for _, mode := range []string{"before-consumption", "after-consumption"} {
		t.Run(mode, func(t *testing.T) {
			db, _, s, a, b := setup(t)
			p := prepareOperation(t, db, s, "prepare", "post_journal", directOperationRequest(a.ID, b.ID, "accepted-post"))
			approveOperation(t, db, s, p, s)
			if mode == "before-consumption" {
				db.Call(t, s, "post_journal", post(a.ID, b.ID, "intervening-post"))
			}
			tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
			entry := insertDirectEntry(t, db, tx, s, a.ID, b.ID, "accepted-post", "1250")
			if err := consumeDirectly(db, tx, s, p, entry); err != nil {
				t.Fatal("direct consumption precondition failed", err)
			}
			if mode == "after-consumption" {
				insertDirectEntry(t, db, tx, s, a.ID, b.ID, "later-in-transaction", "1250")
			}
			// Other entries move the balance; the approved entry still rests on
			// the same accounts, sources and periods, so it commits.
			if err := tx.Commit(db.Context); err != nil {
				t.Fatal("a balance change refused approved work", err)
			}
			var n int
			if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.operation_consumptions").Scan(&n); err != nil || n != 1 {
				t.Fatal("approved work was not consumed once", err)
			}
		})
	}
}

func TestOperationDatabaseHoldsApprovingIdentityUntilCommit(t *testing.T) {
	db, _, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", directOperationRequest(a.ID, b.ID, "accepted-post"))
	approveOperation(t, db, s, p, s)
	owner, _ := proposalOwner(t, db, s)
	tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
	entry := insertDirectEntry(t, db, tx, s, a.ID, b.ID, "accepted-post", "1250")
	if err := consumeDirectly(db, tx, s, p, entry); err != nil {
		t.Fatal(err)
	}
	revoker, err := db.Admin.Acquire(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer revoker.Release()
	var pid int
	if err = revoker.QueryRow(db.Context, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := revoker.Exec(db.Context, "UPDATE access.identities SET epoch=epoch+1 WHERE id=$1", owner)
		done <- err
	}()
	// Do not leave a waiting revocation goroutine or its connection behind on
	// a failed assertion; abort the consuming transaction before joining it.
	defer func() {
		tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("synthetic revocation did not stop")
		}
	}()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err = db.Admin.QueryRow(db.Context, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err = <-done:
			t.Fatal("identity revocation passed the consumption lease", err)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !blocked {
		t.Fatal("revocation was never observed waiting for the identity lease")
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal("operation before revocation failed", err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not finish after commit")
	}
}

func TestOperationDatabaseRetainsOriginalsForDirectProposal(t *testing.T) {
	db, s, _ := documentFixture(t)
	traceProposalQueries(t, db)
	first := readyOriginal(t, db, s, "first-original")
	second := readyOriginal(t, db, s, "second-original")
	a := db.Account(t, s, "1000", "asset")
	b := db.Account(t, s, "3000", "equity")
	q := post(a.ID, b.ID, "accepted-post")
	q.Sources = []ledger.SourceInput{{DocumentID: first.ID}}
	original := prepareOperation(t, db, s, "prepare", "post_journal", q)
	var payload []byte
	if err := db.Admin.QueryRow(db.Context, "SELECT payload_bytes FROM public.operation_proposals WHERE id=$1", original.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	payload = bytes.ReplaceAll(payload, []byte(first.ID), []byte(second.ID))
	payload = bytes.ReplaceAll(payload, []byte(first.SHA256), []byte(second.SHA256))
	payload = bytes.ReplaceAll(payload, []byte("accepted-post"), []byte("direct-post"))
	tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
	if _, err := tx.Exec(db.Context, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	_, err := tx.Exec(db.Context, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes)
        VALUES($1,$2,$3,1,$4,$5,'direct-prepare','post_journal','direct-post',$6)`, s.TenantID, s.BookID, ledger.ID(), s.ActorID, s.ConnectionID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal("direct proposal failed", err)
	}
	var retained bool
	if err = db.Admin.QueryRow(db.Context, "SELECT used_at IS NOT NULL FROM public.documents WHERE id=$1", second.ID).Scan(&retained); err != nil || !retained {
		t.Fatal("directly referenced original remains expirable", err)
	}
}

func TestOperationHistoryIsAppendOnlyForEveryRuntimeRole(t *testing.T) {
	db, _, s, a, b := setup(t)
	p := prepareOperation(t, db, s, "prepare", "post_journal", post(a.ID, b.ID, "accepted-post"))
	approveOperation(t, db, s, p, s)
	executeOperation(t, db, s, p)
	for _, table := range []string{"operation_proposals", "operation_decisions", "operation_consumptions"} {
		t.Run(table, func(t *testing.T) {
			for _, statement := range []string{"DELETE FROM public.", "UPDATE public."} {
				sql := statement + table
				if strings.HasPrefix(statement, "UPDATE") {
					sql += " SET book_id=book_id"
				}
				tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
				_, err := tx.Exec(db.Context, sql)
				if err == nil {
					t.Fatal("runtime mutated retained operation history")
				}
				tx.Rollback(db.Context)
			}
		})
	}
}

func TestOperationHistoricalRestoreRejectsMismatchedOriginalManifest(t *testing.T) {
	db, s, _ := documentFixture(t)
	traceProposalQueries(t, db)
	a := readyOriginal(t, db, s, "first-original")
	b := readyOriginal(t, db, s, "second-original")
	debit := db.Account(t, s, "1000", "asset")
	credit := db.Account(t, s, "3000", "equity")
	q := post(debit.ID, credit.ID, "accepted-post")
	q.Sources = []ledger.SourceInput{{DocumentID: a.ID}, {DocumentID: b.ID}}
	p := prepareOperation(t, db, s, "prepare", "post_journal", q)
	var data []byte
	if err := db.Admin.QueryRow(db.Context, "SELECT payload_bytes FROM public.operation_proposals WHERE id=$1", p.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var payload, dependencies map[string]json.RawMessage
	var documents []json.RawMessage
	if json.Unmarshal(data, &payload) != nil || json.Unmarshal(payload["dependencies"], &dependencies) != nil || json.Unmarshal(dependencies["documents"], &documents) != nil || len(documents) != 2 {
		t.Fatal("invalid synthetic manifest")
	}
	dependencies["documents"] = value([]json.RawMessage{documents[0], documents[0]})
	payload["dependencies"] = value(dependencies)
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.Context)
	if _, err = tx.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true)", s.TenantID, s.BookID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(db.Context, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes)
        VALUES($1,$2,$3,$4,$5,$6,'historical-copy','post_journal','accepted-post',$7)`, s.TenantID, s.BookID, ledger.ID(), p.Sequence+100000, s.ActorID, s.ConnectionID, []byte(value(payload)))
	if err != nil {
		t.Fatal("historical insert precondition failed", err)
	}
	err = tx.Commit(db.Context)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "operation originals differ" {
		t.Fatal("historical manifest omitted an original", err)
	}
}

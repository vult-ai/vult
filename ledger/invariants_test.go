package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func scopedTransaction(t *testing.T, db *testutil.Database, s ledger.Scope, level pgx.TxIsoLevel) pgx.Tx {
	t.Helper()
	tx, e := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: level})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { tx.Rollback(context.Background()) })
	if _, e = tx.Exec(db.Context, "SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)", s.TenantID, s.BookID, s.ActorID, s.ConnectionID); e != nil {
		t.Fatal(e)
	}
	return tx
}

func TestDatabaseRejectsInvalidPostingsWithoutGateway(t *testing.T) {
	db, _, s, a, b := setup(t)
	for _, variation := range []string{"unbalanced", "missing-line", "mismatched-receipt", "forged-actor"} {
		t.Run(variation, func(t *testing.T) {
			tx := scopedTransaction(t, db, s, pgx.ReadCommitted)
			entry := ledger.Entry{ID: ledger.ID(), ReceiptID: ledger.ID(), ActorID: s.ActorID, EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Direct SQL example", Lines: []ledger.Line{}}
			e := tx.QueryRow(db.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8) RETURNING sequence,recorded_at`, s.TenantID, s.BookID, entry.ID, entry.ReceiptID, s.ActorID, entry.EconomicDate, entry.AccountingDate, entry.Description).Scan(&entry.Sequence, &entry.RecordedAt)
			if e != nil {
				t.Fatal(e)
			}
			for i, account := range []string{a.ID, b.ID} {
				if variation == "missing-line" && i == 1 {
					continue
				}
				line := ledger.Line{ID: ledger.ID(), AccountID: account, Side: "debit", AmountMinor: "1250"}
				if i == 1 {
					line.Side = "credit"
					if variation == "unbalanced" {
						line.AmountMinor = "1249"
					}
				}
				if _, e = tx.Exec(db.Context, "INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor) VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint)", s.TenantID, s.BookID, line.ID, entry.ID, i+1, account, line.Side, line.AmountMinor); e != nil {
					t.Fatal(e)
				}
				entry.Lines = append(entry.Lines, line)
			}
			if variation == "mismatched-receipt" {
				entry.Description = "False saved result"
			}
			actor := s.ActorID
			if variation == "forged-actor" {
				actor = ledger.ID()
			}
			_, e = tx.Exec(db.Context, "INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)", s.TenantID, s.BookID, entry.ReceiptID, actor, variation, []byte(value(map[string]string{"request_key": variation})), []byte(value(entry)))
			if variation == "forged-actor" {
				var p *pgconn.PgError
				if !errors.As(e, &p) || p.Code != "42501" {
					t.Fatal("database accepted a forged actor", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			var p *pgconn.PgError
			if e = tx.Commit(db.Context); !errors.As(e, &p) || p.Code != "23514" {
				t.Fatal("database did not reject the invalid posting", e)
			}
		})
	}
	var n int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&n) != nil || n != 0 {
		t.Fatal("rejected direct SQL left a posting")
	}
}

func TestStaleSnapshotsCannotWriteAfterCloseOrBalanceChange(t *testing.T) {
	for _, variation := range []string{"period", "balance"} {
		t.Run(variation, func(t *testing.T) {
			db, book, s, a, b := setup(t)
			tx := scopedTransaction(t, db, s, pgx.RepeatableRead)
			var before int
			if tx.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&before) != nil || before != 0 {
				t.Fatal("snapshot not established")
			}
			if variation == "period" {
				if e := db.Owner.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"); e != nil {
					t.Fatal(e)
				}
			} else {
				q := post(a.ID, b.ID, "fill")
				for i := range q.Lines {
					q.Lines[i].AmountMinor = "9223372036854775807"
				}
				db.Call(t, s, "post_journal", q)
			}
			var visible int
			table := "entries"
			if variation == "period" {
				table = "closed_periods"
			}
			if tx.QueryRow(db.Context, "SELECT count(*) FROM public."+table).Scan(&visible) != nil || visible != 0 {
				t.Fatal("test did not retain a genuinely stale snapshot")
			}
			_, e := tx.Exec(db.Context, "INSERT INTO public.accounts(tenant_id,book_id,id,class) VALUES($1,$2,$3,'asset')", s.TenantID, s.BookID, ledger.ID())
			var p *pgconn.PgError
			if !errors.As(e, &p) || p.Code != "25001" {
				t.Fatal("database allowed a write under a stale snapshot", e)
			}
		})
	}
}

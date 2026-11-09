package ledger_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
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

func insertDirectEntry(t *testing.T, db *testutil.Database, tx pgx.Tx, scope ledger.Scope, debit, credit, key, amount string) ledger.Entry {
	t.Helper()
	entry := ledger.Entry{ID: ledger.ID(), ReceiptID: ledger.ID(), ActorID: scope.ActorID, EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example entry"}
	err := tx.QueryRow(db.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8) RETURNING sequence,recorded_at`, scope.TenantID, scope.BookID, entry.ID, entry.ReceiptID, scope.ActorID, entry.EconomicDate, entry.AccountingDate, entry.Description).Scan(&entry.Sequence, &entry.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	request := ledger.PostJournal{RequestKey: key, EconomicDate: entry.EconomicDate, AccountingDate: entry.AccountingDate, Description: entry.Description}
	for i, account := range []string{debit, credit} {
		input := measuredMoney("USD", 2, amount)
		input.UnitDefinition = "book"
		line := ledger.Line{ID: ledger.ID(), AccountID: account, Side: []string{"debit", "credit"}[i], AmountMinor: amount,
			Valuation: &ledger.Valuation{Basis: "manual", Policy: "supplied-book-value-v1", Input: &input}}
		if _, err = tx.Exec(db.Context, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,valuation)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9)`, scope.TenantID, scope.BookID, line.ID, entry.ID, i+1, account, line.Side, amount, line.Valuation); err != nil {
			t.Fatal(err)
		}

		request.Lines = append(request.Lines, ledger.PostLine{AccountID: account, Side: line.Side, AmountMinor: amount, Valuation: line.Valuation})
		entry.Lines = append(entry.Lines, line)
	}
	if _, err = tx.Exec(db.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)`, scope.TenantID, scope.BookID, entry.ReceiptID, scope.ActorID, key, []byte(value(request)), []byte(value(entry))); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestEarlyConstraintChecksCannotAllowExtraJournalLines(t *testing.T) {
	for _, mode := range []string{"immediate", "deferred"} {
		t.Run(mode, func(t *testing.T) {
			db, _, scope, debit, credit := setup(t)
			tx := scopedTransaction(t, db, scope, pgx.ReadCommitted)
			entry := insertDirectEntry(t, db, tx, scope, debit.ID, credit.ID, "example", "1")
			if _, err := tx.Exec(db.Context, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
				t.Fatal("valid journal did not pass its first validation", err)
			}
			if mode == "deferred" {
				if _, err := tx.Exec(db.Context, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := tx.Exec(db.Context, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor)
 VALUES($1,$2,$3,$4,3,$5,'debit',1)`, scope.TenantID, scope.BookID, ledger.ID(), entry.ID, debit.ID)
			if mode == "deferred" {
				if err != nil {
					t.Fatal("deferred case did not reach commit validation", err)
				}
				err = tx.Commit(db.Context)
			}
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "23514" {
				t.Fatal("extra line escaped its immutable receipt", err)
			}
		})
	}
}

func TestIntermediateBalanceOverflowCannotBeHiddenByLaterEntry(t *testing.T) {
	for _, mode := range []string{"ordinary", "restore-marker-savepoint"} {
		t.Run(mode, func(t *testing.T) {
			db, _, scope, debit, credit := setup(t)
			parent := scopedTransaction(t, db, scope, pgx.ReadCommitted)
			tx := parent
			if mode == "restore-marker-savepoint" {
				if _, err := tx.Exec(db.Context, "SELECT set_config('vult.restore_book',$1,true)", scope.TenantID+"/"+scope.BookID); err != nil {
					t.Fatal(err)
				}
				var err error
				if tx, err = parent.Begin(db.Context); err != nil {
					t.Fatal(err)
				}
			}
			insertDirectEntry(t, db, tx, scope, debit.ID, credit.ID, "maximum", "9223372036854775807")
			insertDirectEntry(t, db, tx, scope, debit.ID, credit.ID, "overflow", "1")
			insertDirectEntry(t, db, tx, scope, credit.ID, debit.ID, "back-in-range", "1")
			if mode == "restore-marker-savepoint" {
				if err := tx.Commit(db.Context); err != nil {
					t.Fatal(err)
				}
			}
			var failure *pgconn.PgError
			if err := parent.Commit(db.Context); !errors.As(err, &failure) || failure.Code != "22003" {
				t.Fatal("final balance or runtime restore marker hid an invalid prefix", err)
			}
		})
	}
}

func TestHistoricalBalanceValidationRequiresLiveScopedWriteAccess(t *testing.T) {
	db, _, scope, debit, credit := setup(t)
	var receipts []string
	if err := db.Admin.QueryRow(db.Context, "SELECT array_agg(filename ORDER BY filename) FROM public.schema_receipts").Scan(&receipts); err != nil || strings.Join(receipts, ",") != "0001_ledger.sql,0002_server_access.sql,0003_documents.sql,0004_recorded_book.sql,0005_proposing_agents.sql" {
		t.Fatal("fresh installation has an unexpected migration sequence", err)
	}
	var definer, trustedOwner, runtimeAccess, directValidator, directLine, directRestore, directHistory bool
	var settings []string
	if err := db.Admin.QueryRow(db.Context, `SELECT p.prosecdef,r.rolsuper OR r.rolbypassrls,p.proconfig,
 has_function_privilege($1,'public.ledger_validate_balances(uuid,uuid,uuid)','EXECUTE'),
 has_function_privilege($1,'public.ledger_validate_entry()','EXECUTE'),
 has_function_privilege($1,'public.ledger_line_receipt()','EXECUTE'),
 has_function_privilege($1,'public.ledger_validate_restored_book(uuid,uuid)','EXECUTE'),
 has_function_privilege($1,'public.ledger_check_balance_history(uuid,uuid,uuid)','EXECUTE')
 FROM pg_proc p JOIN pg_roles r ON r.oid=p.proowner
 WHERE p.oid='public.ledger_validate_balances(uuid,uuid,uuid)'::regprocedure`, db.Role).Scan(&definer, &trustedOwner, &settings, &runtimeAccess, &directValidator, &directLine, &directRestore, &directHistory); err != nil {
		t.Fatal(err)
	}
	if !definer || !trustedOwner || !runtimeAccess || directValidator || directLine || directRestore || directHistory || !strings.Contains(strings.ReplaceAll(strings.Join(settings, ";"), " ", ""), "search_path=pg_catalog,pg_temp") {
		t.Fatal("validator ownership, search path or execution privileges changed")
	}
	reader, err := db.Owner.CreateConnection(db.Context, scope.BookID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	readScope := ledger.Scope{TenantID: reader.TenantID, BookID: reader.BookID, ActorID: reader.ActorID, ConnectionID: reader.ID, Access: "read"}
	for _, variation := range []string{"reader", "wrong-book", "null-scope"} {
		t.Run(variation, func(t *testing.T) {
			selected := scope
			if variation == "reader" {
				selected = readScope
			}
			tx := scopedTransaction(t, db, selected, pgx.ReadCommitted)
			var tenant, book any = scope.TenantID, scope.BookID
			if variation == "wrong-book" {
				book = ledger.ID()
			} else if variation == "null-scope" {
				tenant = nil
			}
			_, err := tx.Exec(db.Context, "SELECT public.ledger_validate_balances($1,$2,$3)", tenant, book, ledger.ID())
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "42501" {
				t.Fatal("range validation bypassed scoped write authorization", err)
			}
		})
	}
	tx := scopedTransaction(t, db, scope, pgx.ReadCommitted)
	entry := insertDirectEntry(t, db, tx, scope, debit.ID, credit.ID, "pending", "1")
	if err = db.Owner.Revoke(db.Context, scope.ConnectionID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(db.Context, "SELECT public.ledger_validate_balances($1,$2,$3)", scope.TenantID, scope.BookID, entry.ID)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "42501" {
		t.Fatal("deferred range validation ignored live revocation", err)
	}
}

func TestRestoreChecksEveryCompletedBookAndClearsItsMarker(t *testing.T) {
	db := testutil.New(t)
	for i := 0; i < 3; i++ {
		_, scope := db.Book(t, "Example Book")
		if i < 2 {
			debit, credit := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
			db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "example"))
		}
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	pool, counts := traceQueries(t, target.Admin)
	owner := ledger.Admin{DB: pool}
	if err = owner.Restore(target.Context, before); err != nil || counts.restoredBooks.Load() != 3 {
		t.Fatal("restore did not validate each completed Book exactly once", err, counts.restoredBooks.Load())
	}
	after, err := owner.Snapshot(target.Context)
	if err != nil {
		t.Fatal(err)
	}
	for table, rows := range before.Tables {
		if !bytes.Equal(rows, after.Tables[table]) {
			t.Fatal("bulk validation changed retained data", table)
		}
	}
	var marker string
	if err = pool.QueryRow(target.Context, "SELECT coalesce(current_setting('vult.restore_book',true),'')").Scan(&marker); err != nil || marker != "" {
		t.Fatal("restore marker escaped its transaction", err)
	}
}

func TestRestoreRejectsIntermediateOverflowAndRollsBack(t *testing.T) {
	db, _, scope, debit, credit := setup(t)
	var first ledger.Entry
	q := post(debit.ID, credit.ID, "first")
	for i := range q.Lines {
		q.Lines[i].AmountMinor = "1"
	}
	if err := json.Unmarshal(db.Call(t, scope, "post_journal", q), &first); err != nil {
		t.Fatal(err)
	}
	q.RequestKey = "second"
	db.Call(t, scope, "post_journal", q)
	q.RequestKey = "offset"
	q.Lines[0].AccountID, q.Lines[1].AccountID = credit.ID, debit.ID
	db.Call(t, scope, "post_journal", q)
	snapshot, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	// Keep each entry and its result internally consistent while making one
	// intermediate balance overflow and the final balance fit again.
	for _, table := range []string{"lines", "requests"} {
		var rows []map[string]json.RawMessage
		if err = json.Unmarshal(snapshot.Tables[table], &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			var id string
			if table == "lines" {
				if err = json.Unmarshal(row["entry_id"], &id); err != nil {
					t.Fatal(err)
				}
				if id == first.ID {
					row["amount_minor"] = json.RawMessage("9223372036854775807")
					var valuation ledger.Valuation
					if err = json.Unmarshal(row["valuation"], &valuation); err != nil {
						t.Fatal(err)
					}
					valuation.Input.AmountMinor = "9223372036854775807"
					row["valuation"] = value(valuation)
				}
			} else {
				if err = json.Unmarshal(row["id"], &id); err != nil {
					t.Fatal(err)
				}
				if id == first.ReceiptID {
					for i := range first.Lines {
						first.Lines[i].AmountMinor = "9223372036854775807"
						first.Lines[i].Valuation.Input.AmountMinor = "9223372036854775807"
					}
					row["result_bytes"] = value("\\x" + hex.EncodeToString(value(first)))
					var requested ledger.PostJournal
					var encoded string
					if err = json.Unmarshal(row["request_bytes"], &encoded); err != nil {
						t.Fatal(err)
					}
					data, decodeErr := hex.DecodeString(strings.TrimPrefix(encoded, "\\x"))
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					if err = json.Unmarshal(data, &requested); err != nil {
						t.Fatal(err)
					}
					for i := range requested.Lines {
						requested.Lines[i].AmountMinor = "9223372036854775807"
					}
					data = value(requested)
					hash := sha256.Sum256(data)
					row["request_bytes"] = value("\\x" + hex.EncodeToString(data))
					row["request_sha256"] = value(hex.EncodeToString(hash[:]))
				}
			}
		}
		snapshot.Tables[table] = value(rows)
	}
	target := testutil.New(t)
	pool, counts := traceQueries(t, target.Admin)
	if err = (ledger.Admin{DB: pool}).Restore(target.Context, snapshot); !errors.Is(err, ledger.ErrInvalid) || counts.restoredBooks.Load() != 1 {
		t.Fatal("bulk restore missed an invalid intermediate prefix", err, counts.restoredBooks.Load())
	}
	var books int
	var marker string
	if err = pool.QueryRow(target.Context, "SELECT count(*) FROM public.books").Scan(&books); err != nil || books != 0 {
		t.Fatal("rejected restore committed partial data", err)
	}
	if err = pool.QueryRow(target.Context, "SELECT coalesce(current_setting('vult.restore_book',true),'')").Scan(&marker); err != nil || marker != "" {
		t.Fatal("failed restore left a reusable bypass marker", err)
	}
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

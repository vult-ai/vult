package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

// ReleasedAccount seeds the account shape shared by released schemas. Upgrade
// fixtures must not ask the current gateway to write columns that did not exist.
func (d *Database) ReleasedAccount(t *testing.T, s ledger.Scope, code, class string) ledger.Account {
	t.Helper()
	d.requireReleasedShape(t)
	q := ledger.CreateAccount{RequestKey: "create-" + code, Class: class, Code: code, Name: "Example " + code}
	a := ledger.Account{ID: ledger.ID(), Class: class, Code: code, Name: q.Name, Revision: 1}
	d.writeReleased(t, s, "create_account", q.RequestKey, q, func(tx pgx.Tx, _ string) json.RawMessage {
		if _, err := tx.Exec(d.Context, "INSERT INTO public.accounts(tenant_id,book_id,id,class)VALUES($1,$2,$3,$4)", s.TenantID, s.BookID, a.ID, class); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(d.Context, "INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id)VALUES($1,$2,$3,1,$4,$5,$6)", s.TenantID, s.BookID, a.ID, code, a.Name, s.ActorID); err != nil {
			t.Fatal(err)
		}
		return releasedJSON(t, a)
	})
	return a
}

// ReleasedPost uses only the original journal columns and freezes their receipt.
func (d *Database) ReleasedPost(t *testing.T, s ledger.Scope, q ledger.PostJournal) json.RawMessage {
	t.Helper()
	d.requireReleasedShape(t)
	if q.MeasurementPolicy != nil || len(q.Sources) > 0 {
		t.Fatal("released fixture requires an original journal shape")
	}
	for _, l := range q.Lines {
		if l.Native != nil || len(l.Contexts) > 0 || l.Valuation != nil {
			t.Fatal("released fixture cannot contain later measurements")
		}
	}
	return d.writeReleased(t, s, "post_journal", q.RequestKey, q, func(tx pgx.Tx, receipt string) json.RawMessage {
		e := ledger.Entry{ID: ledger.ID(), ReceiptID: receipt, ActorID: s.ActorID, EconomicDate: q.EconomicDate, AccountingDate: q.AccountingDate, Description: q.Description, Lines: []ledger.Line{}}
		if err := tx.QueryRow(d.Context, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8)RETURNING sequence,recorded_at`, s.TenantID, s.BookID, e.ID, receipt, s.ActorID, q.EconomicDate, q.AccountingDate, q.Description).Scan(&e.Sequence, &e.RecordedAt); err != nil {
			t.Fatal(err)
		}
		for i, l := range q.Lines {
			line := ledger.Line{ID: ledger.ID(), AccountID: l.AccountID, Side: l.Side, AmountMinor: l.AmountMinor, Memo: l.Memo}
			if _, err := tx.Exec(d.Context, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9)`, s.TenantID, s.BookID, line.ID, e.ID, i+1, l.AccountID, l.Side, l.AmountMinor, l.Memo); err != nil {
				t.Fatal(err)
			}
			e.Lines = append(e.Lines, line)
		}
		return releasedJSON(t, e)
	})
}

func (d *Database) writeReleased(t *testing.T, s ledger.Scope, operation, key string, request any, apply func(pgx.Tx, string) json.RawMessage) json.RawMessage {
	t.Helper()
	tx, err := d.Runtime.BeginTx(d.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(d.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
 set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	receipt := ledger.ID()
	result := apply(tx, receipt)
	if _, err = tx.Exec(d.Context, `INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.TenantID, s.BookID, receipt, s.ActorID, key, operation, []byte(releasedJSON(t, request)), []byte(result)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(d.Context); err != nil {
		t.Fatal(err)
	}
	return result
}

// ReleasedExport reads the original columns and accepted journal receipts. It is
// a frozen upgrade baseline, not a fallback in the production reader.
func (d *Database) ReleasedExport(t *testing.T, book string) ledger.Export {
	t.Helper()
	d.requireReleasedShape(t)
	tx, err := d.Admin.BeginTx(d.Context, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	out := ledger.Export{Format: "vult-journal-v1", Accounts: []ledger.Account{}, Entries: []ledger.Entry{}}
	if err = tx.QueryRow(d.Context, "SELECT tenant_id::text,id::text,name,currency,scale,created_at FROM public.books WHERE id=$1", book).Scan(&out.Book.TenantID, &out.Book.ID, &out.Book.Name, &out.Book.Currency, &out.Book.Scale, &out.Book.CreatedAt); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(d.Context, "SELECT id::text,class,code,name,revision FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 ORDER BY code,id", out.Book.TenantID, book)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a ledger.Account
		if err = rows.Scan(&a.ID, &a.Class, &a.Code, &a.Name, &a.Revision); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out.Accounts = append(out.Accounts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	rows, err = tx.Query(d.Context, `SELECT r.result_bytes FROM public.entries e JOIN public.requests r ON r.tenant_id=e.tenant_id AND r.book_id=e.book_id AND r.id=e.receipt_id
 WHERE e.tenant_id=$1 AND e.book_id=$2 ORDER BY e.sequence`, out.Book.TenantID, book)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		var e ledger.Entry
		if err = rows.Scan(&data); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		out.Entries = append(out.Entries, e)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func releasedJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ReleasedSnapshot captures the original logical wire shape before an upgrade.
func (d *Database) ReleasedSnapshot(t *testing.T) ledger.Snapshot {
	t.Helper()
	d.requireReleasedShape(t)
	tx, err := d.Admin.BeginTx(d.Context, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	out := ledger.Snapshot{Format: "vult-ledger-backup-v1", SchemaSHA256: ledger.SchemaSHA256(), CreatedAt: time.Now().UTC(), Tables: map[string]json.RawMessage{}}
	for _, table := range []string{"tenants", "books", "accounts", "account_revisions", "requests", "entries", "lines", "closed_periods"} {
		projection := "to_jsonb(t)"
		if table == "books" {
			projection += "-'write_epoch'"
		}
		rows, err := tx.Query(d.Context, "SELECT ("+projection+")::text AS rowdata FROM public."+pgx.Identifier{table}.Sanitize()+" t ORDER BY rowdata")
		if err != nil {
			t.Fatal(err)
		}
		data := []json.RawMessage{}
		for rows.Next() {
			var row []byte
			if err = rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			data = append(data, append(json.RawMessage{}, row...))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		out.Tables[table] = releasedJSON(t, data)
	}
	return out
}

func (d *Database) requireReleasedShape(t *testing.T) {
	t.Helper()
	rows, err := d.Admin.Query(d.Context, "SELECT filename FROM public.schema_receipts ORDER BY filename")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	known := []string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql"}
	count := 0
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if count >= len(known) || name != known[count] {
			t.Fatal("fixture requires a recognized released schema")
		}
		count++
	}
	if err = rows.Err(); err != nil || count == 0 {
		t.Fatal("released schema receipts unavailable", err)
	}
}

// AssertReleasedSnapshotUpgrade checks the retained rows and the exact metadata
// that an upgrade adds to the single-Book fixture. It returns the fixed code.
func AssertReleasedSnapshotUpgrade(t *testing.T, before, after ledger.Snapshot, book ledger.Book) string {
	t.Helper()
	emptyTables := []string{"book_revisions", "operation_proposals", "operation_decisions", "operation_consumptions", "book_identity_revisions", "book_logo_images"}
	if len(after.Tables) != len(before.Tables)+len(emptyTables)+1 {
		t.Fatal("upgrade changed the expected backup table set")
	}
	for _, table := range emptyTables {
		if _, existed := before.Tables[table]; existed || !bytes.Equal(after.Tables[table], []byte("[]")) {
			t.Fatal("upgrade invented retained metadata or operation history", table)
		}
	}
	for table, rows := range before.Tables {
		if !bytes.Equal(rows, after.Tables[table]) {
			t.Fatal("upgrade rewrote retained data", table)
		}
	}
	var addresses []struct {
		TenantID string `json:"tenant_id"`
		ledger.BookAddress
	}
	code := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$`)
	if json.Unmarshal(after.Tables["book_addresses"], &addresses) != nil || len(addresses) != 1 ||
		addresses[0].TenantID != book.TenantID || addresses[0].BookID != book.ID ||
		!code.MatchString(addresses[0].Code) || addresses[0].Slug != "" || addresses[0].Revision != 0 ||
		addresses[0].Aliases == nil || len(addresses[0].Aliases) != 0 {
		t.Fatal("upgrade did not allocate exactly one fixed address for the existing Book")
	}
	return addresses[0].Code
}

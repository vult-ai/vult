package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func timezoneAdmin(t *testing.T, db *testutil.Database, zone string) ledger.Admin {
	t.Helper()
	config := db.Admin.Config().Copy()
	config.ConnConfig.RuntimeParams["timezone"] = zone
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ledger.Admin{DB: pool}
}

func historicalTimezoneBook(t *testing.T, db *testutil.Database) (ledger.Book, ledger.Scope) {
	t.Helper()
	tenant, err := db.Owner.EnsureTenant(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	book := ledger.Book{TenantID: tenant, ID: ledger.ID(), Name: "Example retained Book", Currency: "USD", Scale: 2}
	// Initialize a historical fixture with all normal constraints and triggers.
	// Monrovia used a seconds offset in 1970 but has a valid modern timezone.
	if err = db.Admin.QueryRow(db.Context, `INSERT INTO public.books(tenant_id,id,name,currency,scale,created_at)
		VALUES($1,$2,$3,$4,$5,'1970-01-01T00:00:00Z') RETURNING created_at`, tenant, book.ID, book.Name, book.Currency, book.Scale).Scan(&book.CreatedAt); err != nil {
		t.Fatal(err)
	}
	connection, err := db.Owner.CreateConnection(db.Context, book.ID, "Example writer", "write")
	if err != nil {
		t.Fatal(err)
	}
	return book, ledger.Scope{TenantID: tenant, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: connection.Access}
}

func TestSnapshotRestoreAcrossSessionTimezones(t *testing.T) {
	for _, zones := range [][2]string{{"Pacific/Auckland", "UTC"}, {"UTC", "Pacific/Auckland"}, {"Africa/Monrovia", "UTC"}, {"UTC", "Africa/Monrovia"}} {
		t.Run(zones[0]+"-to-"+zones[1], func(t *testing.T) {
			source := testutil.New(t)
			book, scope := historicalTimezoneBook(t, source)
			debit, credit := source.Account(t, scope, "1000", "asset"), source.Account(t, scope, "3000", "equity")
			request := ledger.PostJournal{RequestKey: "recorded-opening", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example retained opening", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "12345", Memo: `{"created_at":"opaque"}`}, {AccountID: credit.ID, Side: "credit", AmountMinor: "12345"}}}
			source.Call(t, scope, "post_journal", request)
			want, err := source.Owner.Export(source.Context, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := timezoneAdmin(t, source, zones[0]).Snapshot(source.Context)
			if err != nil {
				t.Fatal(err)
			}
			if zones[0] == "Africa/Monrovia" && !bytes.Contains(snapshot.Tables["books"], []byte("-00:44:30")) {
				t.Fatal("actual snapshot writer did not exercise the seconds offset")
			}
			target := testutil.New(t)
			if err = timezoneAdmin(t, target, zones[1]).Restore(target.Context, snapshot); err != nil {
				t.Fatal("same history did not restore across display timezones", err)
			}
			got, err := target.Owner.Export(target.Context, book.ID)
			if err != nil || !bytes.Equal(value(got), value(want)) {
				t.Fatal("timezone portability changed recorded accounting", err)
			}
			assertTimezoneReceipts(t, source, target)
		})
	}
}

func TestRestoreRejectsUnrepresentableSnapshotTimestamps(t *testing.T) {
	source := testutil.New(t)
	source.Book(t, "Example retained Book")
	snapshot, err := source.Owner.Snapshot(source.Context)
	if err != nil {
		t.Fatal(err)
	}
	for name, timestamp := range map[string]string{
		"submicrosecond": "2026-01-02T00:00:00.123456001Z",
		"long-fraction":  "2026-01-02T00:00:00.1234560001Z",
		"short-hour":     "2026-01-02T0:00:00.1234560001Z",
	} {
		t.Run(name, func(t *testing.T) {
			copy := snapshot
			copy.Tables = maps.Clone(snapshot.Tables)
			var books []map[string]json.RawMessage
			if json.Unmarshal(copy.Tables["books"], &books) != nil || len(books) != 1 {
				t.Fatal("invalid synthetic snapshot")
			}
			books[0]["created_at"] = value(timestamp)
			copy.Tables["books"] = value(books)
			target := testutil.New(t)
			if err = target.Owner.Restore(target.Context, copy); !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("unrepresentable timestamp was accepted", err)
			}
			if got, err := target.Owner.Books(target.Context); err != nil || len(got) != 0 {
				t.Fatal("failed timestamp restore left a partial Book", err)
			}
		})
	}
}

func TestPortableArchiveRestoreAcrossSessionTimezones(t *testing.T) {
	for _, zones := range [][2]string{{"Pacific/Auckland", "UTC"}, {"UTC", "Pacific/Auckland"}, {"Africa/Monrovia", "UTC"}, {"UTC", "Africa/Monrovia"}} {
		t.Run(zones[0]+"-to-"+zones[1], func(t *testing.T) {
			source, _, _ := documentFixture(t)
			_, scope := historicalTimezoneBook(t, source)
			original := readyOriginal(t, source, scope, "retained-original")
			postCitedJournal(t, source, scope, "retained-entry", []ledger.SourceInput{{DocumentID: original.ID}})
			want, err := source.Owner.Export(source.Context, scope.BookID)
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			if err = timezoneAdmin(t, source, zones[0]).WriteArchive(source.Context, source.Store.Documents, &archive, scope.BookID); err != nil {
				t.Fatal(err)
			}
			if zones[0] == "Africa/Monrovia" && !bytes.Contains(archiveManifestOf(t, archive.Bytes()).Snapshot.Tables["books"], []byte("-00:44:30")) {
				t.Fatal("actual archive writer did not exercise the seconds offset")
			}
			target := testutil.New(t)
			vault, _ := restoreVault(t)
			if err = timezoneAdmin(t, target, zones[1]).RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
				t.Fatal("portable archive rejected equal timestamp instants", err)
			}
			got, err := target.Owner.Export(target.Context, scope.BookID)
			if err != nil || !bytes.Equal(value(got), value(want)) {
				t.Fatal("archive timezone portability changed accounting", err)
			}
			assertTimezoneReceipts(t, source, target)
			connection, err := target.Owner.CreateConnection(target.Context, scope.BookID, "Example restored reader", "read")
			if err != nil {
				t.Fatal(err)
			}
			target.Store.Documents = vault
			restoredScope := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: connection.Access}
			var data bytes.Buffer
			if err = target.Store.Download(target.Context, restoredScope, original.ID, &data); err != nil || data.String() != "synthetic original retained-original" {
				t.Fatal("restored original differs", err)
			}
		})
	}
}

func assertTimezoneReceipts(t *testing.T, source, target *testutil.Database) {
	t.Helper()
	read := func(db *testutil.Database) []byte {
		t.Helper()
		var rows []byte
		if err := db.Admin.QueryRow(db.Context, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY id),'[]') FROM (SELECT id,request_bytes,result_bytes FROM public.requests) r`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if !bytes.Equal(read(source), read(target)) {
		t.Fatal("restoring rewrote saved request or result bytes")
	}
}

func TestStandaloneCurrencySnapshotKeepsMeasurements(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "tiny-acquisition", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	want, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	// The older standalone format contained currency facts but no Book or
	// operation revisions. It carries no archive migration receipts.
	before.Format = "vult-ledger-backup-v2"
	for _, name := range []string{"book_revisions", "book_addresses", "book_identity_revisions", "book_logo_images",
		"operation_proposals", "operation_decisions", "operation_consumptions", "payees", "payee_revisions", "entry_payees",
		"statements", "statement_rows", "statement_duplicates", "statement_matches", "statement_match_parts",
		"statement_match_revocations", "statement_adjustments", "statement_reviews", "accounting_periods", "accounting_period_events"} {
		delete(before.Tables, name)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, before); err != nil {
		t.Fatal("standalone currency snapshot was rejected", err)
	}
	got, err := target.Owner.Export(target.Context, book.ID)
	if err != nil || !bytes.Equal(value(got), value(want)) {
		t.Fatal("standalone currency snapshot changed the entry", err)
	}
}

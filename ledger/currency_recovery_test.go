package ledger_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func releasedSchema(ctx context.Context, pool *pgxpool.Pool, role string) error {
	if _, err := pool.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp()); REVOKE ALL ON public.schema_receipts FROM PUBLIC"); err != nil {
		return err
	}
	for _, name := range []string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql"} {
		data, err := os.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if _, err = pool.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
	}
	r := pgx.Identifier{role}.Sanitize()
	for _, grant := range []string{
		"GRANT USAGE ON SCHEMA public,access TO " + r,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts TO " + r,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + r,
		"GRANT UPDATE(write_epoch) ON public.books TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_context(uuid,uuid),public.ledger_allowed(uuid,uuid,boolean),public.ledger_lock(uuid,uuid),public.ledger_import_owner(),public.ledger_validate_balances(uuid,uuid,uuid) TO " + r,
		"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid),access.owner_write_allowed(uuid) TO " + r,
		"GRANT SELECT ON public.documents,public.source_citations TO " + r,
	} {
		if _, err := pool.Exec(ctx, grant); err != nil {
			return err
		}
	}
	return nil
}

func currencyArchive(t *testing.T, snapshot ledger.Snapshot, sums map[string]string) []byte {
	t.Helper()
	format := strings.Replace(snapshot.Format, "vult-documents-backup-", "vult-archive-", 1)
	data := value(map[string]any{"format": format, "migrations": sums, "snapshot": snapshot, "originals": []any{}})
	sum := sha256.Sum256(data)
	footer := value(map[string]string{"format": "vult-archive-complete-v1", "manifest_sha256": hex.EncodeToString(sum[:])})
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, item := range []struct {
		name string
		data []byte
	}{{"manifest.json", data}, {"complete.json", footer}} {
		if err := writer.WriteHeader(&tar.Header{Name: item.name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(item.data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func TestCurrencyOldSchemaSnapshotsAndArchivesRestorePopulatedHistory(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedSchema)
	book, s := db.Book(t, "Example older Book")
	debit := db.ReleasedAccount(t, s, "1000", "asset")
	credit := db.ReleasedAccount(t, s, "3000", "equity")
	q := post(debit.ID, credit.ID, "older-entry")
	for i := range q.Lines {
		q.Lines[i].AmountMinor = "9007199254740993"
	}
	original := db.ReleasedPost(t, s, q)
	var entry ledger.Entry
	if json.Unmarshal(original, &entry) != nil {
		t.Fatal("invalid released posting fixture")
	}
	before := db.ReleasedSnapshot(t)
	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})) {
		t.Fatal("upgrade reinterpreted older recorded outcome")
	}
	var retained []byte
	if db.Admin.QueryRow(db.Context, "SELECT result_bytes FROM public.requests WHERE id=$1", entry.ReceiptID).Scan(&retained) != nil || !bytes.Equal(retained, original) {
		t.Fatal("upgrade changed canonical receipt bytes")
	}
	want, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, before); err != nil {
		t.Fatal("older populated snapshot restore failed", err)
	}
	got, err := target.Owner.Export(target.Context, book.ID)
	if err != nil || !bytes.Equal(value(want), value(got)) {
		t.Fatal("older snapshot changed recorded accounting", err)
	}
	archiveSnapshot := before
	archiveSnapshot.Tables = map[string]json.RawMessage{}
	for k, v := range before.Tables {
		archiveSnapshot.Tables[k] = v
	}
	archiveSnapshot.Format = "vult-documents-backup-v1"
	for _, table := range []string{"documents", "document_uploads", "source_citations", "evidence_links"} {
		archiveSnapshot.Tables[table] = json.RawMessage("[]")
	}
	sums := map[string]string{}
	for _, name := range []string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql"} {
		data, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(sum[:])
	}
	for _, mode := range []string{"valid", "changed", "missing", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			hashes := map[string]string{}
			for k, v := range sums {
				hashes[k] = v
			}
			switch mode {
			case "changed":
				hashes["0003_documents.sql"] = string(bytes.Repeat([]byte("0"), 64))
			case "missing":
				delete(hashes, "0002_server_access.sql")
			case "unknown":
				delete(hashes, "0003_documents.sql")
				hashes["0009_other.sql"] = sums["0003_documents.sql"]
			}
			target := testutil.New(t)
			vault, _ := restoreVault(t)
			err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(currencyArchive(t, archiveSnapshot, hashes)), nil)
			if mode == "valid" && err != nil || mode != "valid" && !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("archive migration prefix acceptance differs", mode, err)
			}
			if mode == "valid" {
				got, err := target.Owner.Export(target.Context, book.ID)
				if err != nil || !bytes.Equal(value(want), value(got)) {
					t.Fatal("older archive changed postings", err)
				}
			}
		})
	}
}
func TestCurrencyPopulatedRecoveryKeepsMeasurementsAndRejectsLoss(t *testing.T) {
	db, book, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "rounded-acquisition", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, s, "post_journal", q)
	snapshot, err := db.Owner.Snapshot(db.Context)
	if err != nil || snapshot.Format != "vult-ledger-backup-v5" {
		t.Fatal("currency snapshot format missing", err)
	}
	want, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing-native", "changed-quote", "unknown-column", "old-format"} {
		t.Run(mode, func(t *testing.T) {
			copy := snapshot
			copy.Tables = map[string]json.RawMessage{}
			for k, v := range snapshot.Tables {
				copy.Tables[k] = append(json.RawMessage{}, v...)
			}
			if mode == "old-format" {
				copy.Format = "vult-ledger-backup-v1"
				delete(copy.Tables, "book_revisions")
			}
			var rows []map[string]json.RawMessage
			if json.Unmarshal(copy.Tables["lines"], &rows) != nil {
				t.Fatal("invalid snapshot")
			}
			for _, row := range rows {
				if _, ok := row["native_currency"]; !ok {
					continue
				}
				switch mode {
				case "missing-native":
					delete(row, "native_currency")
					delete(row, "native_scale")
					delete(row, "native_amount_minor")
					delete(row, "native_account_revision")
				case "changed-quote":
					var v ledger.Valuation
					json.Unmarshal(row["valuation"], &v)
					v.Rate.Rate = "0.02"
					row["valuation"] = value(v)
				case "unknown-column":
					row["unrecognized_amount"] = json.RawMessage("1")
				}
			}
			copy.Tables["lines"] = value(rows)
			target := testutil.New(t)
			err := target.Owner.Restore(target.Context, copy)
			if mode == "valid" {
				if err != nil {
					t.Fatal("populated currency restore failed", err)
				}
				got, err := target.Owner.Export(target.Context, book.ID)
				if err != nil || !bytes.Equal(value(want), value(got)) {
					t.Fatal("currency round trip changed accepted records", err)
				}
			} else if err == nil {
				t.Fatal("inconsistent financial snapshot restored", mode)
			}
		})
	}
	vault, _ := restoreVault(t)
	var archive bytes.Buffer
	if err = db.Owner.WriteArchive(db.Context, vault, &archive, ""); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	destination, _ := restoreVault(t)
	if err = target.Owner.RestoreArchive(target.Context, destination, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("populated currency archive restore failed", err)
	}
	got, err := target.Owner.Export(target.Context, book.ID)
	if err != nil || !bytes.Equal(value(want), value(got)) {
		t.Fatal("archive lost measurement basis", err)
	}
}
func TestCurrencyHistoricalNativeOverflowCannotBeHiddenByUnitChange(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	var first ledger.Entry
	var firstInput ledger.PostJournal
	for i, n := range []string{"9223372036854775806", "1", "1"} {
		q := measuredPost(cash, equity.ID, []string{"initial", "addition", "withdrawal"}[i], n, "0")
		if i == 2 {
			q.Lines[0].Side, q.Lines[1].Side = "credit", "debit"
		}
		result := db.Call(t, s, "post_journal", q)
		if i == 0 {
			json.Unmarshal(result, &first)
			firstInput = q
		}
	}
	currency := "EUR"
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "change-unit", AccountID: cash.ID, ExpectedRevision: 1, Code: cash.Code, Name: cash.Name, Currency: &currency, Scale: currencyScale(2)})
	snapshot, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	control := testutil.New(t)
	if err = control.Owner.Restore(control.Context, snapshot); err != nil {
		t.Fatal("valid history did not restore before corruption", err)
	}
	first.Lines[0].Native.AmountMinor = "9223372036854775807"
	firstInput.Lines[0].Native.AmountMinor = "9223372036854775807"
	for _, table := range []string{"lines", "requests"} {
		var rows []map[string]json.RawMessage
		json.Unmarshal(snapshot.Tables[table], &rows)
		for _, row := range rows {
			var id string
			json.Unmarshal(row["id"], &id)
			if table == "lines" && id == first.Lines[0].ID {
				row["native_amount_minor"] = json.RawMessage("9223372036854775807")
			}
			if table == "requests" && id == first.ReceiptID {
				row["result_bytes"] = value("\\x" + hex.EncodeToString(value(first)))
				row["request_bytes"] = value("\\x" + hex.EncodeToString(value(firstInput)))
				hash := sha256.Sum256(value(firstInput))
				row["request_sha256"] = value(hex.EncodeToString(hash[:]))
			}
		}
		snapshot.Tables[table] = value(rows)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, snapshot); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("historical native overflow escaped after a unit change", err)
	}
}

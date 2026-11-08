package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

const splitRecordedBookSHA256 = "52bc3c3cb64f0223603f22133e9af6326f88bdee2866ed26b051769c7e20489b"

func TestOlderArchiveRestoresBookWithFixedAddress(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedSchema)
	book, _ := db.Book(t, "Synthetic archived Book")
	snapshot := db.ReleasedSnapshot(t)
	for _, table := range []string{"documents", "document_uploads", "source_citations", "evidence_links"} {
		snapshot.Tables[table] = json.RawMessage("[]")
	}
	snapshot.Format = "vult-documents-backup-v1"
	hashes := map[string]string{}
	if err := db.Admin.QueryRow(db.Context, "SELECT jsonb_object_agg(filename,sha256) FROM public.schema_receipts").Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(currencyArchive(t, snapshot, hashes)), nil); err != nil {
		t.Fatal("earlier archive did not restore", err)
	}
	owner, err := target.Owner.ForTenant(target.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := owner.BookAddress(target.Context, book.ID)
	if err != nil || addr.BookID != book.ID || addr.Revision != 0 || len(addr.Code) != 4 {
		t.Fatal("restored Book has no allocated fixed address", addr, err)
	}
	view, err := owner.BookIdentity(target.Context, book.ID, 0)
	if err != nil || len(view.History) != 0 || view.Subject.Revision != 0 || view.Subject.Kind != nil {
		t.Fatal("older archive acquired an invented legal identity", view, err)
	}
}

func TestNewArchiveRequiresRetainedBookAddress(t *testing.T) {
	db := testutil.New(t)
	if _, err := db.Owner.CreateBook(db.Context, "Synthetic archived Book", "USD", 2); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"documents", "document_uploads", "source_citations", "evidence_links", "book_addresses"} {
		snapshot.Tables[table] = json.RawMessage("[]")
	}
	snapshot.Format = "vult-documents-backup-v5"
	hashes := map[string]string{}
	if err = db.Admin.QueryRow(db.Context, "SELECT jsonb_object_agg(filename,sha256) FROM public.schema_receipts").Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	if err = target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(currencyArchive(t, snapshot, hashes)), nil); err != ledger.ErrInvalid {
		t.Fatal("archive silently replaced a missing Book address", err)
	}
	books, err := target.Owner.Books(target.Context)
	if err != nil || len(books) != 0 {
		t.Fatal("failed restore left partial Books", err)
	}
}

func TestPortableArchiveRequiresTheRecordedBookMigration(t *testing.T) {
	source := testutil.New(t)
	book, _ := source.Book(t, "Example archive Book")
	var archive bytes.Buffer
	vault, _ := restoreVault(t)
	if err := source.Owner.WriteArchive(source.Context, vault, &archive, book.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := archiveManifestOf(t, archive.Bytes()).Snapshot
	const folded = "0004_recorded_book.sql"
	var receipts map[string]string
	if err := source.Admin.QueryRow(source.Context, "SELECT jsonb_object_agg(filename,sha256) FROM public.schema_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if snapshot.Format != "vult-documents-backup-v5" || len(receipts) != 4 || receipts[folded] == "" {
		t.Fatal("the archive did not identify the current Book format")
	}
	for _, mode := range []string{"valid", "changed", "missing", "gap", "prerelease", "split-prerelease", "extra", "wrong-format"} {
		t.Run(mode, func(t *testing.T) {
			hashes, input := maps.Clone(receipts), snapshot
			switch mode {
			case "changed":
				hashes[folded] = hashes["0003_documents.sql"]
			case "missing":
				delete(hashes, folded)
			case "gap":
				delete(hashes, "0002_server_access.sql")
			case "prerelease":
				delete(hashes, folded)
				hashes["0004_currency_measurements.sql"] = "2ded0411828fc716757580e62a6b075ff20250a4ad952f21afc9ce9b3ec2181d"
			case "split-prerelease":
				hashes[folded] = splitRecordedBookSHA256
				hashes["0005_owner_document_uploads.sql"] = "0bc0d8b59186554c876a70525ca826bdf069d039300c01d18b25d4f58810eb1b"
			case "extra":
				hashes["0006_unknown.sql"] = hashes[folded]
			case "wrong-format":
				input.Format = "vult-documents-backup-v1"
			}
			data := archive.Bytes()
			if mode != "valid" {
				data = currencyArchive(t, input, hashes)
			}
			target := testutil.New(t)
			destination, _ := restoreVault(t)
			err := target.Owner.RestoreArchive(target.Context, destination, bytes.NewReader(data), nil)
			if mode == "valid" {
				if err != nil {
					t.Fatal("fresh archive could not be restored", err)
				}
				return
			}
			if !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("unrecognized archive history accepted", err)
			}
			books, err := target.Owner.Books(target.Context)
			if err != nil || len(books) != 0 {
				t.Fatal("rejected archive left partial records", err)
			}
		})
	}
}

func TestUpgradeRefusesSupersededMigrationReceipts(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedSchema)
	book, scope := db.Book(t, "Example Book")
	debit := db.ReleasedAccount(t, scope, "1000", "asset")
	credit := db.ReleasedAccount(t, scope, "3000", "equity")
	db.ReleasedPost(t, scope, post(debit.ID, credit.ID, "funding"))
	before := db.ReleasedExport(t, book.ID)
	for _, receipt := range []struct{ name, sum string }{
		{"0004_currency_measurements.sql", "2ded0411828fc716757580e62a6b075ff20250a4ad952f21afc9ce9b3ec2181d"},
		{"0004_recorded_book.sql", splitRecordedBookSHA256},
	} {
		if _, err := db.Admin.Exec(db.Context, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", receipt.name, receipt.sum); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err == nil || err.Error() != "installed schema does not match this release" {
			t.Fatal("superseded migration receipt was not rejected", receipt.name, err)
		}
		if _, err := db.Admin.Exec(db.Context, "DELETE FROM public.schema_receipts WHERE filename=$1", receipt.name); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.ReleasedExport(t, book.ID); !bytes.Equal(value(got), value(before)) {
		t.Fatal("a rejected upgrade changed recorded data")
	}
}

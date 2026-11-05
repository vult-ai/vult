package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func expiryStorage(t *testing.T, db *testutil.Database) (string, *documents.KeyDirectory) {
	t.Helper()
	objects, keys := privateArchiveDirectories(t)
	vault, closer, err := server.OpenDocuments(&server.DocumentStorage{Directory: objects, KeyDirectory: keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closer.Close() })
	db.Store.Documents = vault
	return objects, vault.Keys.(*documents.KeyDirectory)
}

func expiryOriginal(t *testing.T, db *testutil.Database, scope ledger.Scope, complete bool) ledger.DocumentIntake {
	t.Helper()
	data := []byte("Example original")
	sum := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if err := json.Unmarshal(db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}), &intake); err != nil {
		t.Fatal(err)
	}
	if complete {
		if _, err := db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	return intake
}

func ageExpiryFixture(t *testing.T, db *testutil.Database, oldestBook string) {
	t.Helper()
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Advance only the isolated fixture's document deadlines, then restore the
	// real guard before invoking the operator or restricted runtime.
	for _, sql := range []string{"ALTER TABLE public.documents DISABLE TRIGGER document_guard", "UPDATE public.documents SET expires_at=clock_timestamp()-interval '1 minute'"} {
		if _, err = tx.Exec(db.Context, sql); err != nil {
			t.Fatal(err)
		}
	}
	if oldestBook != "" {
		if _, err = tx.Exec(db.Context, "UPDATE public.documents SET expires_at=clock_timestamp()-interval '1 hour' WHERE book_id=$1", oldestBook); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(db.Context, "SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE public.documents ENABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal(err)
	}
}

func expiryCommand(t *testing.T, db *testutil.Database, q request) expiryResult {
	t.Helper()
	output := filepath.Join(t.TempDir(), "expiry.json")
	if err := offlineArchiveCommand(t, db, "expire-documents", q, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result expiryResult
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOfflineExpiryNeedsNoKeysAndPreservesUsedOriginals(t *testing.T) {
	db := testutil.New(t)
	objects, keys := expiryStorage(t, db)
	var usedScope ledger.Scope
	var used ledger.DocumentIntake
	for i, subject := range []string{"alpha", "beta"} {
		tenant, err := (access.Operator{DB: db.Admin}).BindOwner(db.Context, "", access.Identity{Issuer: "https://identity.example.com", Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		if err = keys.Provision(db.Context, tenant); err != nil {
			t.Fatal(err)
		}
		owner, err := db.Owner.ForTenant(db.Context, tenant)
		if err != nil {
			t.Fatal(err)
		}
		book, err := owner.CreateBook(db.Context, "Example Book", "USD", 2)
		if err != nil {
			t.Fatal(err)
		}
		connection, err := owner.CreateConnection(db.Context, book.ID, "Example agent", "write")
		if err != nil {
			t.Fatal(err)
		}
		scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
		intake := expiryOriginal(t, db, scope, true)
		if i == 0 {
			debit, credit := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
			db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "opening", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example opening", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "100"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "100"}}, Sources: []ledger.SourceInput{{DocumentID: intake.Document.ID}}})
			usedScope, used = scope, intake
		}
	}
	ageExpiryFixture(t, db, "")
	result := expiryCommand(t, db, request{Directory: objects})
	if result.Books != 1 || result.Expired != 1 || result.More {
		t.Fatal("expiry did not reclaim exactly the unused tenant's original", result)
	}
	var original bytes.Buffer
	if err := db.Store.Download(db.Context, usedScope, used.Document.ID, &original); err != nil || original.String() != "Example original" {
		t.Fatal("worker removed a used original", err)
	}
	if files, err := os.ReadDir(objects); err != nil || len(files) != 1 {
		t.Fatal("wrong ciphertext remained after maintenance", err)
	}
	if repeat := expiryCommand(t, db, request{Directory: objects, BookID: usedScope.BookID}); repeat.Expired != 0 || repeat.More {
		t.Fatal("per-Book maintenance changed permanent evidence", repeat)
	}
}

func TestOfflineExpiryBoundsBooksAndReportsRemainingWork(t *testing.T) {
	db := testutil.New(t)
	objects, keys := expiryStorage(t, db)
	var oldestBook, oldestDocument string
	for i := 0; i < 21; i++ {
		_, scope := db.Book(t, "Example Book")
		if err := keys.Provision(db.Context, scope.TenantID); err != nil {
			t.Fatal(err)
		}
		intake := expiryOriginal(t, db, scope, false)
		if scope.BookID > oldestBook {
			oldestBook, oldestDocument = scope.BookID, intake.Document.ID
		}
	}
	// The last Book in UUID order must not wait behind newer expired content.
	ageExpiryFixture(t, db, oldestBook)
	first := expiryCommand(t, db, request{Directory: objects})
	if first.Books != 20 || first.Expired != 20 || !first.More {
		t.Fatal("unbounded or falsely completed maintenance", first)
	}
	var state string
	if err := db.Admin.QueryRow(db.Context, "SELECT state FROM public.documents WHERE id=$1", oldestDocument).Scan(&state); err != nil || state != "expired" {
		t.Fatal("older content was starved by Book ID order", err)
	}
	second := expiryCommand(t, db, request{Directory: objects})
	if second.Books != 1 || second.Expired != 1 || second.More {
		t.Fatal("repeated maintenance did not finish remaining work", second)
	}
	if last := expiryCommand(t, db, request{Directory: objects}); last.Books != 0 || last.Expired != 0 || last.More {
		t.Fatal("empty sweep did not remain empty", last)
	}
}

func TestOfflineExpiryRejectsRestrictedAndRoleSwitchedConnections(t *testing.T) {
	db := testutil.New(t)
	objects, keys := expiryStorage(t, db)
	_, scope := db.Book(t, "Example Book")
	if err := keys.Provision(db.Context, scope.TenantID); err != nil {
		t.Fatal(err)
	}
	expiryOriginal(t, db, scope, false)
	ageExpiryFixture(t, db, "")
	config := db.Admin.Config()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{db.Role}.Sanitize())
		return err
	}
	switched, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer switched.Close()
	for _, pool := range []*pgxpool.Pool{db.Runtime, switched} {
		if _, err = expireDocuments(db.Context, pool, request{Directory: objects}); !errors.Is(err, errOperation) {
			t.Fatal("unauthorized maintenance silently succeeded", err)
		}
	}
	if result := expiryCommand(t, db, request{Directory: objects, BookID: scope.BookID}); result.Expired != 1 {
		t.Fatal("denied callers changed the pending original", result)
	}
}

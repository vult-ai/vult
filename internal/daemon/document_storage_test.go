package daemon

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

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type failingRemoval struct {
	documents.Storage
	object string
	failed bool
}

func (s *failingRemoval) Remove(ctx context.Context, id string) error {
	if id == s.object {
		s.failed = true
		return errors.New("example-private-original.pdf could not be removed")
	}
	return s.Storage.Remove(ctx, id)
}

func TestDocumentMaintenanceContinuesAfterStorageFailureWithoutLeakingDetails(t *testing.T) {
	db := testutil.New(t)
	storage, err := prepareDocumentStorage(db.Context, db.Owner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	db.Store.Documents = storage.vault
	data := []byte("Example original")
	hash := sha256.Sum256(data)
	var ids []string
	for i := 0; i < 2; i++ {
		_, scope := db.Book(t, "Example Book")
		var intake ledger.DocumentIntake
		if err = json.Unmarshal(db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, intake.Document.ID)
	}
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Change only isolated fixture deadlines, then reinstate the real guard.
	if _, err = tx.Exec(db.Context, "ALTER TABLE public.documents DISABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, `UPDATE public.documents SET expires_at=clock_timestamp()-interval '1 minute'
 - CASE WHEN id=$1 THEN interval '1 hour' ELSE interval '0' END`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE public.documents ENABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal(err)
	}
	var object string
	if err = db.Admin.QueryRow(db.Context, "SELECT storage_id FROM public.documents WHERE id=$1", ids[0]).Scan(&object); err != nil {
		t.Fatal(err)
	}
	failing := &failingRemoval{Storage: storage.disk, object: object}
	var diagnostics bytes.Buffer
	runDocumentMaintenance(db.Context, db.Owner, failing, &diagnostics)
	if !failing.failed || diagnostics.String() != "document maintenance could not finish every Book; it will retry\n" {
		t.Fatal("maintenance did not report a safe retryable failure")
	}
	for i, id := range ids {
		var state string
		if err = db.Admin.QueryRow(db.Context, "SELECT state FROM public.documents WHERE id=$1", id).Scan(&state); err != nil || state != []string{"ready", "expired"}[i] {
			t.Fatal("failed Book blocked another Book or lost retry state", err)
		}
	}
	diagnostics.Reset()
	runDocumentMaintenance(db.Context, db.Owner, storage.disk, &diagnostics)
	var remaining int
	if err = db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.documents WHERE state<>'expired'").Scan(&remaining); err != nil || remaining != 0 || diagnostics.Len() != 0 {
		t.Fatal("maintenance did not recover on the next sweep", err)
	}
}

func TestServiceDocumentKeysPersistAndMissingKeysRequireRecovery(t *testing.T) {
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	dataRoot := t.TempDir()
	storage, err := prepareDocumentStorage(db.Context, db.Owner, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.Store.Documents = storage.vault
	data := []byte("synthetic original")
	hash := sha256.Sum256(data)
	result := db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])})
	var intake ledger.DocumentIntake
	if json.Unmarshal(result, &intake) != nil {
		t.Fatal("missing intake")
	}
	if _, err = db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = prepareDocumentStorage(db.Context, db.Owner, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.Store.Documents = storage.vault
	var returned bytes.Buffer
	if err = db.Store.Download(db.Context, scope, intake.Document.ID, &returned); err != nil || !bytes.Equal(returned.Bytes(), data) {
		t.Fatal("restart changed document/key", err)
	}
	storage.Close()
	keyPath := filepath.Join(dataRoot, "document-keys")
	entries, err := os.ReadDir(keyPath)
	if err != nil || len(entries) != 1 {
		t.Fatal("missing fixture key", err)
	}
	if err = os.Remove(filepath.Join(keyPath, entries[0].Name())); err != nil {
		t.Fatal(err)
	}
	if replacement, err := prepareDocumentStorage(db.Context, db.Owner, dataRoot); err == nil {
		replacement.Close()
		t.Fatal("service silently replaced a lost original key")
	}
	if entries, err = os.ReadDir(keyPath); err != nil || len(entries) != 0 {
		t.Fatal("recovery failure generated a new key", err)
	}
}

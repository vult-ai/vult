package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func offlineArchiveCommand(t *testing.T, db *testutil.Database, command string, q request, output string) error {
	t.Helper()
	var console bytes.Buffer
	err := offlineArchiveCommandTo(t, db, command, q, output, &console)
	if bytes.Contains(console.Bytes(), []byte("AGE-SECRET")) {
		t.Fatal("secret key reached console")
	}
	return err
}

func offlineArchiveCommandTo(t *testing.T, db *testutil.Database, command string, q request, output string, console io.Writer) error {
	t.Helper()
	directory := t.TempDir()
	dsn, input := filepath.Join(directory, "operator.dsn"), filepath.Join(directory, "input.json")
	if err := os.WriteFile(dsn, []byte(testutil.DSN(db.Admin)), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(q)
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	return run(db.Context, []string{command, "--dsn-file", dsn, "--input", input, "--output", output}, console)
}

type closedConsole struct{}

func (closedConsole) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func privateArchiveDirectories(t *testing.T) (string, string) {
	t.Helper()
	objects, keys := t.TempDir(), t.TempDir()
	for _, path := range []string{objects, keys} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return objects, keys
}

func TestOfflineArchiveCommandsRestoreOriginalsUnderFreshKeys(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	objects, keys := privateArchiveDirectories(t)
	outputRoot := t.TempDir()
	if err := offlineArchiveCommand(t, db, "document-key", request{TenantID: scope.TenantID, KeyDirectory: keys}, filepath.Join(outputRoot, "provision.json")); err != nil {
		t.Fatal("offline tenant key failed", err)
	}
	vault, closer, err := server.OpenDocuments(&server.DocumentStorage{Directory: objects, KeyDirectory: keys})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	db.Store.Documents = vault
	data := bytes.Repeat([]byte("synthetic original\n"), 10000)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid intake")
	}
	if _, err = db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(outputRoot, "recovery.key")
	if err = os.WriteFile(keyFile, []byte("synthetic-backup-key-12345678901234567890"), 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(outputRoot, "books.age")
	q := request{Directory: objects, KeyDirectory: keys, RecoveryKeyFile: keyFile}
	if err = offlineArchiveCommand(t, db, "backup", q, backup); err != nil {
		t.Fatal("offline encrypted backup failed", err)
	}
	cipher, err := os.ReadFile(backup)
	if err != nil || bytes.Contains(cipher, data[:100]) {
		t.Fatal("backup missing or unencrypted", err)
	}
	if err = offlineArchiveCommand(t, db, "backup", q, backup); err == nil {
		t.Fatal("backup overwrote prior output")
	}
	q.BookID = book.ID
	if err = offlineArchiveCommand(t, db, "export", q, filepath.Join(outputRoot, "book.tar")); err != nil {
		t.Fatal("offline portable export failed", err)
	}
	target := testutil.New(t)
	targetObjects, targetKeys := privateArchiveDirectories(t)
	q = request{Directory: targetObjects, KeyDirectory: targetKeys, RecoveryKeyFile: keyFile, ArchiveFile: backup}
	wrongKey := filepath.Join(outputRoot, "wrong.key")
	if err = os.WriteFile(wrongKey, []byte("another-synthetic-key-12345678901234567890"), 0600); err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(outputRoot, "truncated.age")
	if err = os.WriteFile(truncated, cipher[:len(cipher)-7], 0600); err != nil {
		t.Fatal(err)
	}
	for _, failed := range []struct {
		name, key, archive string
	}{{"wrong-key", wrongKey, backup}, {"truncated", keyFile, truncated}} {
		t.Run(failed.name, func(t *testing.T) {
			input := q
			input.RecoveryKeyFile, input.ArchiveFile = failed.key, failed.archive
			output := filepath.Join(outputRoot, failed.name+".json")
			if err := offlineArchiveCommand(t, target, "restore", input, output); err == nil {
				t.Fatal("invalid archive restored")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("failed restore published output", err)
			}
			var count int
			if err := target.Admin.QueryRow(target.Context, "SELECT count(*) FROM public.books").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed restore retained database rows", err)
			}
			if files, err := os.ReadDir(targetObjects); err != nil || len(files) != 0 {
				t.Fatal("failed restore retained original bytes", err)
			}
		})
	}
	if err = offlineArchiveCommandTo(t, target, "restore", q, filepath.Join(outputRoot, "restored.json"), closedConsole{}); err != nil {
		t.Fatal("offline restore failed", err)
	}
	if result, err := os.ReadFile(filepath.Join(outputRoot, "restored.json")); err != nil || !bytes.Contains(result, []byte(`"status":"restored"`)) {
		t.Fatal("failed console lost committed restore result", err)
	}
	targetVault, targetCloser, err := server.OpenDocuments(&server.DocumentStorage{Directory: targetObjects, KeyDirectory: targetKeys})
	if err != nil {
		t.Fatal(err)
	}
	defer targetCloser.Close()
	connections, err := target.Owner.Connections(target.Context)
	if err != nil || len(connections) != 0 {
		t.Fatal("offline restore revived grants", err)
	}
	connection, err := target.Owner.CreateConnection(target.Context, book.ID, "Example restored agent", "read")
	if err != nil {
		t.Fatal(err)
	}
	read := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}
	target.Store.Documents = targetVault
	var returned bytes.Buffer
	if err = target.Store.Download(target.Context, read, intake.Document.ID, &returned); err != nil || !bytes.Equal(returned.Bytes(), data) {
		t.Fatal("offline restore changed original bytes", err)
	}
	oldKeys := documents.Vault{Storage: targetVault.Storage, Keys: vault.Keys}
	// Compare through the restored metadata, then check its actual ciphertext key.
	entries, err := os.ReadDir(targetObjects)
	if err != nil || len(entries) != 1 {
		t.Fatal("missing restored object", err)
	}
	var storageID, keyID string
	if err = target.Admin.QueryRow(target.Context, "SELECT storage_id,key_id FROM public.documents WHERE id=$1", intake.Document.ID).Scan(&storageID, &keyID); err != nil {
		t.Fatal(err)
	}
	if err = oldKeys.Read(target.Context, documents.Object{TenantID: scope.TenantID, BookID: scope.BookID, DocumentID: intake.Document.ID, StorageID: storageID, KeyID: keyID, SHA256: intake.Document.SHA256, Size: intake.Document.Size}, &bytes.Buffer{}); err == nil {
		t.Fatal("old key decrypted offline-restored ciphertext")
	}
}

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func archiveService(t *testing.T) (*testutil.Database, *Server, local.Client, *documentStorage) {
	t.Helper()
	db := testutil.New(t)
	storage, err := prepareDocumentStorage(db.Context, db.Owner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	db.Store.Documents = storage.vault
	s := &Server{Admin: db.Owner, Ledger: db.Store, RecoveryKey: secret(), ProvisionKeys: storage.keys.Provision}
	directory, err := os.MkdirTemp("/tmp", "vult-archive-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "service.sock")
	listener, err := Listen(socket)
	if err != nil {
		os.Remove(directory)
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(db.Context)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("archive service did not stop")
		}
		os.Remove(directory)
	})
	return db, s, local.Client{Socket: socket, ExpectedUID: uint32(os.Getuid())}, storage
}

func archiveAuthorization(t *testing.T, s *Server, action string, payload any) string {
	t.Helper()
	data, err := s.authorize(0, marshal(local.Authorization{Action: action, Digest: local.Digest(payload), UID: uint32(os.Getuid())}))
	if err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &grant) != nil || grant.Token == "" {
		t.Fatal("missing synthetic owner grant")
	}
	return grant.Token
}

func TestEncryptedArchiveRoundTripOverOwnerSocketPreservesOriginals(t *testing.T) {
	db, service, client, _ := archiveService(t)
	book, scope := db.Book(t, "Example Book")
	data := bytes.Repeat([]byte("synthetic original\n"), 10000)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid intake")
	}
	if _, err := db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	a := db.Account(t, scope, "1000", "asset")
	b := db.Account(t, scope, "3000", "equity")
	db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "opening", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example opening", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1234"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1234"}}, Sources: []ledger.SourceInput{{DocumentID: intake.Document.ID}}})
	request := local.ArchiveRequest{Encrypted: true}
	authorization := archiveAuthorization(t, service, "archive_backup", request)
	var cipher bytes.Buffer
	if err := client.ArchiveBackup(db.Context, authorization, request, &cipher); err != nil {
		t.Fatal("encrypted socket backup failed", err)
	}
	if bytes.Contains(cipher.Bytes(), data[:100]) {
		t.Fatal("backup exposed original bytes")
	}
	if err := client.ArchiveBackup(db.Context, authorization, request, &bytes.Buffer{}); err == nil {
		t.Fatal("owner grant replayed")
	}
	target, replacement, replacementClient, _ := archiveService(t)
	digest := sha256.Sum256(cipher.Bytes())
	input := local.RestoreRequest{Size: int64(cipher.Len()), SHA256: hex.EncodeToString(digest[:]), RecoveryKey: service.RecoveryKey}
	grant := archiveAuthorization(t, replacement, "archive_restore", input)
	if _, err := replacementClient.ArchiveRestore(target.Context, grant, input, bytes.NewReader(cipher.Bytes())); err != nil {
		t.Fatal("encrypted socket restore failed", err)
	}
	before, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := target.Owner.Export(target.Context, book.ID)
	if err != nil || !bytes.Equal(marshal(before), marshal(after)) {
		t.Fatal("socket recovery changed journal", err)
	}
	connection, err := target.Owner.CreateConnection(target.Context, book.ID, "Example restored agent", "read")
	if err != nil {
		t.Fatal(err)
	}
	readScope := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}
	var returned bytes.Buffer
	if err = target.Store.Download(target.Context, readScope, intake.Document.ID, &returned); err != nil || !bytes.Equal(returned.Bytes(), data) {
		t.Fatal("restored original/key unavailable", err)
	}
	bad, denied, deniedClient, _ := archiveService(t)
	wrong := input
	wrong.SHA256 = string(bytes.Repeat([]byte{'0'}, 64))
	grant = archiveAuthorization(t, denied, "archive_restore", wrong)
	if _, err = deniedClient.ArchiveRestore(bad.Context, grant, wrong, bytes.NewReader(cipher.Bytes())); err == nil {
		t.Fatal("unapproved ciphertext hash committed")
	}
	books, err := bad.Owner.Books(bad.Context)
	if err != nil || len(books) != 0 {
		t.Fatal("failed approved-input check changed installation", err)
	}
}

func TestStreamRestoreReadsLegacyEncryptedBackupAndProvisionsItsTenantKey(t *testing.T) {
	legacy := testutil.New(t)
	book, scope := legacy.Book(t, "Example Book")
	legacy.Account(t, scope, "1000", "asset")
	snapshot, err := legacy.Owner.Snapshot(legacy.Context)
	if err != nil {
		t.Fatal(err)
	}
	key := secret()
	cipher, err := encryptBackup(marshal(snapshot), key)
	if err != nil {
		t.Fatal(err)
	}
	target, service, client, storage := archiveService(t)
	digest := sha256.Sum256(cipher)
	input := local.RestoreRequest{Size: int64(len(cipher)), SHA256: hex.EncodeToString(digest[:]), RecoveryKey: key}
	grant := archiveAuthorization(t, service, "archive_restore", input)
	if _, err = client.ArchiveRestore(target.Context, grant, input, bytes.NewReader(cipher)); err != nil {
		t.Fatal("legacy backup not recoverable", err)
	}
	if _, err = storage.keys.Current(target.Context, book.TenantID); err != nil {
		t.Fatal("legacy restore needed a restart to provision keys", err)
	}
	after, err := target.Owner.Snapshot(target.Context)
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range snapshot.Tables {
		if !bytes.Equal(rows, after.Tables[name]) {
			t.Fatal("legacy restore changed rows", name)
		}
	}
	oldTarget, oldService, oldClient, oldStorage := archiveService(t)
	oldRequest := struct {
		Data        []byte `json:"data"`
		RecoveryKey string `json:"recovery_key"`
	}{cipher, key}
	oldGrant := archiveAuthorization(t, oldService, "restore", oldRequest)
	if _, err = oldClient.Call(oldTarget.Context, "restore", oldGrant, oldRequest); err != nil {
		t.Fatal("old JSON restore wire failed", err)
	}
	if _, err = oldStorage.keys.Current(oldTarget.Context, book.TenantID); err != nil {
		t.Fatal("old JSON restore omitted destination key provisioning", err)
	}
}

func TestNativeArchiveRestoreRejectsMultipleTenantsBeforeImport(t *testing.T) {
	source, _, _, storage := archiveService(t)
	source.Book(t, "Example Book")
	other := ledger.ID()
	if _, err := source.Admin.Exec(source.Context, "INSERT INTO public.tenants(id)VALUES($1)", other); err != nil {
		t.Fatal(err)
	}
	owner, err := source.Owner.ForTenant(source.Context, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.CreateBook(source.Context, "Other Example Book", "USD", 2); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = source.Owner.WriteArchive(source.Context, storage.vault, &archive, ""); err != nil {
		t.Fatal(err)
	}
	key := secret()
	cipher, err := encryptBackup(archive.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	target, service, client, _ := archiveService(t)
	digest := sha256.Sum256(cipher)
	input := local.RestoreRequest{Size: int64(len(cipher)), SHA256: hex.EncodeToString(digest[:]), RecoveryKey: key}
	grant := archiveAuthorization(t, service, "archive_restore", input)
	if _, err = client.ArchiveRestore(target.Context, grant, input, bytes.NewReader(cipher)); err == nil {
		t.Fatal("single-owner native service imported multiple tenants")
	}
	books, err := target.Owner.Books(target.Context)
	if err != nil || len(books) != 0 {
		t.Fatal("denied native restore imported Books", err)
	}
	snapshot, err := source.Owner.Snapshot(source.Context)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := encryptBackup(marshal(snapshot), key)
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := struct {
		Data        []byte `json:"data"`
		RecoveryKey string `json:"recovery_key"`
	}{legacy, key}
	grant = archiveAuthorization(t, service, "restore", oldRequest)
	if _, err = client.Call(target.Context, "restore", grant, oldRequest); err == nil {
		t.Fatal("old JSON wire bypassed single-tenant restore rule")
	}
	books, err = target.Owner.Books(target.Context)
	if err != nil || len(books) != 0 {
		t.Fatal("denied old JSON restore imported Books", err)
	}
}

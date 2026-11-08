package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type expiryDocumentKeys struct {
	documents.Keys
	expires atomic.Int64
}

func (k *expiryDocumentKeys) Identity(ctx context.Context, tenant, version string) (age.Identity, error) {
	until := k.expires.Load()
	if until == 0 {
		return k.Keys.Identity(ctx, tenant, version)
	}
	timer := time.NewTimer(max(0, time.Until(time.Unix(0, until).Add(30*time.Millisecond))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return k.Keys.Identity(ctx, tenant, version)
}

type waitingOriginalStore struct {
	documents.Storage
	entered, canceled chan struct{}
}

type waitingUploadStore struct {
	documents.Storage
	entered, canceled chan struct{}
}

func (s waitingUploadStore) Create(ctx context.Context, _ string, _ func(io.Writer) error) error {
	close(s.entered)
	<-ctx.Done()
	close(s.canceled)
	return ctx.Err()
}

func (s waitingOriginalStore) Open(ctx context.Context, _ string) (io.ReadCloser, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.canceled)
	return nil, ctx.Err()
}

func TestOwnerDocumentSocketRequiresPeerSessionAndFinalAuthorization(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("run socket coverage as a regular user")
	}
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	keyJSON, err := documents.NewKeyFile(scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	controlledKeys := &expiryDocumentKeys{Keys: keys}
	vault := &documents.Vault{Storage: disk, Keys: controlledKeys}
	db.Store.Documents = vault
	data := bytes.Repeat([]byte("synthetic original\n"), 12000)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid document fixture")
	}
	if _, err := db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	c, err := newBrowserConfiguration(51999)
	if err != nil {
		t.Fatal(err)
	}
	identity := access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner", AuthenticatedAt: time.Now().UTC()}
	if _, err := (access.Operator{DB: db.Admin}).BindOwner(db.Context, scope.TenantID, identity); err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(db.Context, db.Admin, c.origin())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.NewOwnerDocumentReader(db.Context, db.Runtime, c.origin()); !errors.Is(err, access.ErrDenied) {
		t.Fatal("runtime role acquired operator document reader", err)
	}
	session := access.Random()
	var who string
	if err := db.Admin.QueryRow(db.Context, "SELECT access.issue_session($1,$2,$3,$4,$5)::text", identity.Issuer, identity.Subject, c.origin(), access.Hash(session), identity.AuthenticatedAt).Scan(&who); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	start := func(t *testing.T, browserUID uint32, originals *documents.Vault) local.Client {
		t.Helper()
		directory, err := os.MkdirTemp("/tmp", "vult-owner-document-")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "service.sock")
		listener, err := Listen(path)
		if err != nil {
			os.RemoveAll(directory)
			t.Fatal(err)
		}
		store := db.Store
		store.Documents = originals
		s := &Server{Admin: db.Owner, Ledger: store, Browser: &browserOwner{configuration: c, uid: browserUID, documents: reader}}
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
				t.Error("owner document socket did not stop")
			}
			os.RemoveAll(directory)
		})
		return local.Client{Socket: path, ExpectedUID: uid}
	}
	wrongPeer := start(t, uid+1, vault)
	var denied bytes.Buffer
	if err := wrongPeer.DownloadForOwner(db.Context, session, scope.BookID, intake.Document.ID, &denied); err == nil || denied.Len() != 0 {
		t.Fatal("foreign socket peer reached an original", err)
	}
	conn, err := net.Dial("unix", wrongPeer.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(local.Header{Method: "owner_document_download", Authorization: session, Size: 2}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	reply, err := local.ReadMessage(conn)
	conn.Close()
	var rejection local.Response
	if err != nil || json.Unmarshal(reply, &rejection) != nil || rejection.Error != ledger.ErrDenied.Error() {
		t.Fatal("foreign peer was not rejected before its body", err)
	}
	client := start(t, uid, vault)
	ownerBytes := []byte("date,amount\n2026-01-01,100\n")
	ownerHash := sha256.Sum256(ownerBytes)
	q := ledger.AddDocument{RequestKey: "owner-original", Name: "example.csv", MediaType: "text/csv", Size: int64(len(ownerBytes)), SHA256: hex.EncodeToString(ownerHash[:])}
	if _, err := wrongPeer.UploadForOwner(db.Context, session, scope.BookID, q, bytes.NewReader(ownerBytes)); err == nil {
		t.Fatal("foreign socket peer uploaded an original")
	}
	if _, err := client.UploadForOwner(db.Context, access.Random(), scope.BookID, q, bytes.NewReader(ownerBytes)); err == nil {
		t.Fatal("unauthorized socket token uploaded an original")
	}
	first, err := client.UploadForOwner(db.Context, session, scope.BookID, q, bytes.NewReader(ownerBytes))
	if err != nil || first.Reused || first.Document.ExpiresAt != nil || first.Document.Sequence <= intake.Document.Sequence {
		t.Fatal("owner socket did not record a retained original", err)
	}
	q.RequestKey = "owner-duplicate"
	duplicate, err := client.UploadForOwner(db.Context, session, scope.BookID, q, bytes.NewReader(ownerBytes))
	if err != nil || !duplicate.Reused || duplicate.Document.ID != first.Document.ID {
		t.Fatal("owner socket did not reuse its original", err)
	}
	var ownerOriginal bytes.Buffer
	if err := client.DownloadForOwner(db.Context, session, scope.BookID, first.Document.ID, &ownerOriginal); err != nil || !bytes.Equal(ownerOriginal.Bytes(), ownerBytes) {
		t.Fatal("owner socket upload did not round trip", err)
	}
	var original bytes.Buffer
	if err := client.DownloadForOwner(db.Context, session, scope.BookID, intake.Document.ID, &original); err != nil || !bytes.Equal(original.Bytes(), data) {
		t.Fatal("owner socket changed original or failed", err)
	}
	for _, token := range []string{access.Random(), scope.ConnectionID} {
		denied.Reset()
		if err := client.DownloadForOwner(db.Context, token, scope.BookID, intake.Document.ID, &denied); err == nil || denied.Len() != 0 {
			t.Fatal("non-owner token read original")
		}
	}
	other, err := db.Owner.CreateBook(db.Context, "Another Book", "USD", 2)
	if err != nil {
		t.Fatal(err)
	}
	denied.Reset()
	if err := client.DownloadForOwner(db.Context, session, other.ID, intake.Document.ID, &denied); !errors.Is(err, ledger.ErrNotFound) || denied.Len() != 0 {
		t.Fatal("document crossed Book boundary", err)
	}
	_, err = client.Call(db.Context, "owner_document_download", session, map[string]any{"book_id": scope.BookID, "document_id": intake.Document.ID, "tenant_id": scope.TenantID})
	if err == nil || err.Error() != ledger.ErrInvalid.Error() {
		t.Fatal("owner read accepted a scope override", err)
	}
	t.Run("canceled original releases the Book hold", func(t *testing.T) {
		storage := waitingOriginalStore{Storage: disk, entered: make(chan struct{}), canceled: make(chan struct{})}
		client := start(t, uid, &documents.Vault{Storage: storage, Keys: keys})
		ctx, cancel := context.WithCancel(db.Context)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- client.DownloadForOwner(ctx, session, scope.BookID, intake.Document.ID, io.Discard) }()
		select {
		case <-storage.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("protected original read did not reach storage")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("canceled original succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("download client did not return")
		}
		select {
		case <-storage.canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("daemon retained the original read after the peer disconnected")
		}
		check, stop := context.WithTimeout(db.Context, 2*time.Second)
		defer stop()
		if n, err := db.Owner.ExpireBookDocuments(check, scope.BookID, disk, 100); err != nil || n != 0 {
			t.Fatal("canceled original retained the Book hold", n, err)
		}
	})
	t.Run("canceled upload releases the Book hold and can retry", func(t *testing.T) {
		storage := waitingUploadStore{Storage: disk, entered: make(chan struct{}), canceled: make(chan struct{})}
		blocked := start(t, uid, &documents.Vault{Storage: storage, Keys: keys})
		data := []byte("date,amount\n2026-02-01,100\n")
		hash := sha256.Sum256(data)
		q := ledger.AddDocument{RequestKey: "interrupted", Name: "example.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
		ctx, cancel := context.WithCancel(db.Context)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := blocked.UploadForOwner(ctx, session, scope.BookID, q, bytes.NewReader(data))
			done <- err
		}()
		select {
		case <-storage.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("upload did not reach encrypted storage")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("canceled upload succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("upload client did not return")
		}
		select {
		case <-storage.canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("daemon retained upload after the peer disconnected")
		}
		retry, stop := context.WithTimeout(db.Context, 3*time.Second)
		defer stop()
		result, err := client.UploadForOwner(retry, session, scope.BookID, q, bytes.NewReader(data))
		if err != nil || result.Reused || result.Document.State != "ready" {
			t.Fatal("interrupted upload could not resume its reserved original", err)
		}
	})
	var expires time.Time
	if err := db.Admin.QueryRow(db.Context, "UPDATE access.sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1 RETURNING expires_at", access.Hash(session)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	controlledKeys.expires.Store(expires.UnixNano())
	original.Reset()
	if err := client.DownloadForOwner(db.Context, session, scope.BookID, intake.Document.ID, &original); err == nil {
		t.Fatal("expired owner read produced final confirmation")
	}
	var entries, connections int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.connections)").Scan(&entries, &connections); err != nil || entries != 0 || connections != 1 {
		t.Fatal("read created accounting or agent authority", entries, connections, err)
	}
}

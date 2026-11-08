package ledger_test

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

type documentReadBarrier struct {
	bytes.Buffer
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *documentReadBarrier) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.started); <-w.release })
	return w.Buffer.Write(data)
}

func TestOwnerDownloadHoldsBookAgainstActualExpiry(t *testing.T) {
	db, scope, directory := documentFixture(t)
	doc := readyOriginal(t, db, scope, "unused")
	identity := access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner", AuthenticatedAt: time.Now().UTC()}
	if _, err := (access.Operator{DB: db.Admin}).BindOwner(db.Context, scope.TenantID, identity); err != nil {
		t.Fatal(err)
	}
	const origin = "https://ledger.example.com"
	session := access.Random()
	var who string
	if err := db.Admin.QueryRow(db.Context, "SELECT access.issue_session($1,$2,$3,$4,$5)::text", identity.Issuer, identity.Subject, origin, access.Hash(session), identity.AuthenticatedAt).Scan(&who); err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(db.Context, db.Admin, origin)
	if err != nil {
		t.Fatal(err)
	}
	config := db.Admin.Config()
	config.MaxConns, config.MinConns = 1, 1
	expiryPool, err := pgxpool.NewWithConfig(db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer expiryPool.Close()
	var pid int
	if err := expiryPool.QueryRow(db.Context, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(db.Context, 5*time.Second)
	defer cancel()
	writer := &documentReadBarrier{started: make(chan struct{}), release: make(chan struct{})}
	var released sync.Once
	unblock := func() { released.Do(func() { close(writer.release) }) }
	var workers sync.WaitGroup
	defer func() { unblock(); cancel(); workers.Wait() }()
	readDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		readDone <- reader.Download(ctx, session, scope.BookID, doc.ID, db.Store.Documents, writer)
	}()
	select {
	case <-writer.started:
	case <-ctx.Done():
		t.Fatal("download did not reach original bytes")
	}
	type expiryResult struct {
		count int
		err   error
	}
	expired := make(chan expiryResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		n, err := (ledger.Admin{DB: expiryPool}).ExpireBookDocuments(ctx, scope.BookID, db.Store.Documents.Storage, 100)
		expired <- expiryResult{n, err}
	}()
	blocked := false
	for !blocked {
		if err := db.Admin.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case result := <-expired:
			t.Fatal("expiry bypassed the active original read", result.err)
		case <-ctx.Done():
			t.Fatal("expiry did not wait on the Book hold")
		case <-time.After(5 * time.Millisecond):
		}
	}
	unblock()
	if err := <-readDone; err != nil || writer.String() != "synthetic original unused" {
		t.Fatal("owner download failed or changed bytes", err)
	}
	if result := <-expired; result.err != nil || result.count != 0 {
		t.Fatal("unexpired original was reclaimed", result.err)
	}
	ageDocumentFixture(t, db, doc.ID)
	if n, err := db.Owner.ExpireBookDocuments(db.Context, scope.BookID, db.Store.Documents.Storage, 100); err != nil || n != 1 {
		t.Fatal("expiry did not reclaim after the completed read", n, err)
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 0 {
		t.Fatal("expired original remained after reclamation", err)
	}
}

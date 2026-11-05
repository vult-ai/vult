package ledger_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

func TestOperatorExpiryRespectsBookReadHoldAndPermanentUse(t *testing.T) {
	db, scope, directory := documentFixture(t)
	unused := readyOriginal(t, db, scope, "unused")
	used := readyOriginal(t, db, scope, "used")
	postCitedJournal(t, db, scope, "opening", []ledger.SourceInput{{DocumentID: used.ID}})
	ageDocumentFixture(t, db, unused.ID)
	ageDocumentFixture(t, db, used.ID)
	if _, err := (ledger.Admin{DB: db.Runtime}).ExpireBookDocuments(db.Context, scope.BookID, db.Store.Documents.Storage, 100); err == nil {
		t.Fatal("runtime obtained operator maintenance authority")
	}
	if _, err := (ledger.Admin{DB: db.Runtime}).ExpiredDocumentBooks(db.Context, 20); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("runtime obtained installation maintenance selection", err)
	}
	hold, err := db.Admin.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(context.Background())
	if _, err = hold.Exec(db.Context, "SELECT id FROM public.books WHERE id=$1 FOR SHARE", scope.BookID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(db.Context, 50*time.Millisecond)
	_, err = db.Owner.ExpireBookDocuments(bounded, scope.BookID, db.Store.Documents.Storage, 100)
	cancel()
	if err == nil {
		t.Fatal("expiry bypassed Book read/backup hold")
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 2 {
		t.Fatal("held expiry removed originals", err)
	}
	if err = hold.Rollback(db.Context); err != nil {
		t.Fatal(err)
	}
	if count, err := db.Owner.ExpireBookDocuments(db.Context, scope.BookID, db.Store.Documents.Storage, 100); err != nil || count != 1 {
		t.Fatal("operator expiry did not reclaim exactly unused original", count, err)
	}
	if err = db.Store.Download(db.Context, scope, used.ID, io.Discard); err != nil {
		t.Fatal("maintenance removed used original", err)
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 1 {
		t.Fatal("wrong originals remained", err)
	}
}

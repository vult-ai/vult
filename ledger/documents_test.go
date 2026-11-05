package ledger_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type documentKeys struct{ key *age.X25519Identity }

func (k documentKeys) Current(context.Context, string) (string, error) { return "primary", nil }
func (k documentKeys) Recipient(context.Context, string, string) (age.Recipient, error) {
	return k.key.Recipient(), nil
}
func (k documentKeys) Identity(context.Context, string, string) (age.Identity, error) {
	return k.key, nil
}

func documentFixture(t *testing.T) (*testutil.Database, ledger.Scope, string) {
	t.Helper()
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
	t.Cleanup(func() { disk.Close() })
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	db.Store.Documents = &documents.Vault{Storage: disk, Keys: documentKeys{key}}
	return db, scope, dir
}

func documentRequest(key string, data []byte) ledger.AddDocument {
	hash := sha256.Sum256(data)
	return ledger.AddDocument{RequestKey: key, Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
}
func prepareDocument(t *testing.T, db *testutil.Database, s ledger.Scope, key string, data []byte) ledger.DocumentIntake {
	t.Helper()
	result := db.Call(t, s, "add_document", documentRequest(key, data))
	var intake ledger.DocumentIntake
	if json.Unmarshal(result, &intake) != nil || intake.Document.ID == "" {
		t.Fatal("missing document intake")
	}
	return intake
}

func TestDocumentIntakeVerifiesBeforeCompletionAndReusesExactOutcome(t *testing.T) {
	db, s, dir := documentFixture(t)
	ctx := db.Context
	data := bytes.Repeat([]byte("synthetic document\n"), 1000)
	if _, err := db.Admin.Exec(ctx, "CREATE TABLE public.synthetic_admission(operation text NOT NULL);GRANT INSERT ON public.synthetic_admission TO "+pgx.Identifier{db.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	db.Store.AdmitWrite = func(ctx context.Context, tx pgx.Tx, _ ledger.Scope, operation string, _ json.RawMessage) error {
		_, err := tx.Exec(ctx, "INSERT INTO public.synthetic_admission VALUES($1)", operation)
		return err
	}
	first := prepareDocument(t, db, s, "first", data)
	if first.Document.State != "pending" || first.UploadID == "" {
		t.Fatal("intake was presented as completed")
	}
	var partial bytes.Buffer
	if err := db.Store.Download(ctx, s, first.Document.ID, &partial); !errors.Is(err, ledger.ErrNotFound) || partial.Len() != 0 {
		t.Fatal("pending document was readable", err)
	}
	if _, err := db.Store.Upload(ctx, s, first.UploadID, bytes.NewReader(data[:len(data)-1])); !errors.Is(err, documents.ErrIntegrity) {
		t.Fatal("short original was accepted", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatal("failed upload published ciphertext", err)
	}
	var completed int
	if err := db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.requests WHERE operation='complete_document_upload'").Scan(&completed); err != nil || completed != 0 {
		t.Fatal("failed upload recorded success", err)
	}
	second := prepareDocument(t, db, s, "second", data)
	if second.Document.ID != first.Document.ID || second.UploadID == first.UploadID {
		t.Fatal("deduplication changed the document or reused an intake")
	}
	recorded, err := db.Store.Upload(ctx, s, first.UploadID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var doc ledger.Document
	if json.Unmarshal(recorded, &doc) != nil || doc.State != "ready" {
		t.Fatal("verified upload did not complete")
	}
	retry, err := db.Store.Upload(ctx, s, first.UploadID, bytes.NewReader(data))
	if err != nil || !bytes.Equal(retry, recorded) {
		t.Fatal("upload retry changed its retained result", err)
	}
	if _, err := db.Store.Upload(ctx, s, second.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	third := prepareDocument(t, db, s, "third", data)
	if third.UploadID != "" || third.Document.ID != doc.ID || third.Document.State != "ready" {
		t.Fatal("ready original was not deduplicated")
	}
	var reservations, completions int
	if err := db.Admin.QueryRow(ctx, "SELECT count(*) FILTER(WHERE operation='add_document'),count(*) FILTER(WHERE operation='complete_document_upload') FROM public.synthetic_admission").Scan(&reservations, &completions); err != nil || reservations != 1 || completions != 2 {
		t.Fatal("reservation/completion admission did not roll back or reuse exact outcomes", reservations, completions, err)
	}
	var returned bytes.Buffer
	if err := db.Store.Download(ctx, s, doc.ID, &returned); err != nil || !bytes.Equal(returned.Bytes(), data) {
		t.Fatal("original bytes changed", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
		t.Fatal("duplicate storage objects", err)
	}
	changed := documentRequest("first", data)
	changed.Name = "different.pdf"
	b, _ := json.Marshal(changed)
	if _, err := db.Store.Call(ctx, s, "add_document", b); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("conflicting retry accepted", err)
	}
}

func TestDocumentCompletionRechecksAdmissionButRetainedResultDoesNot(t *testing.T) {
	db, s, dir := documentFixture(t)
	data := []byte("synthetic original")
	first := prepareDocument(t, db, s, "first", data)
	second := prepareDocument(t, db, s, "second", data)
	db.Store.AdmitWrite = func(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error { return ledger.ErrDenied }
	if _, err := db.Store.Upload(db.Context, s, first.UploadID, bytes.NewReader(data)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("new completion bypassed changed admission", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatal("denied completion wrote document bytes", err)
	}
	db.Store.AdmitWrite = nil
	recorded, err := db.Store.Upload(db.Context, s, first.UploadID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	db.Store.AdmitWrite = func(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error { return ledger.ErrDenied }
	retry, err := db.Store.Upload(db.Context, s, first.UploadID, bytes.NewReader(data))
	if err != nil || !bytes.Equal(retry, recorded) {
		t.Fatal("changed admission hid retained completion", err)
	}
	if _, err := db.Store.Upload(db.Context, s, second.UploadID, bytes.NewReader(data)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("new deduplicated completion bypassed admission", err)
	}
}

func TestDocumentReadWriteRevocationAndBookBoundaries(t *testing.T) {
	db, s, _ := documentFixture(t)
	ctx := db.Context
	data := []byte("synthetic document")
	intake := prepareDocument(t, db, s, "upload", data)
	if _, err := db.Store.Upload(ctx, s, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	_, other := db.Book(t, "Other Example Book")
	otherIntake := prepareDocument(t, db, other, "other", data)
	if otherIntake.Document.ID == intake.Document.ID {
		t.Fatal("identical content in two Books shared document identity")
	}
	if _, err := db.Store.Upload(ctx, other, otherIntake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []ledger.Scope{other, {TenantID: ledger.ID(), BookID: s.BookID, ActorID: s.ActorID, ConnectionID: s.ConnectionID, Access: "write"}} {
		var out bytes.Buffer
		if err := db.Store.Download(ctx, scope, intake.Document.ID, &out); err == nil || out.Len() != 0 {
			t.Fatal("cross-scope original was read")
		}
		if _, err := db.Store.Upload(ctx, scope, intake.UploadID, bytes.NewReader(data)); err == nil {
			t.Fatal("cross-scope upload accepted")
		}
	}
	connection, err := db.Owner.CreateConnection(ctx, s.BookID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	reader := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}
	var out bytes.Buffer
	if err := db.Store.Download(ctx, reader, intake.Document.ID, &out); err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("reader could not read its Book", err)
	}
	b, _ := json.Marshal(documentRequest("denied", data))
	if _, err := db.Store.Call(ctx, reader, "add_document", b); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("read grant prepared an upload", err)
	}
	if _, err := db.Store.Upload(ctx, reader, intake.UploadID, bytes.NewReader(data)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("read grant completed an upload", err)
	}
	if err := db.Owner.Revoke(ctx, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if err := db.Store.Download(ctx, s, intake.Document.ID, io.Discard); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("revoked connection read an original", err)
	}
	if _, err := db.Store.Upload(ctx, s, intake.UploadID, bytes.NewReader(data)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("revoked connection recovered a write outcome", err)
	}
}

func ageDocumentFixture(t *testing.T, db *testutil.Database, id string) {
	t.Helper()
	tx, err := db.Admin.Begin(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Adjust only the synthetic clock fixture, then restore the real guard
	// before any restricted runtime operation is tested.
	if _, err = tx.Exec(db.Context, "ALTER TABLE public.documents DISABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "UPDATE public.documents SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "ALTER TABLE public.documents ENABLE TRIGGER document_guard"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(db.Context); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredUnusedOriginalCanBeReuploadedWithoutChangingIdentity(t *testing.T) {
	db, s, dir := documentFixture(t)
	data := []byte("synthetic original")
	first := prepareDocument(t, db, s, "first", data)
	originalReceipt, err := db.Store.Upload(db.Context, s, first.UploadID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ageDocumentFixture(t, db, first.Document.ID)
	if err := db.Store.Download(db.Context, s, first.Document.ID, io.Discard); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("expired original remained downloadable", err)
	}
	if count, err := db.Owner.ExpireBookDocuments(db.Context, s.BookID, db.Store.Documents.Storage, 1); err != nil || count != 1 {
		t.Fatal("expiry failed", count, err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatal("expired bytes remained", err)
	}
	second := prepareDocument(t, db, s, "second", data)
	if second.Document.ID != first.Document.ID {
		t.Fatal("reupload changed permanent document identity")
	}
	if replay, err := db.Store.Upload(db.Context, s, first.UploadID, bytes.NewReader(data)); err != nil || !bytes.Equal(replay, originalReceipt) {
		t.Fatal("retained upload outcome changed after expiry", err)
	}
	current := db.Call(t, s, "get_document", ledger.DocumentQuery{DocumentID: second.Document.ID})
	var state ledger.DocumentInspection
	if json.Unmarshal(current, &state) != nil || state.Document.State != "pending" {
		t.Fatal("old receipt completed a replacement upload")
	}
	if _, err := db.Store.Upload(db.Context, s, second.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := db.Store.Download(db.Context, s, second.Document.ID, &out); err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("reupload changed original bytes", err)
	}
}

func TestUsedOriginalNeverExpires(t *testing.T) {
	db, s, dir := documentFixture(t)
	data := []byte("synthetic retained original")
	intake := prepareDocument(t, db, s, "original", data)
	if _, err := db.Store.Upload(db.Context, s, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	postCitedJournal(t, db, s, "retained", []ledger.SourceInput{{DocumentID: intake.Document.ID}})
	ageDocumentFixture(t, db, intake.Document.ID)
	if count, err := db.Owner.ExpireBookDocuments(db.Context, s.BookID, db.Store.Documents.Storage, 100); err != nil || count != 0 {
		t.Fatal("used original expired", count, err)
	}
	if err := db.Store.Download(db.Context, s, intake.Document.ID, io.Discard); err != nil {
		t.Fatal("used original became unavailable", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
		t.Fatal("used original bytes removed", err)
	}
}

func TestConcurrentIdenticalOriginalsHaveOneObject(t *testing.T) {
	db, s, dir := documentFixture(t)
	data := bytes.Repeat([]byte("synthetic original"), 1000)
	a := prepareDocument(t, db, s, "first", data)
	b := prepareDocument(t, db, s, "second", data)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, id := range []string{a.UploadID, b.UploadID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := db.Store.Upload(db.Context, s, id, bytes.NewReader(data))
			failures <- err
		}(id)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 {
		t.Fatal("concurrent uploads duplicated bytes", err)
	}
}

func TestDocumentDatabaseGuardsImmutableFactsAndStaleSnapshots(t *testing.T) {
	db, s, _ := documentFixture(t)
	data := []byte("synthetic original")
	intake := prepareDocument(t, db, s, "first", data)
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: isolation})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(db.Context, "UPDATE public.documents SET state='ready' WHERE id=$1", intake.Document.ID)
		var pgerr *pgconn.PgError
		if isolation != pgx.ReadCommitted && (!errors.As(err, &pgerr) || pgerr.Code != "25001") {
			t.Fatal("stale-snapshot document write accepted", err)
		}
		if err = tx.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"UPDATE public.documents SET original_name='different.pdf' WHERE id=$1", "UPDATE public.documents SET used_at=clock_timestamp() WHERE id=$1", "DELETE FROM public.documents WHERE id=$1"} {
		tx, err := db.Runtime.Begin(db.Context)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(db.Context, query, intake.Document.ID); err == nil {
			t.Fatal("restricted login changed a protected document fact")
		}
		tx.Rollback(context.Background())
	}
}

func TestDatabaseRequiresReceiptBeforeOriginalBecomesReady(t *testing.T) {
	db, s, _ := documentFixture(t)
	intake := prepareDocument(t, db, s, "first", []byte("synthetic original"))
	tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "UPDATE public.documents SET state='ready' WHERE id=$1", intake.Document.ID); err != nil {
		t.Fatal(err)
	}
	err = tx.Commit(db.Context)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("unreceipted original completion committed", err)
	}
}

type fullDocumentStorage struct{ documents.Storage }

func (fullDocumentStorage) Create(context.Context, string, func(io.Writer) error) error {
	return errors.New("private full storage location")
}

func TestDocumentQuotaAndStorageFailuresHaveNoCompletedEffects(t *testing.T) {
	db, s, _ := documentFixture(t)
	data := []byte("synthetic original")
	db.Store.AdmitWrite = func(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error { return ledger.ErrDenied }
	input, _ := json.Marshal(documentRequest("capacity", data))
	if _, err := db.Store.Call(db.Context, s, "add_document", input); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("quota refusal was ignored", err)
	}
	var count int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.documents").Scan(&count); err != nil || count != 0 {
		t.Fatal("denied reservation created a document", count, err)
	}
	db.Store.AdmitWrite = nil
	intake := prepareDocument(t, db, s, "storage", data)
	db.Store.Documents.Storage = fullDocumentStorage{db.Store.Documents.Storage}
	if _, err := db.Store.Upload(db.Context, s, intake.UploadID, bytes.NewReader(data)); !errors.Is(err, documents.ErrUnavailable) {
		t.Fatal("storage failure was reported as success", err)
	}
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.documents WHERE state='ready'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed storage completed a document", count, err)
	}
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.requests WHERE operation='complete_document_upload'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed storage recorded a completed receipt", count, err)
	}
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed storage changed the journal", count, err)
	}
}

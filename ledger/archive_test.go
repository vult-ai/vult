package ledger_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type portableManifest struct {
	Snapshot  ledger.Snapshot    `json:"snapshot"`
	Originals []documents.Object `json:"originals"`
}

func archiveManifestOf(t *testing.T, data []byte) portableManifest {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(data))
	header, err := reader.Next()
	if err != nil || header.Name != "manifest.json" {
		t.Fatal("missing archive manifest", err)
	}
	body, err := io.ReadAll(reader)
	var manifest portableManifest
	if err != nil || json.Unmarshal(body, &manifest) != nil {
		t.Fatal("invalid archive manifest", err)
	}
	return manifest
}

func restoreVault(t *testing.T) (*documents.Vault, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return &documents.Vault{Storage: disk, Keys: documentKeys{key}}, directory
}

func TestPortableArchiveRekeysAndKeepsEveryPostingAndRelationship(t *testing.T) {
	db, scope, _ := documentFixture(t)
	a := readyOriginal(t, db, scope, "statement")
	b := readyOriginal(t, db, scope, "receipt")
	entry := postCitedJournal(t, db, scope, "opening", []ledger.SourceInput{{DocumentID: a.ID}})
	citation := entryCitations(t, db, scope, entry.ID, false)[0]
	db.Call(t, scope, "correct_source_citation", ledger.CorrectSourceCitation{RequestKey: "citation", SourceID: citation.SourceID, ExpectedRevision: 1, DocumentID: b.ID, Location: ledger.Location{Page: locationNumber(2)}, Reason: "Use the detailed original"})
	var link ledger.EvidenceLink
	if json.Unmarshal(db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "attach", EntryID: entry.ID, DocumentID: b.ID, Role: "receipt", Reason: "Original receipt"}), &link) != nil {
		t.Fatal("invalid evidence result")
	}
	db.Call(t, scope, "detach_evidence", ledger.DetachEvidence{RequestKey: "detach", LinkID: link.LinkID, Reason: "Replace association"})
	db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "reattach", EntryID: entry.ID, DocumentID: b.ID, Role: "receipt", Reason: "Reviewed receipt"})
	expired := readyOriginal(t, db, scope, "unused")
	ageDocumentFixture(t, db, expired.ID)
	if _, err := db.Owner.ExpireBookDocuments(db.Context, scope.BookID, db.Store.Documents.Storage, 100); err != nil {
		t.Fatal(err)
	}
	prepareDocument(t, db, scope, "pending", []byte("synthetic pending original"))
	if _, err := db.Owner.Snapshot(db.Context); err == nil {
		t.Fatal("legacy backup silently omitted documents")
	}
	var archive bytes.Buffer
	var earliest, latest time.Time
	if err := db.Admin.QueryRow(db.Context, "SELECT clock_timestamp()").Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(db.Context, "SELECT clock_timestamp()").Scan(&latest); err != nil {
		t.Fatal(err)
	}
	before := archiveManifestOf(t, archive.Bytes())
	if before.Snapshot.CreatedAt.Before(earliest) || before.Snapshot.CreatedAt.After(latest) {
		t.Fatal("archive cutoff escaped database clock interval")
	}
	if len(before.Originals) != 2 {
		t.Fatal("used originals omitted or unused expiry revived")
	}
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("portable restore failed", err)
	}
	var restored bytes.Buffer
	if err := target.Owner.WriteArchive(target.Context, vault, &restored, ""); err != nil {
		t.Fatal(err)
	}
	after := archiveManifestOf(t, restored.Bytes())
	for _, table := range []string{"tenants", "books", "accounts", "account_revisions", "requests", "entries", "lines", "closed_periods", "source_citations", "evidence_links"} {
		if !bytes.Equal(before.Snapshot.Tables[table], after.Snapshot.Tables[table]) {
			t.Fatal("restore changed retained rows", table)
		}
	}
	for _, old := range before.Originals {
		var replacement documents.Object
		for _, object := range after.Originals {
			if object.DocumentID == old.DocumentID {
				replacement = object
			}
		}
		if replacement.DocumentID == "" || replacement.StorageID == old.StorageID || replacement.SHA256 != old.SHA256 || replacement.Size != old.Size {
			t.Fatal("portable restore lost identity/hash or reused old ciphertext identity")
		}
		var original, returned bytes.Buffer
		if err := db.Store.Documents.Read(db.Context, old, &original); err != nil {
			t.Fatal(err)
		}
		if err := vault.Read(target.Context, replacement, &returned); err != nil || !bytes.Equal(original.Bytes(), returned.Bytes()) {
			t.Fatal("destination key changed original bytes", err)
		}
		wrongKey := documents.Vault{Storage: vault.Storage, Keys: db.Store.Documents.Keys}
		if err := wrongKey.Read(db.Context, replacement, io.Discard); !errors.Is(err, documents.ErrIntegrity) {
			t.Fatal("old key decrypted the destination ciphertext", err)
		}
	}
	connections, err := target.Owner.Connections(target.Context)
	if err != nil || len(connections) != 0 {
		t.Fatal("restore revived grants", err)
	}
	connection, err := target.Owner.CreateConnection(target.Context, scope.BookID, "Example restored agent", "write")
	if err != nil {
		t.Fatal(err)
	}
	target.Store.Documents = vault
	restoredScope := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	if err = target.Store.Download(target.Context, restoredScope, a.ID, io.Discard); err != nil {
		t.Fatal("new connection could not read restored original", err)
	}
	// A new write must advance past document and relationship sequences too.
	readyOriginal(t, target, restoredScope, "new-original")
}

type heldArchiveStorage struct {
	documents.Storage
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldArchiveStorage) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.Storage.Open(ctx, id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestArchiveHoldsTheBookUntilOriginalBytesAreCopied(t *testing.T) {
	db, scope, directory := documentFixture(t)
	used := readyOriginal(t, db, scope, "used")
	postCitedJournal(t, db, scope, "opening", []ledger.SourceInput{{DocumentID: used.ID}})
	unused := readyOriginal(t, db, scope, "unused")
	ageDocumentFixture(t, db, unused.ID)
	storage := &heldArchiveStorage{Storage: db.Store.Documents.Storage, started: make(chan struct{}), release: make(chan struct{})}
	vault := &documents.Vault{Storage: storage, Keys: db.Store.Documents.Keys}
	var once sync.Once
	unblock := func() { once.Do(func() { close(storage.release) }) }
	done := make(chan struct{})
	var writeErr error
	go func() {
		defer close(done)
		writeErr = db.Owner.WriteArchive(db.Context, vault, io.Discard, scope.BookID)
	}()
	t.Cleanup(func() {
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("archive did not release its hold")
		}
	})
	select {
	case <-storage.started:
	case <-time.After(5 * time.Second):
		t.Fatal("archive did not start copying original")
	}
	bounded, cancel := context.WithTimeout(db.Context, 50*time.Millisecond)
	_, err := db.Owner.ExpireBookDocuments(bounded, scope.BookID, db.Store.Documents.Storage, 100)
	cancel()
	if err == nil {
		t.Fatal("cleanup bypassed active archive Book hold")
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 2 {
		t.Fatal("active archive lost an original", err)
	}
	unblock()
	select {
	case <-done:
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("archive did not finish")
	}
	if count, err := db.Owner.ExpireBookDocuments(db.Context, scope.BookID, db.Store.Documents.Storage, 100); err != nil || count != 1 {
		t.Fatal("archive kept hold after completion", count, err)
	}
}

func TestPortableBookExportContainsOnlySelectedBook(t *testing.T) {
	db, scope, _ := documentFixture(t)
	original := readyOriginal(t, db, scope, "first")
	_, other := db.Book(t, "Other Example Book")
	readyOriginal(t, db, other, "other")
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, scope.BookID); err != nil {
		t.Fatal(err)
	}
	manifest := archiveManifestOf(t, archive.Bytes())
	if len(manifest.Originals) != 1 || manifest.Originals[0].DocumentID != original.ID {
		t.Fatal("Book export included foreign original")
	}
	var books []ledger.Book
	if json.Unmarshal(manifest.Snapshot.Tables["books"], &books) != nil || len(books) != 1 || books[0].ID != scope.BookID {
		t.Fatal("Book export included foreign metadata")
	}
}

func alteredArchive(t *testing.T, data []byte, change func(*tar.Header, []byte) (*tar.Header, []byte)) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(data))
	var result bytes.Buffer
	writer := tar.NewWriter(&result)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		header, body = change(header, body)
		if header == nil {
			continue
		}
		header.Size = int64(len(body))
		if err = writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestPortableArchiveRejectsCorruptionAndCleansUncommittedObjects(t *testing.T) {
	db, scope, _ := documentFixture(t)
	readyOriginal(t, db, scope, "original")
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	corrupt := alteredArchive(t, archive.Bytes(), func(h *tar.Header, b []byte) (*tar.Header, []byte) {
		if h.Name != "manifest.json" && h.Name != "complete.json" {
			b[0] ^= 1
		}
		return h, b
	})
	missing := alteredArchive(t, archive.Bytes(), func(h *tar.Header, b []byte) (*tar.Header, []byte) {
		if h.Name != "manifest.json" && h.Name != "complete.json" {
			return nil, nil
		}
		return h, b
	})
	incomplete := alteredArchive(t, archive.Bytes(), func(h *tar.Header, b []byte) (*tar.Header, []byte) {
		if h.Name == "complete.json" {
			return nil, nil
		}
		return h, b
	})
	trailing := append(append([]byte{}, archive.Bytes()...), []byte("unexpected trailing bytes")...)
	target := testutil.New(t)
	vault, directory := restoreVault(t)
	for name, data := range map[string][]byte{"corrupt-original": corrupt, "missing-original": missing, "missing-completion": incomplete, "trailing-data": trailing, "truncated-original": archive.Bytes()[:len(archive.Bytes())/2]} {
		t.Run(name, func(t *testing.T) {
			if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(data), nil); err == nil {
				t.Fatal("invalid archive accepted")
			}
			var occupied bool
			if err := target.Admin.QueryRow(target.Context, "SELECT EXISTS(SELECT 1 FROM public.books)").Scan(&occupied); err != nil || occupied {
				t.Fatal("failed restore committed metadata", err)
			}
			if files, err := os.ReadDir(directory); err != nil || len(files) != 0 {
				t.Fatal("failed restore left encrypted objects", err)
			}
		})
	}
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("negative controls broke valid restore", err)
	}
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err == nil {
		t.Fatal("occupied target overwritten")
	}
}

type failedArchiveOutput struct{}

func (failedArchiveOutput) Write([]byte) (int, error) { return 0, errors.New("synthetic full output") }

func TestArchiveFailureLeavesJournalAndOriginalsUnchanged(t *testing.T) {
	db, scope, directory := documentFixture(t)
	original := readyOriginal(t, db, scope, "original")
	if err := db.Owner.WriteArchive(context.Background(), db.Store.Documents, failedArchiveOutput{}, ""); err == nil {
		t.Fatal("failed output reported success")
	}
	if err := db.Store.Download(db.Context, scope, original.ID, io.Discard); err != nil {
		t.Fatal("failed backup changed original", err)
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 1 {
		t.Fatal("failed backup touched storage", err)
	}
}

type archiveCreateControl struct {
	documents.Storage
	creates, removes int
	publishedError   bool
	exists           bool
	removeError      bool
}

func (s *archiveCreateControl) Create(ctx context.Context, id string, write func(io.Writer) error) error {
	s.creates++
	if s.exists {
		return documents.ErrExists
	}
	if err := s.Storage.Create(ctx, id, write); err != nil {
		return err
	}
	if s.publishedError {
		return documents.ErrUnavailable
	}
	return nil
}
func (s *archiveCreateControl) Remove(ctx context.Context, id string) error {
	s.removes++
	if s.removeError {
		return documents.ErrUnavailable
	}
	return s.Storage.Remove(ctx, id)
}

func TestArchiveCleanupIncludesPublishedFailuresButKeepsPreexistingObjects(t *testing.T) {
	db, scope, _ := documentFixture(t)
	readyOriginal(t, db, scope, "original")
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	vault, directory := restoreVault(t)
	control := &archiveCreateControl{Storage: vault.Storage, publishedError: true}
	vault.Storage = control
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err == nil {
		t.Fatal("ambiguous published write reported success")
	}
	if control.creates != 1 || control.removes != 1 {
		t.Fatal("possibly published object not reclaimed", control.creates, control.removes)
	}
	if files, err := os.ReadDir(directory); err != nil || len(files) != 0 {
		t.Fatal("failed restore retained an untracked object", err)
	}
	control.publishedError, control.exists = false, true
	control.creates, control.removes = 0, 0
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); err == nil {
		t.Fatal("preexisting object treated as a new write")
	}
	if control.creates != 1 || control.removes != 0 {
		t.Fatal("failure cleanup removed a preexisting identity", control.creates, control.removes)
	}
	control.exists, control.publishedError, control.removeError = false, true, true
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive.Bytes()), nil); !errors.Is(err, ledger.ErrArchiveCleanup) {
		t.Fatal("failed cleanup was hidden", err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatal("cleanup failure control did not retain an object", err)
	}
	if err = control.Storage.Remove(target.Context, files[0].Name()); err != nil {
		t.Fatal("synthetic retained object cleanup failed", err)
	}
}

func tarMember(t *testing.T, name string, kind byte, body []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := tar.NewWriter(&result)
	if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(body)), Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte{}, result.Bytes()[:result.Len()-1024]...)
	if kind != tar.TypeReg {
		data[156] = kind
		for i := 148; i < 156; i++ {
			data[i] = ' '
		}
		var sum int
		for _, b := range data[:512] {
			sum += int(b)
		}
		copy(data[148:156], fmt.Sprintf("%06o\x00 ", sum))
	}
	return data
}
func paxRecord(key, value string) string {
	line := key + "=" + value + "\n"
	size := len(line) + 2
	for {
		record := strconv.Itoa(size) + " " + line
		if len(record) == size {
			return record
		}
		size = len(record)
	}
}

func TestSparseArchiveAndOverBudgetManifestFailBeforeObjectWrites(t *testing.T) {
	db, scope, _ := documentFixture(t)
	data := make([]byte, 1<<20)
	data[0] = 'X'
	intake := prepareDocument(t, db, scope, "original", data)
	if _, err := db.Store.Upload(db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &original, ""); err != nil {
		t.Fatal(err)
	}
	var sparse bytes.Buffer
	reader := tar.NewReader(bytes.NewReader(original.Bytes()))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "originals/"+intake.Document.ID {
			pax := paxRecord("GNU.sparse.major", "0") + paxRecord("GNU.sparse.minor", "1") + paxRecord("GNU.sparse.numblocks", "1") + paxRecord("GNU.sparse.map", "0,1") + paxRecord("GNU.sparse.size", strconv.Itoa(len(data)))
			sparse.Write(tarMember(t, "sparse.pax", tar.TypeXHeader, []byte(pax)))
			sparse.Write(tarMember(t, header.Name, tar.TypeReg, []byte{'X'}))
		} else {
			sparse.Write(tarMember(t, header.Name, tar.TypeReg, body))
		}
	}
	sparse.Write(make([]byte, 1024))
	// Prove this is a valid small sparse encoding, not merely a broken header.
	probe := tar.NewReader(bytes.NewReader(sparse.Bytes()))
	if _, err := probe.Next(); err != nil {
		t.Fatal(err)
	}
	header, err := probe.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Format != tar.FormatPAX || header.Size != int64(len(data)) || sparse.Len() >= len(data) {
		t.Fatal("sparse expansion control failed", err)
	}
	var prefix [64]byte
	if _, err = io.ReadFull(probe, prefix[:]); err != nil || prefix[0] != 'X' || !bytes.Equal(prefix[1:], make([]byte, 63)) {
		t.Fatal("sparse logical byte control failed", err)
	}
	oversized := alteredArchive(t, original.Bytes(), func(h *tar.Header, b []byte) (*tar.Header, []byte) {
		if h.Name == "manifest.json" {
			var manifest map[string]json.RawMessage
			if json.Unmarshal(b, &manifest) != nil {
				t.Fatal("invalid source manifest")
			}
			originalObject := archiveManifestOf(t, original.Bytes()).Originals[0]
			objects := make([]documents.Object, 257)
			for i := range objects {
				objects[i] = originalObject
				objects[i].Size = documents.MaxSize
			}
			manifest["originals"], _ = json.Marshal(objects)
			b, _ = json.Marshal(manifest)
		}
		return h, b
	})
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	control := &archiveCreateControl{Storage: vault.Storage}
	vault.Storage = control
	provisioned := 0
	provision := func(context.Context, string) error { provisioned++; return nil }
	if err = target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(sparse.Bytes()), provision); err == nil || control.creates != 0 {
		t.Fatal("sparse member reached storage", err, control.creates)
	}
	provisioned = 0
	if err = target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(oversized), provision); !errors.Is(err, ledger.ErrLimit) || control.creates != 0 || provisioned != 0 {
		t.Fatal("logical budget was not rejected before effects", err, control.creates, provisioned)
	}
}

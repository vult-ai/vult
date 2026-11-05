package filebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

type exampleTransport struct {
	data          []byte
	document      ledger.Document
	uploadID      string
	uploaded      bool
	downloadError bool
	afterDownload func()
	calls         int
}

func (f *exampleTransport) Call(_ context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
	f.calls++
	if method == "add_document" {
		var q ledger.AddDocument
		if json.Unmarshal(input, &q) != nil {
			return nil, ledger.ErrInvalid
		}
		f.document = ledger.Document{ID: ledger.ID(), Name: q.Name, MediaType: q.MediaType, Size: q.Size, SHA256: q.SHA256, State: "pending"}
		f.uploadID = ledger.ID()
		return json.Marshal(ledger.DocumentIntake{Document: f.document, UploadID: f.uploadID})
	}
	if method == "get_document" {
		var q ledger.DocumentQuery
		if json.Unmarshal(input, &q) != nil || q.DocumentID != f.document.ID {
			return nil, ledger.ErrNotFound
		}
		inspection := ledger.DocumentInspection{Document: f.document}
		if q.UploadID != "" {
			inspection.Upload = &ledger.DocumentUploadState{ID: f.uploadID, Status: "pending"}
		}
		return json.Marshal(inspection)
	}
	return nil, ledger.ErrInvalid
}
func (f *exampleTransport) Upload(_ context.Context, id string, size int64, source io.Reader) (json.RawMessage, error) {
	data, err := io.ReadAll(source)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if id != f.uploadID || size != f.document.Size || int64(len(data)) != size || hex.EncodeToString(hash[:]) != f.document.SHA256 {
		return nil, documents.ErrIntegrity
	}
	f.data = data
	f.uploaded = true
	f.document.State = "ready"
	return json.Marshal(f.document)
}
func (f *exampleTransport) Download(_ context.Context, id string, dst io.Writer) error {
	if id != f.document.ID {
		return ledger.ErrNotFound
	}
	_, err := dst.Write(f.data)
	if err != nil {
		return err
	}
	if f.afterDownload != nil {
		f.afterDownload()
	}
	if f.downloadError {
		return documents.ErrIntegrity
	}
	return nil
}
func body(v any) json.RawMessage { data, _ := json.Marshal(v); return data }

func TestFilesTransferOriginalWithoutJSONBytesOrOverwrite(t *testing.T) {
	ctx := context.Background()
	imports, exports := t.TempDir(), t.TempDir()
	if err := os.Chmod(exports, 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("synthetic original\n"), 300000)
	if err := os.WriteFile(filepath.Join(imports, "example.pdf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	transport := &exampleTransport{}
	files, err := Open(transport, imports, exports)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	result, err := files.Call(ctx, "add_document", body(map[string]any{"request_key": "original", "file": "example.pdf"}))
	if err != nil || !transport.uploaded || !bytes.Equal(transport.data, data) || bytes.Contains(result, data[:100]) {
		t.Fatal("file was not streamed correctly", err)
	}
	if _, err = files.Call(ctx, "get_document", body(map[string]any{"document_id": transport.document.ID, "output": "returned.pdf"})); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(exports, "returned.pdf"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("export changed bytes", err)
	}
	info, err := os.Stat(filepath.Join(exports, "returned.pdf"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("export not private", err)
	}
	if _, err = files.Call(ctx, "get_document", body(map[string]any{"document_id": transport.document.ID, "output": "returned.pdf"})); err == nil {
		t.Fatal("existing export overwritten")
	}
	transport.downloadError = true
	if _, err = files.Call(ctx, "get_document", body(map[string]any{"document_id": transport.document.ID, "output": "failed.pdf"})); err != documents.ErrIntegrity {
		t.Fatal("failed stream presented as complete", err)
	}
	entries, err := os.ReadDir(exports)
	if err != nil || len(entries) != 1 || entries[0].Name() != "returned.pdf" {
		t.Fatal("failed export left files", err)
	}
}

func TestFilesRejectTraversalLinksSpecialFilesAndUnapprovedFolders(t *testing.T) {
	ctx := context.Background()
	imports, exports, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Chmod(exports, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(outside, "outside.pdf")
	if err := os.WriteFile(original, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, filepath.Join(imports, "symlink.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(imports, "hardlink.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(imports, "pipe.pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(imports, "directory.pdf"), 0700); err != nil {
		t.Fatal(err)
	}
	transport := &exampleTransport{}
	files, err := Open(transport, imports, exports)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	for _, name := range []string{"../outside.pdf", original, "symlink.pdf", "hardlink.pdf", "pipe.pdf", "directory.pdf", ".", "example\\file.pdf"} {
		if _, err := files.Call(ctx, "add_document", body(map[string]any{"request_key": "original", "file": name})); err == nil {
			t.Fatal("unapproved input accepted", name)
		}
	}
	if transport.calls != 0 {
		t.Fatal("invalid files reached ledger")
	}
	denied, err := Open(transport, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Close()
	if _, err = denied.Call(ctx, "add_document", body(map[string]any{"request_key": "original", "file": "example.pdf"})); err != ledger.ErrDenied {
		t.Fatal("unapproved folder read", err)
	}
	for _, name := range []string{"../export.pdf", "/export.pdf", "a/b.pdf", "a\\b.pdf", "."} {
		if _, err := files.Call(ctx, "get_document", body(map[string]any{"document_id": ledger.ID(), "output": name})); err != ledger.ErrInvalid {
			t.Fatal("unsafe export filename accepted", name, err)
		}
	}
}

func TestSharedExportFolderAndReplacedStagingCannotPublish(t *testing.T) {
	ctx := context.Background()
	exports := t.TempDir()
	transport := &exampleTransport{}
	if err := os.Chmod(exports, 0777); err != nil {
		t.Fatal(err)
	}
	if files, err := Open(transport, "", exports); err == nil {
		files.Close()
		t.Fatal("shared writable export root accepted")
	}
	if err := os.Chmod(exports, 0700); err != nil {
		t.Fatal(err)
	}
	files, err := Open(transport, "", exports)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	data := []byte("synthetic original")
	hash := sha256.Sum256(data)
	transport.data = data
	transport.document = ledger.Document{ID: ledger.ID(), State: "ready", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	transport.afterDownload = func() {
		entries, err := os.ReadDir(exports)
		if err != nil || len(entries) != 1 {
			t.Fatal("missing staging file", err)
		}
		path := filepath.Join(exports, entries[0].Name())
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("replacement bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(exports, 0777); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = files.Call(ctx, "get_document", body(map[string]any{"document_id": transport.document.ID, "output": "returned.pdf"})); err != ledger.ErrDenied {
		t.Fatal("changed export authority published replacement", err)
	}
	entries, err := os.ReadDir(exports)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed export published a file", err)
	}
}

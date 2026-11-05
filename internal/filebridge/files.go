// Package filebridge moves originals between approved client folders and a
// ledger transport. The service never receives a client filesystem path.
package filebridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

type Transport interface {
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Upload(context.Context, string, int64, io.Reader) (json.RawMessage, error)
	Download(context.Context, string, io.Writer) error
}

type Files struct {
	transport        Transport
	imports, exports *os.Root
}

func Open(transport Transport, imports, exports string) (*Files, error) {
	if transport == nil {
		return nil, ledger.ErrInvalid
	}
	f := &Files{transport: transport}
	for _, folder := range []struct {
		path    string
		root    **os.Root
		private bool
	}{{imports, &f.imports, false}, {exports, &f.exports, true}} {
		if folder.path == "" {
			continue
		}
		if !filepath.IsAbs(folder.path) || filepath.Clean(folder.path) != folder.path || folder.path == string(filepath.Separator) {
			f.Close()
			return nil, ledger.ErrInvalid
		}
		info, err := os.Lstat(folder.path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			f.Close()
			return nil, documents.ErrUnavailable
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			f.Close()
			return nil, ledger.ErrDenied
		}
		root, err := os.OpenRoot(folder.path)
		if err != nil {
			f.Close()
			return nil, documents.ErrUnavailable
		}
		handle, err := root.Open(".")
		if err != nil {
			root.Close()
			f.Close()
			return nil, documents.ErrUnavailable
		}
		opened, err := handle.Stat()
		if err == nil && folder.private {
			err = privateExportFolder(handle)
		}
		handle.Close()
		if err != nil || !os.SameFile(info, opened) {
			root.Close()
			f.Close()
			return nil, documents.ErrUnavailable
		}
		*folder.root = root
	}
	return f, nil
}

func (f *Files) Close() {
	if f.imports != nil {
		f.imports.Close()
		f.imports = nil
	}
	if f.exports != nil {
		f.exports.Close()
		f.exports = nil
	}
}

func (f *Files) Call(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "add_document":
		return f.add(ctx, input)
	case "get_document":
		return f.get(ctx, input)
	default:
		return f.transport.Call(ctx, method, input)
	}
}

func validName(name string) bool {
	if len(name) < 1 || len(name) > 255 || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func mediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".csv":
		return "text/csv"
	}
	return ""
}

func (f *Files) add(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var q struct {
		RequestKey string `json:"request_key"`
		File       string `json:"file"`
		MediaType  string `json:"media_type,omitempty"`
	}
	if strictjson.Decode(input, &q) != nil || !filepath.IsLocal(q.File) || strings.Contains(q.File, "\\") || !validName(filepath.Base(q.File)) {
		return nil, ledger.ErrInvalid
	}
	if f.imports == nil {
		return nil, ledger.ErrDenied
	}
	file, err := f.imports.OpenFile(q.File, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > documents.MaxSize {
		return nil, ledger.ErrInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, ledger.ErrDenied
	}
	if q.MediaType == "" {
		q.MediaType = mediaType(q.File)
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, documents.MaxSize+1)); err != nil || n != info.Size() {
		return nil, documents.ErrIntegrity
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, documents.ErrUnavailable
	}
	request := ledger.AddDocument{RequestKey: q.RequestKey, Name: filepath.Base(q.File), MediaType: q.MediaType, Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))}
	body, _ := json.Marshal(request)
	prepared, err := f.transport.Call(ctx, "add_document", body)
	if err != nil {
		return nil, err
	}
	var intake ledger.DocumentIntake
	if strictjson.Decode(prepared, &intake) != nil || !ledger.ValidID(intake.Document.ID) || intake.Document.Size != request.Size || intake.Document.SHA256 != request.SHA256 {
		return nil, documents.ErrUnavailable
	}
	// A retained preparation never implies current availability. Inspect its
	// exact intake before resuming; do not invent a new request key on failure.
	query, _ := json.Marshal(ledger.DocumentQuery{DocumentID: intake.Document.ID, UploadID: intake.UploadID})
	current, err := f.transport.Call(ctx, "get_document", query)
	if err != nil {
		return nil, err
	}
	var inspection ledger.DocumentInspection
	if strictjson.Decode(current, &inspection) != nil || inspection.Document.ID != intake.Document.ID || inspection.Document.Size != request.Size || inspection.Document.SHA256 != request.SHA256 {
		return nil, documents.ErrUnavailable
	}
	if inspection.Upload != nil && inspection.Upload.Status == "recorded" {
		return inspection.Upload.Result, nil
	}
	if intake.UploadID == "" && inspection.Document.State == "ready" {
		return json.Marshal(inspection.Document)
	}
	if inspection.Upload == nil || inspection.Upload.Status != "pending" {
		return nil, errors.New("upload attempt expired; inspect the original request outcome before preparing a new intake")
	}
	result, err := f.transport.Upload(ctx, intake.UploadID, info.Size(), file)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (f *Files) get(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var q struct {
		DocumentID string `json:"document_id"`
		UploadID   string `json:"upload_id,omitempty"`
		Output     string `json:"output,omitempty"`
	}
	if strictjson.Decode(input, &q) != nil || q.Output != "" && !validName(q.Output) {
		return nil, ledger.ErrInvalid
	}
	query, _ := json.Marshal(ledger.DocumentQuery{DocumentID: q.DocumentID, UploadID: q.UploadID})
	metadata, err := f.transport.Call(ctx, "get_document", query)
	if err != nil || q.Output == "" {
		return metadata, err
	}
	if f.exports == nil {
		return nil, ledger.ErrDenied
	}
	if err = f.checkExportFolder(); err != nil {
		return nil, err
	}
	var inspection ledger.DocumentInspection
	if strictjson.Decode(metadata, &inspection) != nil || inspection.Document.State != "ready" || inspection.Document.ID != q.DocumentID || inspection.Document.Size < 1 || inspection.Document.Size > documents.MaxSize {
		return nil, ledger.ErrNotFound
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, documents.ErrUnavailable
	}
	temporary := ".vult-" + hex.EncodeToString(random[:]) + ".part"
	file, err := f.exports.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	defer func() { file.Close(); f.exports.Remove(temporary) }()
	if !fssecurity.NoExtendedACL(file) {
		return nil, ledger.ErrDenied
	}
	hash := sha256.New()
	count := &countWriter{dst: io.MultiWriter(file, hash), remaining: inspection.Document.Size}
	if err = f.transport.Download(ctx, q.DocumentID, count); err != nil {
		return nil, err
	}
	if count.remaining != 0 || hex.EncodeToString(hash.Sum(nil)) != inspection.Document.SHA256 {
		return nil, documents.ErrIntegrity
	}
	if err = file.Sync(); err != nil {
		return nil, documents.ErrUnavailable
	}
	if err = file.Close(); err != nil {
		return nil, documents.ErrUnavailable
	}
	if err = f.checkExportFolder(); err != nil {
		return nil, err
	}
	if err = f.exports.Link(temporary, q.Output); err != nil {
		return nil, documents.ErrUnavailable
	}
	if err = f.exports.Remove(temporary); err != nil {
		return nil, documents.ErrUnavailable
	}
	directory, err := f.exports.Open(".")
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	err = directory.Sync()
	directory.Close()
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	return json.Marshal(struct {
		Document ledger.Document `json:"document"`
		Output   string          `json:"output"`
	}{inspection.Document, q.Output})
}

func (f *Files) checkExportFolder() error {
	handle, err := f.exports.Open(".")
	if err != nil {
		return ledger.ErrDenied
	}
	defer handle.Close()
	return privateExportFolder(handle)
}

func privateExportFolder(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ledger.ErrDenied
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || !fssecurity.NoExtendedACL(file) {
		return ledger.ErrDenied
	}
	return nil
}

type countWriter struct {
	dst       io.Writer
	remaining int64
}

func (w *countWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, documents.ErrIntegrity
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
}

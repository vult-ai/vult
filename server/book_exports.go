package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

const bookExportPrefix = "/api/v1/exports/"
const bookExportTimeout = 10 * time.Minute
const bookExportWriteIdle = 15 * time.Second

type exportByteCount struct {
	io.Writer
	size int64
}

func (c *exportByteCount) Write(data []byte) (int, error) {
	n, err := c.Writer.Write(data)
	c.size += int64(n)
	return n, err
}

func bookExportType(path string) string {
	switch path {
	case bookExportPrefix + "book.tar":
		return "application/x-tar"
	case bookExportPrefix + "journal.json":
		return "application/json"
	case bookExportPrefix + "transactions.csv", bookExportPrefix + "journal-lines.csv", bookExportPrefix + "accounts.csv":
		return "text/csv; charset=utf-8"
	default:
		return ""
	}
}

func (a *webAPI) requireExportOwner(ctx context.Context, session, tenant, book string) error {
	current, err := a.sessions.BookTenant(ctx, session, book)
	if errors.Is(err, access.ErrDenied) {
		return ledger.ErrDenied
	}
	if err != nil {
		return err
	}
	if current != tenant {
		return ledger.ErrDenied
	}
	return nil
}

func (a *webAPI) archiveOriginal(session, tenant, book string) func(context.Context, documents.Object, io.Writer) error {
	return func(ctx context.Context, original documents.Object, dst io.Writer) error {
		if a.readDocument != nil {
			hash := sha256.New()
			if err := a.readDocument(ctx, session, book, original.DocumentID, io.MultiWriter(dst, hash)); err != nil {
				return err
			}
			if hex.EncodeToString(hash.Sum(nil)) != original.SHA256 {
				return documents.ErrIntegrity
			}
			return nil
		}
		if a.documents == nil {
			return documents.ErrUnavailable
		}
		if err := a.requireExportOwner(ctx, session, tenant, book); err != nil {
			return err
		}
		return a.documents.Read(ctx, original, dst)
	}
}

type stagedBookExport struct {
	spool *exportSpool
	body  io.Reader
	size  int64
	state ledger.RecordedState
}

func (a *webAPI) stageBookExport(ctx context.Context, session, tenant, book, name string) (out stagedBookExport, err error) {
	if !a.exports.staging.TryLock() {
		return out, errExportBusy
	}
	defer a.exports.staging.Unlock()
	spool, err := a.exports.openSpool()
	if err != nil {
		return out, err
	}
	defer func() {
		if out.spool == nil {
			spool.close()
			if err != nil && spool.full {
				err = errExportBusy
			}
		}
	}()

	// Keep the one-use decryption identity in memory and the spool unlinked.
	key, err := age.GenerateX25519Identity()
	if err != nil {
		return out, err
	}
	cipher, err := age.Encrypt(spool, key.Recipient())
	if err != nil {
		return out, err
	}
	output := &exportByteCount{Writer: cipher}
	var archive ledger.BookArchive
	err = a.sessions.WithBookExportSnapshot(ctx, session, tenant, func(owner ledger.TenantAdmin) error {
		var err error
		if name == "book.tar" {
			archive, out.state, err = owner.PrepareBookArchive(ctx, book)
		} else {
			out.state, err = owner.WriteBookExport(ctx, book, name, output)
		}
		return err
	})
	if err != nil {
		return out, err
	}
	if name == "book.tar" {
		if err = archive.Write(ctx, output, a.archiveOriginal(session, tenant, book)); err != nil {
			if authErr := a.requireExportOwner(ctx, session, tenant, book); authErr != nil {
				return out, authErr
			}
			return out, err
		}
	}
	if err = cipher.Close(); err != nil {
		return out, err
	}
	if _, err = spool.file.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	out.body, err = age.Decrypt(spool.file, key)
	if err != nil {
		return out, err
	}
	out.spool, out.size = spool, output.size
	return out, nil
}

func exportDeadline(w http.ResponseWriter, deadline time.Time) error {
	err := http.NewResponseController(w).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

type exportTransfer struct {
	ctx context.Context
	w   http.ResponseWriter
}

func (t exportTransfer) Write(data []byte) (int, error) {
	if err := t.ctx.Err(); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(bookExportWriteIdle)
	if end, ok := t.ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := exportDeadline(t.w, deadline); err != nil {
		return 0, err
	}
	return t.w.Write(data)
}

func (a *webAPI) bookExport(w http.ResponseWriter, r *http.Request, session string) {
	book, err := ownerBookID(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ctx := r.Context()
	tenant, err := a.sessions.BookTenant(ctx, session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(bookExportTimeout)
	}
	if err := exportDeadline(w, deadline); err != nil {
		apiFailure(w, err)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, bookExportPrefix)
	out, err := a.stageBookExport(ctx, session, tenant, book, name)
	if errors.Is(err, errExportBusy) {
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusTooManyRequests)
		return
	}
	if err != nil {
		apiFailure(w, err)
		return
	}
	defer out.spool.close()
	if err := a.requireExportOwner(ctx, session, tenant, book); err != nil {
		apiFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", bookExportType(r.URL.Path))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(out.size, 10))
	w.Header().Set("X-Vult-State-Revision", out.state.StateRevision)
	w.Header().Set("X-Vult-State-Captured-At", out.state.StateCapturedAt)
	_, _ = io.Copy(exportTransfer{ctx, w}, out.body)
}

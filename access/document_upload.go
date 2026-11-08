package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

// UploadOwnerDocument holds two short owner transactions. The first reserves
// an original for retry or expiry. The second commits its verified completion.
func (s *Service) UploadOwnerDocument(ctx context.Context, session, book string, q ledger.AddDocument, vault *documents.Vault, body io.Reader, admit OwnerAdmission) (ledger.OwnerDocumentUpload, error) {
	tenant, err := s.BookTenant(ctx, session, book)
	if err != nil {
		return ledger.OwnerDocumentUpload{}, err
	}
	return uploadOwnerDocument(ctx, s.owners, s.origin, session, tenant, book, q, vault, body, admit)
}

// Upload verifies the plaintext and encrypts it in the protected service.
// The browser peer and its live session must select the same owned Book.
func (o *OwnerDocumentReader) Upload(ctx context.Context, session, book string, q ledger.AddDocument, vault *documents.Vault, body io.Reader) (ledger.OwnerDocumentUpload, error) {
	if o == nil || !ledger.ValidID(book) {
		return ledger.OwnerDocumentUpload{}, ErrDenied
	}
	select {
	case o.uploadSlots <- struct{}{}:
		defer func() { <-o.uploadSlots }()
	default:
		return ledger.OwnerDocumentUpload{}, documents.ErrUnavailable
	}
	tenant, err := ownerResourceTenant(ctx, o.db, session, o.origin, "book", book)
	if err != nil {
		return ledger.OwnerDocumentUpload{}, err
	}
	return uploadOwnerDocument(ctx, o.db, o.origin, session, tenant, book, q, vault, body, nil)
}

func uploadOwnerDocument(ctx context.Context, db *pgxpool.Pool, origin, session, tenant, book string, q ledger.AddDocument, vault *documents.Vault, body io.Reader, admit OwnerAdmission) (ledger.OwnerDocumentUpload, error) {
	if vault == nil || vault.Storage == nil || vault.Keys == nil {
		return ledger.OwnerDocumentUpload{}, documents.ErrUnavailable
	}
	if !q.ValidOwnerUpload() || !ownerKeyPattern.MatchString(q.RequestKey) || body == nil {
		return ledger.OwnerDocumentUpload{}, ledger.ErrInvalid
	}
	// Verify before making a reservation, including for a retry or duplicate.
	// The bounded buffer keeps plaintext off disk and socket reads off the lock.
	data := make([]byte, q.Size+1)
	n, err := io.ReadFull(body, data)
	if int64(n) != q.Size || err != io.ErrUnexpectedEOF {
		return ledger.OwnerDocumentUpload{}, documents.ErrIntegrity
	}
	data = data[:n]
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != q.SHA256 {
		return ledger.OwnerDocumentUpload{}, documents.ErrIntegrity
	}
	if !ownerDocumentMedia(data, q.MediaType) {
		return ledger.OwnerDocumentUpload{}, documents.ErrMediaType
	}
	store := ledger.Store{Documents: vault}
	if admit != nil {
		store.AdmitWrite = func(ctx context.Context, tx pgx.Tx, scope ledger.Scope, operation string, input json.RawMessage) error {
			return admit(ctx, tx, scope.TenantID, operation, input)
		}
	}
	var upload string
	err = withOwnerPool(ctx, db, origin, session, tenant, true, false, "", func(_ pgx.Tx, owner ledger.TenantAdmin, who string) error {
		var err error
		upload, err = owner.PrepareOwnerDocument(ctx, book, who, q, store)
		return err
	})
	if err != nil {
		return ledger.OwnerDocumentUpload{}, err
	}
	var out ledger.OwnerDocumentUpload
	err = withOwnerPool(ctx, db, origin, session, tenant, true, false, "", func(_ pgx.Tx, owner ledger.TenantAdmin, who string) error {
		var err error
		out, err = owner.CompleteOwnerDocument(ctx, book, who, upload, vault, bytes.NewReader(data))
		return err
	})
	if err != nil {
		return ledger.OwnerDocumentUpload{}, err
	}
	return out, nil
}

func ownerDocumentMedia(data []byte, media string) bool {
	switch media {
	case "application/pdf":
		return bytes.HasPrefix(data, []byte("%PDF-"))
	case "image/png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	case "text/csv":
		if !utf8.Valid(data) {
			return false
		}
		for _, r := range string(data) {
			if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
				return false
			}
		}
		return true
	}
	return false
}

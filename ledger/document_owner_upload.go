package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/documents"
)

var ErrUploadExpired = errors.New("document upload reservation expired")

type OwnerDocumentUpload struct {
	Document Document `json:"document"`
	Reused   bool     `json:"reused"`
}

func (q AddDocument) ValidOwnerUpload() bool {
	return validOriginal(q) && strings.TrimSpace(q.Name) == q.Name && utf8.RuneCountInString(q.Name) <= 180
}

// Reserve the storage identity before publishing ciphertext. A failed or
// uncertain completion keeps that same object eligible for retry and expiry.
func (a TenantAdmin) PrepareOwnerDocument(ctx context.Context, book, actor string, q AddDocument, store Store) (string, error) {
	if !q.ValidOwnerUpload() || store.Documents == nil || store.Documents.Storage == nil || store.Documents.Keys == nil {
		return "", ErrInvalid
	}
	var intake DocumentIntake
	err := a.ownerDocumentWrite(ctx, book, actor, func(tx pgx.Tx, scope Scope) error {
		data, err := store.prepareDocument(ctx, tx, scope, q)
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &intake) != nil || !ValidID(intake.UploadID) {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return intake.UploadID, nil
}

func (a TenantAdmin) CompleteOwnerDocument(ctx context.Context, book, actor, upload string, vault *documents.Vault, body io.Reader) (OwnerDocumentUpload, error) {
	if !ValidID(upload) || vault == nil || body == nil {
		return OwnerDocumentUpload{}, ErrInvalid
	}
	var result ownerDocumentReceipt
	err := a.ownerDocumentWrite(ctx, book, actor, func(tx pgx.Tx, scope Scope) error {
		data, err := (Store{Documents: vault}).completeDocument(ctx, tx, scope, upload, body)
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &result) != nil || result.State != "ready" || result.ExpiresAt != nil {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return OwnerDocumentUpload{}, err
	}
	return OwnerDocumentUpload{Document: result.Document, Reused: result.Reused}, nil
}

func (a TenantAdmin) ownerDocumentWrite(ctx context.Context, book, actor string, apply func(pgx.Tx, Scope) error) error {
	if !ValidID(actor) {
		return ErrDenied
	}
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT access.owner_document_allowed($1,$2)", scope.TenantID, scope.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrDenied
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		return apply(tx, scope)
	})
}

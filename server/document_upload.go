package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

const (
	ownerDocumentUploadPath    = "/api/v1/documents"
	ownerDocumentUploadTimeout = 90 * time.Second
	documentGroupPath          = "/api/v1/documents/group"
)

func (a *webAPI) documentUpload(w http.ResponseWriter, r *http.Request, session string) {
	query, err := documentReadQuery(r, false, false)
	if err != nil {
		apiFailure(w, err)
		return
	}
	book := query.book
	if r.ContentLength > documents.MaxSize {
		apiFailure(w, ledger.ErrLimit)
		return
	}
	if r.ContentLength < 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	for _, key := range []string{"Content-Type", "Idempotency-Key", "X-Vult-Document-Name", "X-Vult-Document-SHA256"} {
		if len(r.Header.Values(key)) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	// The group to file the document in, optional.
	group := r.Header.Get("X-Vult-Document-Group")
	if len(r.Header.Values("X-Vult-Document-Group")) > 1 || group != "" && group != "statement" && group != "receipt" && group != "company" && group != "tax" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	media := r.Header.Get("Content-Type")
	switch media {
	case "application/pdf", "text/csv", "image/png", "image/jpeg", "image/webp":
	default:
		failure(w, http.StatusUnsupportedMediaType)
		return
	}
	encoded := r.Header.Get("X-Vult-Document-Name")
	if len(encoded) > 2160 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	name, err := url.PathUnescape(encoded)
	q := ledger.AddDocument{
		RequestKey: r.Header.Get("Idempotency-Key"), Name: name, MediaType: media,
		Size: r.ContentLength, SHA256: r.Header.Get("X-Vult-Document-SHA256"),
	}
	if err != nil || !q.ValidOwnerUpload() {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	checkReserved, checkQueued := false, false
	if a.added != nil && q.Size <= maxCheckedDocument && group != "company" && group != "tax" {
		// The file is always added; its check waits its turn.
		checkReserved = a.checkQueue != nil && a.checkQueue.admit()
		if checkReserved {
			defer func() {
				if !checkQueued {
					a.checkQueue.release()
				}
			}()
		}
	}
	select {
	case a.documentUploads <- struct{}{}:
		defer func() { <-a.documentUploads }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusTooManyRequests)
		return
	}
	deadline := time.Now().Add(ownerDocumentUploadTimeout)
	controller := http.NewResponseController(w)
	for _, setDeadline := range []func(time.Time) error{controller.SetReadDeadline, controller.SetWriteDeadline} {
		if err := setDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			failure(w, http.StatusServiceUnavailable)
			return
		}
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	var result ledger.OwnerDocumentUpload
	if a.uploadDocument != nil {
		// Deny foreign Books before sending the bytes to the local service.
		if _, err = a.sessions.BookTenant(ctx, session, book); err == nil {
			result, err = a.uploadDocument(ctx, session, book, q, r.Body)
		}
	} else {
		result, err = a.sessions.UploadOwnerDocument(ctx, session, book, q, a.documents, r.Body, a.admit)
	}
	if err != nil {
		switch {
		case errors.Is(err, documents.ErrMediaType):
			failure(w, http.StatusUnsupportedMediaType)
		case errors.Is(err, documents.ErrIntegrity):
			apiFailure(w, ledger.ErrInvalid)
		case errors.Is(err, ledger.ErrUploadExpired):
			failure(w, http.StatusGone)
		default:
			apiFailure(w, err)
		}
		return
	}
	if group != "" {
		// Filed after it is stored; a retried upload files it again.
		tenant, err := a.sessions.BookTenant(ctx, session, book)
		if err == nil {
			err = a.sessions.OwnerAct(ctx, session, tenant, func(o ledger.TenantAdmin, who string) error {
				filed, err := o.SetDocumentGroup(ctx, book, who, ledger.SetDocumentGroup{DocumentID: result.Document.ID, Group: group})
				result.Document.Group, result.Document.GroupSet = filed.Group, filed.GroupSet
				return err
			})
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
	}
	if !result.Reused {
		w.WriteHeader(http.StatusCreated)
		if checkReserved {
			checkQueued = a.documentAdded(r, session, book, result.Document)
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		SchemaVersion int `json:"schema_version"`
		ledger.OwnerDocumentUpload
	}{1, result})
}

// documentGroup files one of the Book's documents in a group: the owner's
// change, which needs a sign-in but not a fresh one, since it changes no
// records.
func (a *webAPI) documentGroup(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID     string `json:"book_id"`
		DocumentID string `json:"document_id"`
		Group      string `json:"group"`
	}
	if !readRuleBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out ledger.Document
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.SetDocumentGroup(r.Context(), q.BookID, who, ledger.SetDocumentGroup{DocumentID: q.DocumentID, Group: q.Group})
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "document": out})
}

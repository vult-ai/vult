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
	if !result.Reused {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(struct {
		SchemaVersion int `json:"schema_version"`
		ledger.OwnerDocumentUpload
	}{1, result})
}

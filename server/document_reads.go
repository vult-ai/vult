package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

type documentReadRequest struct {
	book, revision, search string
	page                   ledger.ListQuery
}

func documentReadQuery(r *http.Request, paged, entries bool) (documentReadRequest, error) {
	request := documentReadRequest{page: ledger.ListQuery{Limit: 50}}
	bound := 1024
	if paged && !entries {
		bound = 8192
	}
	if len(r.URL.RawQuery) > bound {
		return request, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || !ledger.ValidID(q.Get("book_id")) {
		return request, ledger.ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 {
			return request, ledger.ErrInvalid
		}
		if key == "book_id" {
			continue
		}
		if paged && key == "revision" && values[0] != "" &&
			(entries && len(values[0]) <= 20 || !entries && len(values[0]) == 64) {
			request.revision = values[0]
			continue
		}
		if paged && !entries && key == "q" {
			request.search = strings.TrimSpace(values[0])
			continue
		}
		if !paged || key != "after" && key != "limit" {
			return request, ledger.ErrInvalid
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 0 {
			return request, ledger.ErrInvalid
		}
		if key == "limit" {
			if n < 1 || n > 100 {
				return request, ledger.ErrInvalid
			}
			request.page.Limit = int(n)
		} else {
			request.page.After = n
		}
	}
	if paged && !entries && request.search == "" && request.revision != "" {
		return request, ledger.ErrInvalid
	}
	request.book = q.Get("book_id")
	return request, nil
}

func (a *webAPI) documentRead(w http.ResponseWriter, r *http.Request, session string) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/documents")
	id, operation := "", "list"
	if path != "" {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) > 2 || !ledger.ValidID(parts[0]) {
			apiFailure(w, ledger.ErrNotFound)
			return
		}
		id, operation = parts[0], "show"
		if len(parts) == 2 {
			operation = parts[1]
			if operation != "content" && operation != "entries" {
				apiFailure(w, ledger.ErrNotFound)
				return
			}
		}
	}
	query, err := documentReadQuery(r, operation == "list" || operation == "entries", operation == "entries")
	if err != nil {
		apiFailure(w, err)
		return
	}
	book := query.book
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	if operation == "content" {
		a.documentContent(w, r, session, tenant, book, id)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		switch operation {
		case "list":
			if query.search != "" {
				var page ledger.DocumentSearchPage
				page, err = owner.SearchDocuments(r.Context(), book, query.search, query.page, query.revision)
				out = struct {
					SchemaVersion int `json:"schema_version"`
					ledger.DocumentSearchPage
				}{1, page}
				break
			}
			var page ledger.DocumentPage
			page, err = owner.Documents(r.Context(), book, query.page)
			out = struct {
				SchemaVersion int `json:"schema_version"`
				ledger.DocumentPage
			}{1, page}
		case "show":
			var doc ledger.DocumentInspection
			doc, err = owner.Document(r.Context(), book, id)
			out = struct {
				SchemaVersion int `json:"schema_version"`
				ledger.DocumentInspection
			}{1, doc}
		case "entries":
			out, err = owner.DocumentEntries(r.Context(), book, id, query.page, query.revision)
		}
		return err
	})
	if err != nil {
		searchFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) documentContent(w http.ResponseWriter, r *http.Request, session, tenant, book, id string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Header.Get("Range") != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var doc ledger.DocumentInspection
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		doc, err = owner.Document(r.Context(), book, id)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	if doc.Document.State != "ready" {
		apiFailure(w, ledger.ErrNotFound)
		return
	}
	if err = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(90 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		failure(w, http.StatusServiceUnavailable)
		return
	}
	dst := &documentDownload{response: w, size: doc.Document.Size, sha: doc.Document.SHA256}
	if a.readDocument != nil {
		err = a.readDocument(r.Context(), session, book, id, dst)
	} else {
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
			return owner.DownloadDocument(r.Context(), book, id, a.documents, dst)
		})
	}
	if err != nil {
		if dst.started {
			return
		}
		if errors.Is(err, documents.ErrUnavailable) || errors.Is(err, documents.ErrIntegrity) {
			failure(w, http.StatusServiceUnavailable)
		} else {
			apiFailure(w, err)
		}
		return
	}
	_ = dst.finish()
}

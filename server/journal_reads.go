package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

func (a *webAPI) journalRead(w http.ResponseWriter, r *http.Request, session string) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/transactions/"), "/")
	documents := len(parts) == 2 && parts[1] == "documents"
	if (len(parts) != 1 && !documents) || !ledger.ValidID(parts[0]) {
		failure(w, http.StatusNotFound)
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	params, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(params["book_id"]) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	q := ledger.EntryDocumentsQuery{EntryID: parts[0]}
	for key, values := range params {
		if len(values) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		if key == "book_id" {
			continue
		}
		if !documents {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		if key == "history" {
			if values[0] != "true" && values[0] != "false" {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
			q.History = values[0] == "true"
			continue
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 0 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		switch key {
		case "source_after":
			q.SourceAfter = n
		case "evidence_after":
			q.EvidenceAfter = n
		case "limit":
			if n < 1 || n > 100 {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
			q.Limit = int(n)
		default:
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	book := params.Get("book_id")
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		if documents {
			out, err = owner.JournalDocuments(r.Context(), book, q)
		} else {
			out, err = owner.Journal(r.Context(), book, parts[0])
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

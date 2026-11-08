package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

func (a *webAPI) journalPostings(w http.ResponseWriter, r *http.Request, session string) {
	if len(r.URL.RawQuery) > 8192 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	query := r.URL.Query()
	if len(query) != 2 || len(query["book_id"]) != 1 || len(query["ids"]) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	book := query.Get("book_id")
	ids := strings.Split(query.Get("ids"), ",")
	if len(ids) < 1 || len(ids) > 100 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !ledger.ValidID(id) || seen[id] {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		seen[id] = true
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var result ledger.ScopedPostings
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		result, err = owner.JournalPostings(r.Context(), book, ids)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

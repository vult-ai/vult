package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

// searchQuery reads GET /api/v1/search?book_id=…&q=…[&limit=1–20][&types=a,b].
// Every parameter appears at most once; book_id and q are required.
func searchQuery(r *http.Request) (string, ledger.SearchQuery, error) {
	out := ledger.SearchQuery{}
	if len(r.URL.RawQuery) > 8192 {
		return "", out, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || !ledger.ValidID(q.Get("book_id")) || len(q["q"]) != 1 {
		return "", out, ledger.ErrInvalid
	}
	allowed := map[string]bool{"book_id": true, "q": true, "limit": true, "types": true, "account_ids": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return "", out, ledger.ErrInvalid
		}
	}
	out.Query = q.Get("q")
	if value, exists := q["limit"]; exists {
		n, err := strconv.Atoi(value[0])
		if err != nil || n < 1 {
			return "", out, ledger.ErrInvalid
		}
		out.Limit = n
	}
	if value := q.Get("types"); value != "" {
		out.Types = strings.Split(value, ",")
	}
	if values, ok := q["account_ids"]; ok {
		if values[0] != "1" || q.Get("types") != "accounts" {
			return "", out, ledger.ErrInvalid
		}
		out.AccountIDsOnly = true
	}
	return q.Get("book_id"), out, nil
}

// searchRead answers the interface's finder for the signed-in owner. Results
// and the recorded state they describe come from one scoped read; the request's
// cancellation reaches the database.
func (a *webAPI) searchRead(w http.ResponseWriter, r *http.Request, session string) {
	book, query, err := searchQuery(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out ledger.SearchResult
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		out, err = owner.SearchBook(r.Context(), book, query)
		return err
	})
	if err != nil {
		searchFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func searchFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, ledger.ErrQueryTooLong) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "QUERY_TOO_LONG",
			"limits": map[string]int{"characters": ledger.SearchMaxCharacters, "terms": ledger.SearchMaxTerms, "term_characters": ledger.SearchMaxTermCharacters}})
		return
	}
	apiFailure(w, err)
}

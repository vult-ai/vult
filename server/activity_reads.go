package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vult-ai/vult/ledger"
)

func activityQuery(r *http.Request, page bool) (string, ledger.ActivityPageQuery, error) {
	out := ledger.ActivityPageQuery{Limit: 25}
	if len(r.URL.RawQuery) > 8192 {
		return "", out, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || !ledger.ValidID(q.Get("book_id")) {
		return "", out, ledger.ErrInvalid
	}
	// q is the Book search; search is its older name, kept until the interface moves.
	allowed := map[string]bool{"book_id": true, "q": true, "search": true, "kind": true, "from": true, "to": true, "account": true, "revision": true}
	if page {
		for _, key := range []string{"view", "limit", "offset", "dashboard_from", "dashboard_to"} {
			allowed[key] = true
		}
		if q.Get("view") != "transactions" {
			return "", out, ledger.ErrInvalid
		}
		for _, key := range []string{"dashboard_from", "dashboard_to"} {
			if value := q.Get(key); value != "" && !ledger.ValidDate(value) {
				return "", out, ledger.ErrInvalid
			}
		}
		if q.Get("dashboard_from") != "" && q.Get("dashboard_to") != "" && q.Get("dashboard_from") > q.Get("dashboard_to") {
			return "", out, ledger.ErrInvalid
		}
		for _, key := range []string{"limit", "offset"} {
			if value, exists := q[key]; exists {
				if len(value) != 1 {
					return "", out, ledger.ErrInvalid
				}
				n, err := strconv.Atoi(value[0])
				if err != nil {
					return "", out, ledger.ErrInvalid
				}
				if key == "limit" {
					out.Limit = n
				} else {
					out.Offset = n
				}
			}
		}
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return "", out, ledger.ErrInvalid
		}
	}
	search := q.Get("search")
	if _, named := q["q"]; named {
		if _, old := q["search"]; old && q.Get("search") != q.Get("q") {
			return "", out, ledger.ErrInvalid
		}
		search = q.Get("q")
	}
	out.ActivityFilters = ledger.ActivityFilters{Search: search, Kind: q.Get("kind"), From: q.Get("from"), To: q.Get("to"), Account: q.Get("account")}
	out.Revision = q.Get("revision")
	return q.Get("book_id"), out, nil
}

func (a *webAPI) activityRead(w http.ResponseWriter, r *http.Request, session string) {
	page := r.URL.Path == "/api/v1/state"
	book, query, err := activityQuery(r, page)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		if !page {
			var err error
			out, err = owner.ActivitySummary(r.Context(), book, query.ActivityFilters, query.Revision)
			return err
		}
		value, err := owner.Activity(r.Context(), book, query)
		if err != nil {
			return err
		}
		out = struct {
			ledger.ActivityPage
			Book webBook `json:"book"`
		}{value, browserBook(value.Book, time.Now())}
		return nil
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

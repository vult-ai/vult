package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/vult-ai/vult/ledger"
)

func (a *webAPI) stateRead(w http.ResponseWriter, r *http.Request, session string) {
	if len(r.URL.RawQuery) > 8192 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if q.Get("view") == "transactions" {
		a.activityRead(w, r, session)
		return
	}
	view := q.Get("view")
	metadata := view == "settings" || view == "documents" || view == "statements"
	if !metadata && view != "dashboard" && view != "accounts" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	allowed := map[string]bool{"book_id": true, "view": true, "bootstrap": true, "dashboard_from": true, "dashboard_to": true, "dashboard_unit": true, "account": true, "search": true, "kind": true, "from": true, "to": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	if !ledger.ValidID(q.Get("book_id")) || q.Get("bootstrap") != "" && (q.Get("bootstrap") != "1" || !metadata) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	// Transaction filters do not silently narrow a position or metadata read.
	for _, key := range []string{"account", "search", "kind", "from", "to"} {
		if q.Get(key) != "" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	from, to := q.Get("dashboard_from"), q.Get("dashboard_to")
	unit := q.Get("dashboard_unit")
	if q.Has("dashboard_unit") && (view != "dashboard" || unit == "") {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if from != "" && !ledger.ValidDate(from) || to != "" && !ledger.ValidDate(to) || from != "" && to != "" && from > to || !metadata && (from == "" || to == "") {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	book := q.Get("book_id")
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var state ledger.BrowserState
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		if unit != "" {
			state, err = owner.DashboardState(r.Context(), book, from, to, unit)
		} else {
			state, err = owner.BrowserState(r.Context(), book, view, from, to)
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	out := struct {
		ledger.BrowserState
		Book webBook `json:"book"`
	}{state, browserBook(state.Book, time.Now())}
	_ = json.NewEncoder(w).Encode(out)
}

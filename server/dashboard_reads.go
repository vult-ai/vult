package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

func (a *webAPI) dashboardRead(w http.ResponseWriter, r *http.Request, session string) {
	mode := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard")
	if len(r.URL.RawQuery) > 8192 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || len(q["from"]) != 1 || len(q["to"]) != 1 || !ledger.ValidID(q.Get("book_id")) || !ledger.ValidDate(q.Get("from")) || !ledger.ValidDate(q.Get("to")) || q.Get("from") > q.Get("to") {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	for key, values := range q {
		allowed := key == "book_id" || key == "from" || key == "to" || mode == "/projection" && (key == "view" || key == "unit")
		if len(values) != 1 || !allowed {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	if mode == "/projection" && q.Get("view") != "recognition" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	unit := q.Get("unit")
	if mode == "/projection" {
		if _, supplied := q["unit"]; supplied && unit == "" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		if unit == "" {
			unit = "day"
		}
		switch unit {
		case "day", "week", "month", "quarter", "year":
		default:
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	book := q.Get("book_id")
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		switch mode {
		case "":
			out, err = owner.Dashboard(r.Context(), book, q.Get("from"), q.Get("to"))
		case "/projection":
			out, err = owner.DashboardProjection(r.Context(), book, q.Get("from"), q.Get("to"), unit)
		case "/cash-summary":
			out, err = owner.CashSummary(r.Context(), book, q.Get("from"), q.Get("to"))
		default:
			return ledger.ErrNotFound
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

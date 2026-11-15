package server

import (
	"encoding/json"
	"net/http"

	"github.com/vult-ai/vult/ledger"
)

// Needs you shows what each Book asks its owner to do: add a receipt,
// switch a link's terms, look at a partnership's repayment.
const (
	remindersPath     = "/api/v1/reminders"
	remindersDonePath = "/api/v1/reminders/done"
)

func (a *webAPI) reminders(w http.ResponseWriter, r *http.Request, session string) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == remindersPath:
		q := r.URL.Query()
		book := q.Get("book_id")
		if len(q) != 1 || !ledger.ValidID(book) {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, err := a.sessions.BookTenant(r.Context(), session, book)
		if err != nil {
			apiFailure(w, err)
			return
		}
		var out []ledger.AgentLoop
		if err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			out, err = o.OwnerReminders(r.Context(), book)
			return err
		}); err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reminders": out})
	case r.Method == http.MethodPost && r.URL.Path == remindersDonePath:
		var q struct {
			BookID string `json:"book_id"`
			ID     string `json:"id"`
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
		if err != nil {
			apiFailure(w, err)
			return
		}
		if err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			return o.CloseReminder(r.Context(), q.BookID, who, q.ID)
		}); err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": q.ID, "status": "done"})
	default:
		failure(w, http.StatusNotFound)
	}
}

package server

import (
	"encoding/json"
	"net/http"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

const bookViewPreferencePath = "/api/v1/book-view-preference"

func (a *webAPI) saveBookViewPreference(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var patch struct {
		BookID    string                   `json:"book_id"`
		Scope     string                   `json:"scope"`
		Selection access.BookViewSelection `json:"selection"`
	}
	if err := readOwnerJSON(w, r, 1024, &patch, ""); err != nil {
		apiFailure(w, err)
		return
	}
	if err := a.sessions.SaveBookViewPreference(r.Context(), session, patch.BookID, patch.Scope, patch.Selection); err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "status": "saved"})
}

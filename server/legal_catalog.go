package server

import (
	"encoding/json"
	"net/http"

	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/legalidentity"
)

func (a *webAPI) legalCatalog(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) > 64 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	query := r.URL.Query()
	var catalog legalidentity.Registry
	if len(query) == 0 {
		catalog = legalidentity.Catalog()
	} else if len(query) == 1 && len(query["country"]) == 1 {
		var err error
		catalog, err = legalidentity.CatalogForCountry(query.Get("country"))
		if err != nil {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	} else {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"catalog": catalog})
}

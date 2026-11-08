package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/legalidentity"
)

func TestBrowserLegalCatalogIsSessionOnlyAndCountryScoped(t *testing.T) {
	f, s := browserServer(t)
	for _, query := range []string{"", "?country=US", "?country=", "?country=ZZ"} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/legal-identity-catalog"+query, f.session))
		var result struct {
			Catalog legalidentity.Registry `json:"catalog"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Catalog.Version != legalidentity.CatalogVersion || len(result.Catalog.Countries) != 250 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("catalog response unavailable or unbounded", w.Code)
		}
		for _, form := range result.Catalog.Forms {
			if query != "" && (query != "?country=US" || form.CountryCode != "US") {
				t.Fatal("catalog ignored its country scope", query, form.ID)
			}
		}
		if query == "" && len(result.Catalog.Forms) != 3450 {
			t.Fatal("full reference inventory incomplete")
		}
	}
	for _, query := range []string{"?country=UK", "?country=US&country=GB", "?tenant_id=" + f.tenant, "?country=" + strings.Repeat("x", 70)} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/legal-identity-catalog"+query, f.session))
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), `"catalog"`) {
			t.Fatal("invalid catalog filter returned reference data", w.Code)
		}
	}
	for _, tc := range []struct {
		session string
		bearer  bool
		want    int
	}{
		{"", false, http.StatusUnauthorized}, {f.session, true, http.StatusForbidden},
	} {
		r := browserRequest(http.MethodGet, "/api/v1/legal-identity-catalog?country=US", tc.session)
		if tc.bearer {
			r.Header.Set("Authorization", "Bearer example")
		}
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal("reference endpoint bypassed the browser boundary", w.Code)
		}
	}
}

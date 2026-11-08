package access

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const exampleReview = "AAAAAAAAAAAAAAAAAAAAAf____________________4"

func TestReviewContinuationAcceptsOnlyAnExactBoundedAddress(t *testing.T) {
	for _, query := range []string{
		"operation=" + exampleReview, "operation=short", "operation=" + exampleReview + "&operation=" + exampleReview,
		"return_to=https://example.com", "operation=" + exampleReview + "&return_to=/", "operation=%ZZ",
	} {
		r := httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login?"+query, nil)
		w := httptest.NewRecorder()
		accepted := startReviewContinuation(w, r)
		if accepted != (query == "operation="+exampleReview) {
			t.Fatal("unexpected continuation verdict", query)
		}
		if accepted {
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != reviewCookie || cookies[0].Value != exampleReview || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" || cookies[0].MaxAge != 300 {
				t.Fatal("review continuation cookie lost its bounds")
			}
		} else if len(w.Result().Cookies()) != 0 {
			t.Fatal("rejected address changed continuation state")
		}
	}
	r := httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login?operation="+exampleReview, nil)
	r.Header.Set("Authorization", "Bearer example")
	if startReviewContinuation(httptest.NewRecorder(), r) {
		t.Fatal("bearer credential became human review context")
	}
}

func TestReviewContinuationReturnsToReviewWithoutAnEffect(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, values := range [][]string{nil, {exampleReview}, {"https://example.com"}, {exampleReview, exampleReview}} {
			r := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/callback", nil)
			for _, value := range values {
				r.AddCookie(&http.Cookie{Name: reviewCookie, Value: value})
			}
			w := httptest.NewRecorder()
			h := &HTTP{local: local}
			want := "/auth/complete"
			if local {
				want = "/"
			}
			if len(values) == 1 && values[0] == exampleReview {
				want = "/auth/approve?operation=" + exampleReview
			}
			if got := h.signInDestination(w, r, "/auth/complete"); got != want {
				t.Fatal("sign-in returned to a substituted or wrong destination", got)
			}
			if len(values) != 0 {
				cookies := w.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Name != reviewCookie || cookies[0].MaxAge >= 0 {
					t.Fatal("sign-in retained its review continuation")
				}
			}
		}
	}
}

func TestNewLoginClearsCancelledReviewBeforeOrdinaryOrAgentSignIn(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, agent := range []bool{false, true} {
			login := httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login", nil)
			login.AddCookie(&http.Cookie{Name: reviewCookie, Value: exampleReview})
			w := httptest.NewRecorder()
			h := &HTTP{local: local}
			h.ServeHTTP(w, login)
			if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "same-origin" {
				t.Fatal("login form cannot preserve its same-origin proof")
			}
			cleared := false
			for _, cookie := range w.Result().Cookies() {
				cleared = cleared || cookie.Name == reviewCookie && cookie.MaxAge < 0
			}
			if !cleared {
				t.Fatal("cancelled review captured a new sign-in")
			}
			callback := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/callback", nil)
			pending := Random()
			if agent {
				callback.AddCookie(&http.Cookie{Name: pendingCookie, Value: pending})
			}
			want := "/auth/complete"
			if local {
				want = "/"
			} else if agent {
				want = "/oauth/authorize?request=" + pending
			}
			if got := h.signInDestination(httptest.NewRecorder(), callback, "/auth/complete"); got != want {
				t.Fatal("new sign-in returned to a cancelled review", got)
			}
		}
	}
}

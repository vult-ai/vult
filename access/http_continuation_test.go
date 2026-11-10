package access

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestEndedSessionReturnsToThePageItLeft(t *testing.T) {
	for _, local := range []bool{false, true} {
		login := httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login?return="+url.QueryEscape("/BOOK1/activity?range=month"), nil)
		w := httptest.NewRecorder()
		if !startReviewContinuation(w, login) {
			t.Fatal("a page to return to was refused")
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != returnCookie || cookies[0].MaxAge != 600 || !cookies[0].HttpOnly || !cookies[0].Secure {
			t.Fatal("the return page was not kept for sign-in", cookies)
		}
		done := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/callback", nil)
		done.AddCookie(cookies[0])
		w = httptest.NewRecorder()
		if got := (&HTTP{local: local}).signInDestination(w, done, "/auth/complete"); got != "/BOOK1/activity?range=month" {
			t.Fatal("sign-in did not return to the page the owner left", got)
		}
		if c := w.Result().Cookies(); len(c) != 1 || c[0].Name != returnCookie || c[0].MaxAge >= 0 {
			t.Fatal("the return page outlived sign-in")
		}
		// A review continuation still wins.
		done = httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/callback", nil)
		done.AddCookie(cookies[0])
		done.AddCookie(&http.Cookie{Name: reviewCookie, Value: exampleReview})
		if got := (&HTTP{local: local}).signInDestination(httptest.NewRecorder(), done, "/auth/complete"); got != "/auth/approve?operation="+exampleReview {
			t.Fatal("the return page replaced a review", got)
		}
	}
	for _, unsafe := range []string{"//example.com/", "https://example.com/", "/auth/logout", "/api/v1/books", "/BOOK1#x", "/a/../auth/login", "BOOK1"} {
		w := httptest.NewRecorder()
		if !startReviewContinuation(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login?return="+url.QueryEscape(unsafe), nil)) || len(w.Result().Cookies()) != 0 {
			t.Fatal("an unsafe return page was kept", unsafe)
		}
	}
	forged := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/callback", nil)
	forged.AddCookie(&http.Cookie{Name: returnCookie, Value: base64.RawURLEncoding.EncodeToString([]byte("//example.com/"))})
	if got := (&HTTP{}).signInDestination(httptest.NewRecorder(), forged, "/auth/complete"); got != "/auth/complete" {
		t.Fatal("a forged return cookie was followed", got)
	}
}

func TestSessionSaysSignedOutOnlyWithoutASession(t *testing.T) {
	w := httptest.NewRecorder()
	(&HTTP{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/session", nil))
	if w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"SIGNED_OUT"`) {
		t.Fatal("a browser without a session was not told it is signed out", w.Code, w.Body.String())
	}
}

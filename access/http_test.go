package access_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
)

func responseCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("expected cookie missing")
	return nil
}
func loginForm(t *testing.T, h http.Handler) (*http.Cookie, url.Values) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://ledger.example.com/auth/login", nil))
	if w.Code != 200 {
		t.Fatal("login page unavailable")
	}
	c := responseCookie(t, w, "__Host-vult-login")
	if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode || !strings.Contains(w.Body.String(), c.Value) {
		t.Fatal("login binding missing")
	}
	return c, url.Values{"csrf": {c.Value}}
}

func TestSignInHTTPCookiesCSRFAndLogout(t *testing.T) {
	f := fresh(t)
	tenant, _, id := f.owner(t, "alpha")
	calls := 0
	h, e := access.NewHTTP(f.service, nil, nil, func(*http.Request) (access.Identity, error) { calls++; return id, nil })
	if e != nil {
		t.Fatal(e)
	}
	c, form := loginForm(t, h)
	for _, variant := range []string{"missing-origin", "foreign-origin", "duplicate-origin", "missing-cookie", "duplicate-form", "large-body"} {
		t.Run(variant, func(t *testing.T) {
			body := form.Encode()
			if variant == "duplicate-form" {
				body += "&csrf=" + c.Value
			}
			if variant == "large-body" {
				body += "&padding=" + strings.Repeat("x", 8192)
			}
			r := httptest.NewRequest("POST", "https://ledger.example.com/auth/start", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://ledger.example.com")
			if variant != "missing-cookie" {
				r.AddCookie(c)
			}
			switch variant {
			case "missing-origin":
				r.Header.Del("Origin")
			case "foreign-origin":
				r.Header.Set("Origin", "https://foreign.example.com")
			case "duplicate-origin":
				r.Header.Add("Origin", "https://foreign.example.com")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 || calls != 0 {
				t.Fatal("unbound form reached identity provider")
			}
		})
	}
	r := httptest.NewRequest("POST", "https://ledger.example.com/auth/start", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://ledger.example.com")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" || calls != 1 {
		t.Fatal("valid login failed")
	}
	session := responseCookie(t, w, access.SessionCookie)
	if !session.Secure || !session.HttpOnly || session.Domain != "" || session.Path != "/" || session.MaxAge != 7200 || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie protections missing")
	}
	r = httptest.NewRequest("GET", "https://ledger.example.com/auth/session", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var info struct {
		Mode    string   `json:"mode"`
		UserID  string   `json:"user_id"`
		Tenants []string `json:"tenants"`
		CSRF    string   `json:"csrf"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Mode != "server" || len(info.UserID) != 64 || len(info.Tenants) != 1 || info.Tenants[0] != tenant || info.CSRF == "" || strings.Contains(w.Body.String(), id.Subject) || strings.Contains(w.Body.String(), id.Issuer) {
		t.Fatal("session response incorrect")
	}
	for _, valid := range []bool{false, true} {
		r = httptest.NewRequest("POST", "https://ledger.example.com/auth/logout", nil)
		r.AddCookie(session)
		r.Header.Set("Origin", "https://ledger.example.com")
		if valid {
			r.Header.Set("X-CSRF-Token", info.CSRF)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 403
		if valid {
			expected = 204
		}
		if w.Code != expected {
			t.Fatal("logout CSRF handling failed")
		}
	}
	r = httptest.NewRequest("GET", "https://ledger.example.com/auth/session", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("logged out session remained usable")
	}
}

func TestExternalSignInCannotEnrollUnknownOrStaleIdentity(t *testing.T) {
	f := fresh(t)
	_, _, known := f.owner(t, "alpha")
	for _, variant := range []string{"unknown", "stale", "future"} {
		t.Run(variant, func(t *testing.T) {
			id := known
			switch variant {
			case "unknown":
				id.Subject = "unmapped"
			case "stale":
				id.AuthenticatedAt = time.Now().Add(-10 * time.Minute)
			case "future":
				id.AuthenticatedAt = time.Now().Add(2 * time.Minute)
			}
			h, e := access.NewHTTP(f.service, nil, nil, func(*http.Request) (access.Identity, error) { return id, nil })
			if e != nil {
				t.Fatal(e)
			}
			c, form := loginForm(t, h)
			r := httptest.NewRequest("POST", "https://ledger.example.com/auth/start", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://ledger.example.com")
			r.AddCookie(c)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 401 || strings.Contains(w.Body.String(), id.Subject) {
				t.Fatal("invalid identity acquired a session or leaked provider data")
			}
		})
	}
}

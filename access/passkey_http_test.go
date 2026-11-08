package access_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/server"
)

func passkeyPost(t *testing.T, h http.Handler, path string, nonce, challenge *http.Cookie, body any, extra ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://ledger.example.com"+path, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://ledger.example.com")
	if nonce != nil {
		r.AddCookie(nonce)
		r.Header.Set("X-CSRF-Token", nonce.Value)
	}
	if challenge != nil {
		r.AddCookie(challenge)
	}
	for _, c := range extra {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPasskeyBrowserResumesPendingAgentConsent(t *testing.T) {
	f := fresh(t)
	p, setup, authenticator, oldSession, _ := enrollPasskey(t, f)
	book := f.book(t, setup.TenantID, oldSession)
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(server.Config{Origin: "https://ledger.example.com", Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1/32"}}, server.Options{Database: f.db.Runtime, Access: &server.Access{Sessions: f.service, HTTP: h}})
	if err != nil {
		t.Fatal(err)
	}
	client := f.client(t)
	verifier := access.Random()
	query := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {client.Redirects[0]}, "resource": {f.service.Resource()}, "code_challenge": {access.Challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {"ledger:write"}, "state": {"passkey-consent-state"}}
	w := networkRequest(f.db.Context, s.Handler, http.MethodGet, "/oauth/authorize?"+query.Encode(), "")
	if w.Code != http.StatusSeeOther {
		t.Fatal("agent authorization did not start", w.Code)
	}
	pending := responseCookie(t, w, "__Host-vult-oauth-request")
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, w.Header().Get("Location"), "", pending)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login" {
		t.Fatal("agent skipped human sign-in", w.Code)
	}
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, "/auth/login", "", pending, &http.Cookie{Name: "__Host-vult-review", Value: "AAAAAAAAAAAAAAAAAAAAAf____________________4"})
	if responseCookie(t, w, "__Host-vult-review").MaxAge >= 0 {
		t.Fatal("cancelled review captured a new agent authorization")
	}
	nonce := responseCookie(t, w, "__Host-vult-login")
	w = passkeyPost(t, h, "/auth/passkeys/login/options", nonce, nil, struct{}{}, pending)
	if w.Code != http.StatusOK {
		t.Fatal("passkey options unavailable", w.Code)
	}
	ceremony := responseCookie(t, w, "__Host-vult-passkey")
	proof := authenticator.assertion(t, access.PasskeyChallenge{Token: ceremony.Value, Options: json.RawMessage(w.Body.Bytes())}, "https://ledger.example.com", "ledger.example.com", 1, nil)
	w = passkeyPost(t, h, "/auth/passkeys/login/verify", nonce, ceremony, map[string]any{"credential": proof}, pending, &http.Cookie{Name: access.SessionCookie, Value: oldSession})
	var outcome struct {
		SignedIn bool   `json:"signed_in"`
		Continue string `json:"continue"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || !outcome.SignedIn || outcome.Continue != "/oauth/authorize?request="+pending.Value {
		t.Fatal("passkey sign-in lost pending agent consent", w.Code)
	}
	session := responseCookie(t, w, access.SessionCookie)
	if _, err := f.service.Identity(f.db.Context, oldSession); !errors.Is(err, access.ErrDenied) {
		t.Fatal("browser sign-in retained its replaced session", err)
	}
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, outcome.Continue, "", pending, session)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), book.ID) {
		t.Fatal("signed-in owner could not reach agent consent", w.Code)
	}
	csrfResponse := networkRequest(f.db.Context, s.Handler, http.MethodGet, "/auth/session", "", session)
	var csrf struct {
		Value string `json:"csrf"`
	}
	if json.Unmarshal(csrfResponse.Body.Bytes(), &csrf) != nil || csrf.Value == "" {
		t.Fatal("consent CSRF unavailable")
	}
	form := url.Values{"request": {pending.Value}, "csrf": {csrf.Value}, "book": {setup.TenantID + "/" + book.ID}, "access": {"write"}, "decision": {"allow"}}
	w = networkRequest(f.db.Context, s.Handler, http.MethodPost, "/oauth/authorize", form.Encode(), pending, session)
	if w.Code != http.StatusSeeOther {
		t.Fatal("fresh passkey owner could not consent", w.Code)
	}
	callback, err := url.Parse(w.Header().Get("Location"))
	if err != nil || callback.Query().Get("state") != "passkey-consent-state" {
		t.Fatal("agent callback lost its bound state")
	}
	form = url.Values{"grant_type": {"authorization_code"}, "code": {callback.Query().Get("code")}, "client_id": {client.ID}, "resource": {f.service.Resource()}, "redirect_uri": {client.Redirects[0]}, "code_verifier": {verifier}}
	w = networkRequest(f.db.Context, s.Handler, http.MethodPost, "/oauth/token", form.Encode())
	var tokens access.Tokens
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tokens) != nil || tokens.Access == "" {
		t.Fatal("agent did not obtain its scoped token", w.Code)
	}
	scope, err := f.service.ResolveAgent(f.db.Context, tokens.Access)
	if err != nil || scope.BookID != book.ID || scope.Access != "write" {
		t.Fatal("agent token has the wrong Book or access", err)
	}
}

func TestPasskeyBrowserLocalSessionReportsActualOwnership(t *testing.T) {
	f := fresh(t)
	host, err := loopback.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + host + ":51999"
	s, err := access.New(f.auth, f.owners, origin)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", origin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, s, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Passkeys: p, Local: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.IssueSession(f.db.Context, access.Identity{Issuer: access.BuiltinIssuer, Subject: "example-owner", AuthenticatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, origin+"/auth/session", nil)
	r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: raw})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var state struct {
		Mode    string   `json:"mode"`
		Tenants []string `json:"tenants"`
		CSRF    string   `json:"csrf"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Mode != "local" || len(state.Tenants) != 1 || state.Tenants[0] != setup.TenantID || state.CSRF == "" || strings.Contains(w.Body.String(), raw) {
		t.Fatal("local session misstated ownership or exposed its credential", w.Code)
	}
	if _, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Local: true}); err == nil {
		t.Fatal("local browser enabled without a passkey provider")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/auth/recover"
		if method == http.MethodPost {
			path = "/auth/passkeys/recover"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, origin+path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatal("local recovery bypassed native administration", method, w.Code)
		}
	}
	remote, err := f.operator.InitializePasskeys(f.db.Context, "", "other-owner", "https://ledger.example.com")
	if err != nil || remote.TenantID == setup.TenantID {
		t.Fatal("independent owner fixture unavailable", err)
	}
	remotePasskeys, err := access.NewPasskeys(f.db.Context, f.service, "other-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: remotePasskeys, Local: true}); err == nil {
		t.Fatal("remote origin presented as the local installation")
	}
}

func TestPasskeyBrowserSetupSignInAndSessionBoundaries(t *testing.T) {
	f := fresh(t)
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/setup", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Body.String(), "/auth/passkeys.js") || strings.Contains(w.Body.String(), setup.Token) || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("setup page leaked authority or relaxed its policy", w.Code)
	}
	nonce := responseCookie(t, w, "__Host-vult-login")
	if !nonce.Secure || !nonce.HttpOnly || nonce.Domain != "" || nonce.Path != "/" {
		t.Fatal("setup binding cookie is unsafe")
	}
	form := url.Values{"csrf": {nonce.Value}}
	unsupported := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/start", strings.NewReader(form.Encode()))
	unsupported.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unsupported.Header.Set("Origin", "https://ledger.example.com")
	unsupported.AddCookie(nonce)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, unsupported)
	var sessionCount int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM access.sessions").Scan(&sessionCount); err != nil || w.Code != http.StatusNotFound || sessionCount != 0 {
		t.Fatal("passkey-only sign-in reached an unsupported form provider", w.Code, err)
	}
	w = passkeyPost(t, h, "/auth/passkeys/register/options", nonce, nil, map[string]string{"setup_token": setup.Token})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), setup.Token) {
		t.Fatal("registration options unavailable or secret exposed", w.Code)
	}
	ceremony := responseCookie(t, w, "__Host-vult-passkey")
	a := newPasskeyAuthenticator(t)
	proof := a.registration(t, access.PasskeyChallenge{Token: ceremony.Value, Options: json.RawMessage(w.Body.Bytes())}, "https://ledger.example.com", "ledger.example.com")
	w = passkeyPost(t, h, "/auth/passkeys/register/verify", nonce, ceremony, map[string]any{"credential": proof})
	var outcome struct {
		SignedIn bool   `json:"signed_in"`
		Continue string `json:"continue"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || !outcome.SignedIn || outcome.Continue != "/" {
		t.Fatal("browser registration did not confirm sign-in", w.Code)
	}
	session := responseCookie(t, w, access.SessionCookie)
	if !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Domain != "" || session.Path != "/" || session.MaxAge != 7200 || strings.Contains(w.Body.String(), session.Value) {
		t.Fatal("session became a script-visible credential")
	}
	if id, err := f.service.Identity(f.db.Context, session.Value); err != nil || id.Subject != "example-owner" {
		t.Fatal("browser session has wrong identity", err)
	}
	w = passkeyPost(t, h, "/auth/passkeys/register/verify", nonce, ceremony, map[string]any{"credential": proof})
	if w.Code != http.StatusUnauthorized {
		t.Fatal("registration HTTP replay accepted", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/login", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sign in with a passkey") || strings.Contains(w.Body.String(), "<form") {
		t.Fatal("passkey-only login offered a broken form", w.Code)
	}
	nonce = responseCookie(t, w, "__Host-vult-login")
	w = passkeyPost(t, h, "/auth/passkeys/login/options", nonce, nil, map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatal("login options unavailable", w.Code)
	}
	ceremony = responseCookie(t, w, "__Host-vult-passkey")
	proof = a.assertion(t, access.PasskeyChallenge{Token: ceremony.Value, Options: json.RawMessage(w.Body.Bytes())}, "https://ledger.example.com", "ledger.example.com", 1, nil)
	w = passkeyPost(t, h, "/auth/passkeys/login/verify", nonce, ceremony, map[string]any{"credential": proof})
	if w.Code != http.StatusOK {
		t.Fatal("signed browser login failed", w.Code)
	}
	newSession := responseCookie(t, w, access.SessionCookie)
	if newSession.Value == session.Value {
		t.Fatal("login refreshed an old token instead of issuing a new session")
	}
	r := httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/session", nil)
	r.AddCookie(newSession)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var state struct {
		Mode    string   `json:"mode"`
		Tenants []string `json:"tenants"`
		CSRF    string   `json:"csrf"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Mode != "server" || len(state.Tenants) != 1 || state.Tenants[0] != setup.TenantID || state.CSRF == "" {
		t.Fatal("browser session did not resolve actual ownership", w.Code)
	}
	for _, path := range []string{"/auth/passkeys.js", "/auth/setup"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com"+path+"?setup="+setup.Token, nil))
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), setup.Token) {
			t.Fatal("setup authority accepted through logged query", w.Code)
		}
	}
}

func TestPasskeyBrowserRejectsAmbiguousAndCrossOriginRequests(t *testing.T) {
	f := fresh(t)
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/setup", nil))
	nonce := responseCookie(t, w, "__Host-vult-login")
	for _, variant := range []string{"missing-origin", "foreign-origin", "duplicate-origin", "missing-csrf", "wrong-csrf", "missing-cookie", "duplicate-cookie", "bearer", "wrong-type", "duplicate-type", "duplicate-json", "unknown-field", "large-body"} {
		t.Run(variant, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"setup_token": setup.Token})
			want := http.StatusForbidden
			if variant == "duplicate-json" {
				body = []byte(`{"setup_token":"` + setup.Token + `","setup_token":"` + setup.Token + `"}`)
				want = http.StatusBadRequest
			}
			if variant == "unknown-field" {
				body = []byte(`{"setup_token":"` + setup.Token + `","tenant_id":"example"}`)
				want = http.StatusBadRequest
			}
			if variant == "large-body" {
				body = []byte(strings.Repeat("x", 65<<10))
				want = http.StatusBadRequest
			}
			r := httptest.NewRequest(http.MethodPost, "https://ledger.example.com/auth/passkeys/register/options", strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://ledger.example.com")
			r.Header.Set("X-CSRF-Token", nonce.Value)
			if variant != "missing-cookie" {
				r.AddCookie(nonce)
			}
			switch variant {
			case "missing-origin":
				r.Header.Del("Origin")
			case "foreign-origin":
				r.Header.Set("Origin", "https://other.example.com")
			case "duplicate-origin":
				r.Header.Add("Origin", "https://other.example.com")
			case "missing-csrf":
				r.Header.Del("X-CSRF-Token")
			case "wrong-csrf":
				r.Header.Set("X-CSRF-Token", access.Random())
			case "duplicate-cookie":
				r.AddCookie(&http.Cookie{Name: nonce.Name, Value: access.Random()})
			case "bearer":
				r.Header.Set("Authorization", "Bearer example")
			case "wrong-type":
				r.Header.Set("Content-Type", "text/plain")
			case "duplicate-type":
				r.Header.Add("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), setup.Token) {
				t.Fatal("unbound passkey request reached registration", w.Code)
			}
		})
	}
	var challenges int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM access.passkey_challenges").Scan(&challenges); err != nil || challenges != 0 {
		t.Fatal("rejected browser requests persisted ceremonies", err)
	}
}

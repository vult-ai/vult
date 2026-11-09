package access

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
)

//go:embed passkeys.js
var passkeyScript string

const passkeyCookie = "__Host-vult-passkey"

var passkeySetupPage = template.Must(template.New("setup").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Your Vult is ready · Vult</title><link rel="stylesheet" href="/auth/style.css"><script src="/auth/particles.js" defer></script><script src="/auth/passkeys.js" defer></script></head><body><main>` + vultLockup + `{{if .Local}}<h1>Your Vult is ready</h1><p>It lives on this Mac, and only you can open it. Create the passkey you’ll open it with.</p><p id="passkey-setup-missing" hidden>This page needs the setup link Vult just opened. Close this tab and run <code>vult setup</code> again, or ask your agent to.</p>{{else}}<h1>Set up sign-in</h1><p>Use your setup link, or enter the setup code shown by Vult or supplied by your administrator.</p>{{end}}<label id="passkey-setup-label" hidden>Setup code<input id="passkey-setup-code" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="43"></label><button id="passkey-register" type="button">Create your passkey</button>{{if .Local}}<p class="fine" id="passkey-fine">Touch ID or your Mac’s password. Nothing to remember.</p>{{end}}<p id="passkey-status" role="status" aria-live="polite"></p>{{if .Authenticator}}<button id="authenticator-switch" class="secondary" type="button" hidden>Use an authenticator app instead</button><section id="authenticator-setup" hidden><h2>Use an authenticator app</h2><p class="start">Scan this with 1Password, Google Authenticator or a similar app, or enter the key by hand. Then enter the 6-digit code it shows.</p><div id="authenticator-qr" class="qr"></div><p class="key"><code id="authenticator-secret"></code></p><button id="authenticator-copy" class="secondary" type="button">Copy key</button><label>6-digit code<input id="authenticator-code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6"></label><button id="authenticator-verify" type="button">Continue</button><p id="authenticator-status" role="status" aria-live="polite"></p></section>{{end}}</main></body></html>`))

var passkeyRecoveryPage = template.Must(template.New("recover").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Recover sign-in · Vult</title><link rel="stylesheet" href="/auth/style.css"><script src="/auth/particles.js" defer></script><script src="/auth/passkeys.js" defer></script></head><body><main>` + vultLockup + `<h1>Recover sign-in</h1><p>Using a recovery code revokes your existing passkeys and sign-ins. You must create a replacement passkey before signing in again. Your Books are preserved.</p><label id="passkey-code-label">Recovery code<input id="passkey-recovery-code" autocomplete="off" type="password" maxlength="128" spellcheck="false"></label><button id="passkey-recover" type="button">Use recovery code</button><p id="passkey-status" role="status" aria-live="polite"></p><p><a href="/auth/login">Back to sign-in</a></p></main></body></html>`))

func (h *HTTP) passkeyRoute(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/auth/setup" && r.URL.Path != "/auth/recover" && r.URL.Path != "/auth/passkeys.js" && !strings.HasPrefix(r.URL.Path, "/auth/passkeys/") {
		return false
	}
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		fail(w, http.StatusForbidden)
		return true
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.local && (r.URL.Path == "/auth/recover" || r.URL.Path == "/auth/passkeys/recover") {
		fail(w, http.StatusNotFound)
		return true
	}
	if r.Method == http.MethodGet && r.URL.Path == "/auth/passkeys.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, passkeyScript)
		return true
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/auth/setup" || r.URL.Path == "/auth/recover") {
		nonce := Random()
		cookie(w, loginCookie, nonce, 600)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := passkeySetupPage
		if r.URL.Path == "/auth/recover" {
			page = passkeyRecoveryPage
		}
		_ = page.Execute(w, struct {
			CSRF          string
			Local         bool
			Authenticator bool
		}{nonce, h.local, h.local && h.totp != nil && r.URL.Path == "/auth/setup"})
		return true
	}
	if r.Method != http.MethodPost {
		fail(w, http.StatusNotFound)
		return true
	}
	var intent string
	switch r.URL.Path {
	case "/auth/passkeys/register/options", "/auth/passkeys/register/verify":
		intent = "registration"
	case "/auth/passkeys/login/options", "/auth/passkeys/login/verify":
		intent = "login"
	case "/auth/passkeys/recover":
		intent = "recovery"
	default:
		fail(w, http.StatusNotFound)
		return true
	}
	nonces := r.CookiesNamed(loginCookie)
	if len(nonces) != 1 || !validToken(nonces[0].Value) || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin || len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(nonces[0].Value), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		fail(w, http.StatusForbidden)
		return true
	}
	if len(r.Header.Values("Content-Type")) != 1 {
		fail(w, http.StatusForbidden)
		return true
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, http.StatusForbidden)
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, passkeyResponseLimit+1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusBadRequest)
		return true
	}
	if intent == "recovery" {
		var q struct {
			Code string `json:"code"`
		}
		if strictjson.Decode(data, &q) != nil {
			fail(w, http.StatusBadRequest)
			return true
		}
		setup, err := h.passkeys.Recover(r.Context(), q.Code, nonces[0].Value)
		if err != nil {
			fail(w, http.StatusUnauthorized)
			return true
		}
		cookie(w, SessionCookie, "", -1)
		cookie(w, passkeyCookie, "", -1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"setup_token": setup})
		return true
	}
	if strings.HasSuffix(r.URL.Path, "/options") {
		var q struct {
			Setup string `json:"setup_token,omitempty"`
		}
		if strictjson.Decode(data, &q) != nil || intent == "login" && q.Setup != "" {
			fail(w, http.StatusBadRequest)
			return true
		}
		var challenge PasskeyChallenge
		if intent == "registration" {
			challenge, err = h.passkeys.BeginRegistration(r.Context(), q.Setup, nonces[0].Value)
		} else {
			challenge, err = h.passkeys.BeginLogin(r.Context(), nonces[0].Value)
		}
		if err != nil {
			fail(w, http.StatusUnauthorized)
			return true
		}
		cookie(w, passkeyCookie, challenge.Token, 300)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(challenge.Options)
		return true
	}
	var q struct {
		Credential json.RawMessage `json:"credential"`
	}
	if strictjson.Decode(data, &q) != nil {
		fail(w, http.StatusBadRequest)
		return true
	}
	challenges := r.CookiesNamed(passkeyCookie)
	if len(challenges) != 1 || !validToken(challenges[0].Value) {
		fail(w, http.StatusForbidden)
		return true
	}
	cookie(w, passkeyCookie, "", -1)
	var session string
	if intent == "registration" {
		session, err = h.passkeys.FinishRegistration(r.Context(), challenges[0].Value, nonces[0].Value, q.Credential)
	} else {
		session, err = h.passkeys.FinishLogin(r.Context(), challenges[0].Value, nonces[0].Value, q.Credential)
	}
	if err != nil {
		fail(w, http.StatusUnauthorized)
		return true
	}
	cookie(w, loginCookie, "", -1)
	h.setSession(w, r, session)
	w.Header().Set("Content-Type", "application/json")
	destination := h.signInDestination(w, r, "/")
	if intent == "registration" && h.localRecovery() != nil {
		// A new local passkey: show the setup secrets once, in this browser.
		destination = "/auth/recovery-codes"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"signed_in": true, "continue": destination})
	return true
}

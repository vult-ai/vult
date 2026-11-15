package access

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
)

//go:embed passkeys.js
var passkeyScript string

// uiScript shows messages and the move into the Books on every sign-in page.
//
//go:embed ui.js
var uiScript string

const passkeyCookie = "__Host-vult-passkey"

var passkeySetupPage = template.Must(template.New("setup").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><meta name="vult-setup-again" content="{{if .Development}}Ask for a new setup link for this development Vult.{{else}}Close this tab and run vult setup again, or ask the agent to.{{end}}"><title>{{if .Local}}Secure Vult{{else}}Set up sign-in{{end}} · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script><script src="/auth/passkeys.js" defer></script></head><body>` + authBar + `<main>{{if .Local}}<section class="threshold" id="passkey-threshold" aria-labelledby="setup-title"><div class="dial" id="passkey-dial" data-phase="idle" aria-hidden="true"><svg viewBox="0 0 228 228"><g class="ring-outer"><circle cx="114" cy="114" r="110"/><circle class="ticks" cx="114" cy="114" r="110"/></g><g class="ring-inner"><circle class="dash" cx="114" cy="114" r="88"/><circle class="dots" cx="114" cy="114" r="98"/></g></svg><span class="core">` + fingerprintIcon("when-closed") + checkMarkIcon("when-open") + `</span></div><h1 id="setup-title">Secure Vult</h1><p class="lede">Books hold financial records. A passkey keeps everyone else out.</p><p id="passkey-setup-missing" hidden>{{if .Development}}This page needs a setup link for this development Vult. Ask for a new one.{{else}}This page needs the setup link Vult just opened. Close this tab and run <code>vult setup</code> again, or ask the agent to.{{end}}</p><label id="passkey-setup-label" hidden>Setup code<input id="passkey-setup-code" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="43"></label><button id="passkey-register" class="primary" type="button">` + fingerprintIcon("") + `Create a passkey</button><div id="passkey-status" class="notice-slot" aria-live="polite"><div></div></div><p class="fine" id="passkey-fine">Your Mac will ask where to save the passkey. Choose iCloud Keychain to use it in any browser on your Apple devices.</p>{{if .Authenticator}}<button id="authenticator-switch" class="link" type="button" hidden>Use an authenticator app instead</button>{{end}}<button id="setup-help-toggle" class="link" type="button" aria-expanded="false" aria-controls="setup-help">Having trouble?</button><div id="setup-help" class="threshold-help" role="region" aria-label="Setup help" hidden><p>A passkey can use this computer, a phone or a compatible security key. An authenticator app is another option.</p><p>{{if .Development}}If neither works here, ask for a new setup link for this development Vult.{{else}}If neither works here, close this tab and run <code>vult setup</code> again, or ask the agent to.{{end}}</p></div></section>{{if .Authenticator}}<section id="authenticator-setup" class="enroll" aria-labelledby="authenticator-title" hidden><h1 id="authenticator-title">Set up an authenticator</h1><ol class="steps"><li>Open an authenticator app.</li><li>Scan the QR code or enter the setup key.</li><li>Enter the 6-digit code to finish.</li></ol><div class="enroll-panel"><div class="manual"><code id="authenticator-secret" aria-label="Setup key"></code><button id="authenticator-copy" class="icon-button" type="button" aria-label="Copy setup key">` + copyIcon("when-ready") + checkMarkIcon("when-copied") + `</button></div><div id="authenticator-qr" class="qr"></div></div><div id="authenticator-status" class="notice-slot" aria-live="polite"><div></div></div><div class="otp-heading"><label for="authenticator-code">Authenticator code</label><span class="verifying" id="authenticator-verifying" hidden>Verifying…</span></div><div class="otp"><input id="authenticator-code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6" spellcheck="false"><div class="slots" id="authenticator-slots" aria-hidden="true"><span class="slot"></span><span class="slot"></span><span class="slot" data-separator></span><span class="slot"></span><span class="slot"></span><span class="slot"></span></div></div><button id="authenticator-verify" type="button" disabled>Verify and continue</button><div class="links"><button id="authenticator-back" class="link" type="button">Use a passkey instead</button></div></section>{{end}}{{else}}<h1>Set up sign-in</h1><p>Use the setup link, or enter the setup code shown by Vult or supplied by the administrator.</p><label id="passkey-setup-label" hidden>Setup code<input id="passkey-setup-code" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="43"></label><div id="passkey-status" class="notice-slot" aria-live="polite"><div></div></div><button id="passkey-register" type="button">Create a passkey</button>{{end}}</main></body></html>`))

var passkeyRecoveryPage = template.Must(template.New("recover").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Recover sign-in · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script><script src="/auth/passkeys.js" defer></script></head><body>` + authBar + `<main><h1>Recover sign-in</h1><p>Using a recovery code revokes all existing passkeys and sign-ins. You must create a replacement passkey before signing in again. Books are preserved.</p><label id="passkey-code-label">Recovery code<input id="passkey-recovery-code" autocomplete="off" type="password" maxlength="128" spellcheck="false"></label><div id="passkey-status" class="notice-slot" aria-live="polite"><div></div></div><button id="passkey-recover" type="button">Use recovery code</button><div class="links"><a href="/auth/login">Use another method</a></div></main></body></html>`))

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
			Development   bool
		}{nonce, h.local, h.local && h.totp != nil && r.URL.Path == "/auth/setup", developmentBuild})
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
	step := strings.TrimPrefix(path.Base(r.URL.Path), "/")
	nonces := r.CookiesNamed(loginCookie)
	if len(nonces) != 1 || !validToken(nonces[0].Value) {
		passkeyLog(intent, step, "refused: the page's sign-in check is missing or expired", nil)
		fail(w, http.StatusForbidden)
		return true
	}
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin {
		passkeyLog(intent, step, "refused: the request came from another address than "+h.sessions.origin, nil)
		fail(w, http.StatusForbidden)
		return true
	}
	if len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(nonces[0].Value), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		passkeyLog(intent, step, "refused: the page's sign-in check didn't match", nil)
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
			if intent == "registration" {
				passkeyLog(intent, step, "refused: the setup link is expired, replaced or unknown", err)
			} else {
				passkeyLog(intent, step, "refused", err)
			}
			fail(w, http.StatusUnauthorized)
			return true
		}
		passkeyLog(intent, step, "issued", nil)
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
		passkeyLog(intent, step, "refused: the browser didn't return the passkey challenge (it expires after five minutes)", nil)
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
		passkeyLog(intent, step, "not confirmed", err)
		fail(w, http.StatusUnauthorized)
		return true
	}
	passkeyLog(intent, step, "confirmed", nil)
	cookie(w, loginCookie, "", -1)
	h.setSession(w, r, session)
	w.Header().Set("Content-Type", "application/json")
	destination := h.signInDestination(w, r, "/")
	if intent == "registration" {
		// A new passkey: show the owner's new recovery codes once, in this browser.
		destination = "/auth/recovery-codes"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"signed_in": true, "continue": destination})
	return true
}

// passkeyLog records each passkey step and its outcome, so a failed sign-in
// or setup can be diagnosed. It never records tokens, codes or credentials.
func passkeyLog(intent, step, outcome string, err error) {
	if err != nil {
		reason := err.Error()
		if len(reason) > 200 {
			reason = reason[:200]
		}
		outcome += " (" + reason + ")"
	}
	log.Printf("passkey %s %s: %s", intent, step, outcome)
}

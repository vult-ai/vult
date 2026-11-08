package access

import (
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/operationref"
)

const SessionCookie = "__Host-vult-session"
const loginCookie = "__Host-vult-login"
const attemptCookie = "__Host-vult-attempt"
const reviewCookie = "__Host-vult-review"
const LoginCSS = `:root{color-scheme:light dark;--bg:#fafafa;--fg:#0a0a0a;--muted:#525252;--faint:#737373;--line:#e5e5e5;--card:#fff;--button:#0a0a0a;--button-fg:#fff;font-family:Geist,system-ui,-apple-system,sans-serif}@media (prefers-color-scheme:dark){:root{--bg:#0a0a0a;--fg:#fafafa;--muted:#a3a3a3;--faint:#8a8a8a;--line:#262626;--card:#141414;--button:#fafafa;--button-fg:#0a0a0a}}*{box-sizing:border-box}[hidden]{display:none!important}body{margin:0;display:grid;min-height:100dvh;place-items:center;background:var(--bg);color:var(--fg);-webkit-font-smoothing:antialiased}main{width:min(100% - 32px,440px);padding:48px 0;text-align:center}main>*{animation:rise .5s cubic-bezier(.2,.7,.2,1) both}main>*:nth-child(2){animation-delay:60ms}main>*:nth-child(3){animation-delay:120ms}main>*:nth-child(n+4){animation-delay:180ms}@keyframes rise{from{opacity:0;transform:translateY(8px)}}@media (prefers-reduced-motion:reduce){main>*{animation:none}}h1{font-size:30px;line-height:1.15;font-weight:600;letter-spacing:-.02em;margin:0 0 12px;text-wrap:balance}h2{font-size:15px;font-weight:600;margin:28px 0 4px;text-align:left}p{font-size:15px;line-height:1.55;color:var(--muted);margin:0 0 16px;text-wrap:pretty}.fine{font-size:13px;color:var(--faint)}.start{text-align:left}.qr{display:grid;place-items:center;margin:16px auto;width:200px;padding:12px;background:#fff;border-radius:12px}.qr svg{width:100%;height:auto;display:block}a{color:var(--fg)}code{font-family:"Geist Mono",ui-monospace,Menlo,monospace;font-size:14px}label{display:block;font-size:14px;margin-top:20px;text-align:left;color:var(--fg)}input:not([type=checkbox]):not([type=radio]),select{width:100%;padding:11px 12px;font:inherit;color:inherit;background:var(--card);border:1px solid var(--line);border-radius:10px;margin:8px 0 16px}fieldset{border:0;padding:0;margin:20px 0 8px;text-align:left}legend{font-size:14px;font-weight:600;margin-bottom:4px}fieldset label{display:flex;gap:10px;align-items:flex-start;margin-top:10px;color:var(--muted)}button,.button{display:block;width:100%;border:1px solid var(--button);border-radius:10px;background:var(--button);color:var(--button-fg);padding:13px;font:inherit;font-weight:500;cursor:pointer;text-decoration:none;text-align:center;margin-top:12px}button[value=deny],button.secondary,.button.secondary{background:transparent;color:var(--fg);border-color:var(--line)}button:disabled,.button[aria-disabled=true]{opacity:.45;cursor:default;pointer-events:none}:focus-visible{outline:2px solid var(--fg);outline-offset:3px}[role=status]{min-height:22px;font-size:14px;color:var(--faint);margin-top:12px}.codes{list-style:none;padding:0;margin:20px 0 8px;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;counter-reset:code}.codes li{display:flex;gap:10px;align-items:baseline;padding:10px 12px;border:1px solid var(--line);border-radius:10px;background:var(--card);counter-increment:code}.codes li::before{content:counter(code,decimal-leading-zero);font:12px "Geist Mono",ui-monospace,Menlo,monospace;color:var(--faint)}.key{padding:12px;border:1px solid var(--line);border-radius:10px;background:var(--card);overflow-wrap:anywhere;text-align:left}.row{display:flex;gap:8px;margin:12px 0}.row .button{margin:0}.check{display:flex;gap:10px;justify-content:center;align-items:center;margin:20px 0 4px;color:var(--fg)}@media (max-width:420px){.codes{grid-template-columns:1fr}}`

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Sign in · Vult</title><link rel="stylesheet" href="/auth/style.css">{{if .Passkeys}}<script src="/auth/passkeys.js" defer></script>{{end}}</head><body><main><h1>Sign in to Vult</h1>{{if .Passkeys}}<button id="passkey-signin" type="button">Sign in with a passkey</button><p id="passkey-status" role="status" aria-live="polite"></p>{{if .Authenticator}}<form id="authenticator-signin"><h2>Use an authenticator app instead</h2><label>6-digit code<input id="authenticator-code" name="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6" required></label><button type="submit">Sign in with the code</button><p id="authenticator-status" role="status" aria-live="polite"></p></form>{{end}}{{if .Recovery}}<p><a href="/auth/recover">Use a recovery code</a></p>{{end}}{{end}}{{if .UseForm}}<form method="post" action="/auth/start"><input type="hidden" name="csrf" value="{{.CSRF}}">{{if .Builtin}}<label>Username<input name="username" autocomplete="username" required maxlength="80"></label><label>Passphrase<input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label><label>Authenticator or recovery code<input name="code" autocomplete="one-time-code" required maxlength="128"></label>{{end}}<button type="submit">{{if .Builtin}}Sign in{{else}}Continue to sign in{{end}}</button></form>{{end}}<p>Use the identity configured by your server operator.</p></main></body></html>`))

type ExternalIdentity func(*http.Request) (Identity, error)
type HTTP struct {
	sessions *Service
	builtin  *Builtin
	oidc     *OIDC
	external ExternalIdentity
	page     func(http.ResponseWriter, *http.Request, string)
	passkeys *Passkeys
	local    bool
	agents   LocalAgents
	totp     *LocalAuthenticator
}

type HTTPOptions struct {
	Passkeys *Passkeys
	Local    bool
	// Authenticator adds authenticator-app sign-in for the passkey owner.
	// Local only.
	Authenticator *LocalAuthenticator
	// Agents decides requests from agents on this computer. Local only.
	Agents LocalAgents
}

// NewExternalHTTP lets an identity provider render sign-in while core owns the
// nonce, session cookie and final membership check. The renderer receives only
// the login CSRF nonce, never owner or installation authority.
func NewExternalHTTP(s *Service, external ExternalIdentity, page func(http.ResponseWriter, *http.Request, string)) (*HTTP, error) {
	h, e := NewHTTP(s, nil, nil, external)
	if e != nil || page == nil {
		return nil, ErrDenied
	}
	h.page = page
	return h, nil
}

func NewHTTP(s *Service, b *Builtin, o *OIDC, external ExternalIdentity, options ...HTTPOptions) (*HTTP, error) {
	if len(options) > 1 {
		return nil, ErrDenied
	}
	var p *Passkeys
	local := false
	var agents LocalAgents
	var authenticator *LocalAuthenticator
	if len(options) == 1 {
		p = options[0].Passkeys
		local = options[0].Local
		agents = options[0].Agents
		authenticator = options[0].Authenticator
	}
	if agents != nil && !local {
		return nil, ErrDenied
	}
	if authenticator != nil && (!local || p == nil || authenticator.sessions != s || authenticator.subject != p.subject || authenticator.identityID != p.identityID) {
		return nil, ErrDenied
	}
	n := 0
	if b != nil {
		n++
	}
	if o != nil {
		n++
	}
	if external != nil {
		n++
	}
	if p != nil && b == nil {
		n++
	}
	if s == nil || n != 1 || b != nil && b.sessions != s || o != nil && o.sessions != s || p != nil && (p.sessions != s || o != nil || external != nil || b != nil && b.username != p.subject) {
		return nil, ErrDenied
	}
	if local {
		origin, err := url.Parse(s.origin)
		if err != nil || !loopback.ValidHostname(origin.Hostname()) || p == nil || o != nil || external != nil {
			return nil, ErrDenied
		}
	}
	return &HTTP{sessions: s, builtin: b, oidc: o, external: external, passkeys: p, local: local, agents: agents, totp: authenticator}, nil
}
func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func csrf(raw string) string { return Hash("csrf:" + raw) }

// UserID is a stable pseudonymous key for one verified identity. It lets the
// browser keep personal settings apart without learning issuer or subject.
func UserID(id Identity) string { return Hash("user:" + id.Issuer + "\x00" + id.Subject) }
func fail(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"error\":\"authorization unavailable\"}\n")
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if h.passkeys != nil {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		if h.passkeyRoute(w, r) {
			return
		}
	}
	if h.totp != nil && strings.HasPrefix(r.URL.Path, "/auth/authenticator/") {
		h.authenticatorRoute(w, r)
		return
	}
	if h.agents != nil && r.URL.Path == "/auth/agent" {
		h.agentRoute(w, r)
		return
	}
	if r.URL.Path == "/auth/recovery-codes" && h.localRecovery() != nil {
		h.recoveryRoute(w, r)
		return
	}
	if r.URL.Path == "/auth/recovery.js" && r.Method == http.MethodGet && h.localRecovery() != nil {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, recoveryScript)
		return
	}
	switch {
	case r.URL.Path == "/auth/style.css" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/css")
		_, _ = io.WriteString(w, LoginCSS)
	case r.URL.Path == "/auth/login" && r.Method == "GET":
		if !startReviewContinuation(w, r) {
			fail(w, http.StatusBadRequest)
			return
		}
		w.Header().Set("Referrer-Policy", "same-origin")
		nonce := Random()
		cookie(w, loginCookie, nonce, 600)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if h.page != nil {
			h.page(w, r, nonce)
			return
		}
		_ = loginPage.Execute(w, struct {
			CSRF          string
			Builtin       bool
			Passkeys      bool
			UseForm       bool
			Recovery      bool
			Authenticator bool
		}{nonce, h.builtin != nil, h.passkeys != nil, h.builtin != nil || h.oidc != nil || h.external != nil, h.passkeys != nil && !h.local, h.totp != nil && h.totp.Ready(r.Context())})
	case r.URL.Path == "/auth/start" && r.Method == "POST":
		h.start(w, r)
	case r.URL.Path == "/auth/callback" && r.Method == "GET":
		h.callback(w, r)
	case r.URL.Path == "/auth/session" && r.Method == "GET":
		raw, e := r.Cookie(SessionCookie)
		if e != nil {
			fail(w, 401)
			return
		}
		id, e := h.sessions.Identity(r.Context(), raw.Value)
		if e != nil {
			fail(w, 401)
			return
		}
		tenants, e := h.sessions.Tenants(r.Context(), raw.Value)
		if e != nil {
			fail(w, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mode := "server"
		if h.local {
			mode = "local"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode, "user_id": UserID(id), "tenants": tenants, "csrf": csrf(raw.Value), "agent_address": h.sessions.Resource()})
	case r.URL.Path == "/auth/complete" && r.Method == "GET":
		raw, e := r.Cookie(SessionCookie)
		if e != nil {
			fail(w, 401)
			return
		}
		if _, e = h.sessions.Identity(r.Context(), raw.Value); e != nil {
			fail(w, 401)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Signed in · Vult</title><link rel="stylesheet" href="/auth/style.css"></head><body><main><h1>Signed in</h1><p>You can return to your agent to continue.</p></main></body></html>`)
	case r.URL.Path == "/auth/logout" && r.Method == "POST":
		raw, e := r.Cookie(SessionCookie)
		if e != nil || !h.CheckCSRF(r, raw.Value) {
			fail(w, 403)
			return
		}
		if h.sessions.EndSession(r.Context(), raw.Value) != nil {
			fail(w, 401)
			return
		}
		cookie(w, SessionCookie, "", -1)
		w.WriteHeader(204)
	default:
		fail(w, 404)
	}
}

func (h *HTTP) CheckCSRF(r *http.Request, session string) bool {
	return len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == h.sessions.origin && validToken(session) && len(r.Header.Values("X-CSRF-Token")) == 1 && subtle.ConstantTimeCompare([]byte(csrf(session)), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

func (h *HTTP) CheckLoginCSRF(r *http.Request) bool {
	c, e := r.Cookie(loginCookie)
	return e == nil && validToken(c.Value) && len(r.Header.Values("Origin")) == 1 && r.Header.Get("Origin") == h.sessions.origin && len(r.Header.Values("X-CSRF-Token")) == 1 && subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

func (h *HTTP) start(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil && h.builtin == nil && h.external == nil {
		fail(w, http.StatusNotFound)
		return
	}
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin || len(r.Header.Values("Content-Type")) != 1 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		fail(w, 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		fail(w, 403)
		return
	}
	login, e := r.Cookie(loginCookie)
	if e != nil || !validToken(login.Value) || len(r.PostForm["csrf"]) != 1 || subtle.ConstantTimeCompare([]byte(login.Value), []byte(r.PostForm.Get("csrf"))) != 1 {
		fail(w, 403)
		return
	}
	cookie(w, loginCookie, "", -1)
	if h.oidc != nil {
		destination, attempt, e := h.oidc.Start(r.Context())
		if e != nil {
			fail(w, 401)
			return
		}
		cookie(w, attemptCookie, attempt, 300)
		http.Redirect(w, r, destination, http.StatusSeeOther)
		return
	}
	var session string
	if h.builtin != nil {
		for _, field := range []string{"username", "password", "code"} {
			if len(r.PostForm[field]) != 1 {
				fail(w, 403)
				return
			}
		}
		session, e = h.builtin.SignIn(r.Context(), r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("code"))
	} else {
		identity, err := h.external(r)
		if err != nil {
			fail(w, 401)
			return
		}
		session, e = h.sessions.IssueSession(r.Context(), identity)
	}
	if e != nil {
		fail(w, 401)
		return
	}
	h.signedIn(w, r, session)
}
func (h *HTTP) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	attempt, e := r.Cookie(attemptCookie)
	if h.oidc == nil || e != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("error") != "" {
		fail(w, 401)
		return
	}
	session, e := h.oidc.Callback(r.Context(), attempt.Value, q.Get("state"), q.Get("code"))
	cookie(w, attemptCookie, "", -1)
	if e != nil {
		fail(w, 401)
		return
	}
	h.signedIn(w, r, session)
}
func (h *HTTP) signedIn(w http.ResponseWriter, r *http.Request, session string) {
	h.setSession(w, r, session)
	http.Redirect(w, r, h.signInDestination(w, r, "/auth/complete"), http.StatusSeeOther)
}

func (h *HTTP) setSession(w http.ResponseWriter, r *http.Request, session string) {
	if old, e := r.Cookie(SessionCookie); e == nil {
		_ = h.sessions.EndSession(r.Context(), old.Value)
	}
	cookie(w, SessionCookie, session, int((2 * time.Hour).Seconds()))
}

func (h *HTTP) signInDestination(w http.ResponseWriter, r *http.Request, fallback string) string {
	if reviews := r.CookiesNamed(reviewCookie); len(reviews) != 0 {
		cookie(w, reviewCookie, "", -1)
		if len(reviews) == 1 {
			if _, _, ok := operationref.Decode(reviews[0].Value); ok {
				return "/auth/approve?operation=" + reviews[0].Value
			}
		}
	}
	if h.local {
		if agents := r.CookiesNamed(agentCookie); len(agents) != 0 {
			cookie(w, agentCookie, "", -1)
			if len(agents) == 1 && validAgentRequest(agents[0].Value) {
				return "/auth/agent?request=" + agents[0].Value
			}
		}
		return "/"
	}
	if pending := r.CookiesNamed(pendingCookie); len(pending) == 1 && validToken(pending[0].Value) {
		return "/oauth/authorize?request=" + pending[0].Value
	}
	return fallback
}

func startReviewContinuation(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery == "" {
		if len(r.CookiesNamed(reviewCookie)) != 0 {
			cookie(w, reviewCookie, "", -1)
		}
		return true
	}
	if len(r.URL.RawQuery) > 128 || len(r.Header.Values("Authorization")) != 0 {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["operation"]) != 1 {
		return false
	}
	operation := query.Get("operation")
	if _, _, ok := operationref.Decode(operation); !ok {
		return false
	}
	cookie(w, reviewCookie, operation, 300)
	return true
}

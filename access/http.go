package access

import (
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
)

const SessionCookie = "__Host-vult-session"
const loginCookie = "__Host-vult-login"
const attemptCookie = "__Host-vult-attempt"
const LoginCSS = `:root{color-scheme:light dark;font-family:system-ui,sans-serif}body{margin:0;display:grid;min-height:100dvh;place-items:center}main{box-sizing:border-box;width:min(100% - 32px,420px);border:1px solid #8885;border-radius:12px;padding:32px}h1{font-size:26px;line-height:1.2}p{font-size:14px;line-height:1.5}label{display:block;font-size:14px;margin-top:24px}input{box-sizing:border-box;width:100%;padding:10px;font:inherit;border:1px solid #888;border-radius:6px;margin:8px 0 16px}button{width:100%;border:0;border-radius:6px;background:#171717;color:white;padding:12px;font:inherit;cursor:pointer}:focus-visible{outline:3px solid #007df9;outline-offset:3px}`

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · Vult</title><link rel="stylesheet" href="/auth/style.css"></head><body><main><h1>Sign in to Vult</h1><form method="post" action="/auth/start"><input type="hidden" name="csrf" value="{{.CSRF}}">{{if .Builtin}}<label>Username<input name="username" autocomplete="username" required maxlength="80"></label><label>Passphrase<input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label><label>Authenticator or recovery code<input name="code" autocomplete="one-time-code" required maxlength="128"></label>{{end}}<button type="submit">{{if .Builtin}}Sign in{{else}}Continue to sign in{{end}}</button></form><p>Use the identity configured by your server operator.</p></main></body></html>`))

type ExternalIdentity func(*http.Request) (Identity, error)
type HTTP struct {
	sessions *Service
	builtin  *Builtin
	oidc     *OIDC
	external ExternalIdentity
	page     func(http.ResponseWriter, *http.Request, string)
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

func NewHTTP(s *Service, b *Builtin, o *OIDC, external ExternalIdentity) (*HTTP, error) {
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
	if s == nil || n != 1 || b != nil && b.sessions != s || o != nil && o.sessions != s {
		return nil, ErrDenied
	}
	return &HTTP{sessions: s, builtin: b, oidc: o, external: external}, nil
}
func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func csrf(raw string) string { return Hash("csrf:" + raw) }
func fail(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"error\":\"authorization unavailable\"}\n")
}

func (h *HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	switch {
	case r.URL.Path == "/auth/style.css" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/css")
		_, _ = io.WriteString(w, LoginCSS)
	case r.URL.Path == "/auth/login" && r.Method == "GET":
		nonce := Random()
		cookie(w, loginCookie, nonce, 600)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if h.page != nil {
			h.page(w, r, nonce)
			return
		}
		_ = loginPage.Execute(w, struct {
			CSRF    string
			Builtin bool
		}{nonce, h.builtin != nil})
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
		if _, e = h.sessions.Identity(r.Context(), raw.Value); e != nil {
			fail(w, 401)
			return
		}
		tenants, e := h.sessions.Tenants(r.Context(), raw.Value)
		if e != nil {
			fail(w, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tenants": tenants, "csrf": csrf(raw.Value)})
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
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin || len(r.Header.Values("Content-Type")) != 1 || strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") == false {
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
		http.Redirect(w, r, destination, 303)
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
	if old, e := r.Cookie(SessionCookie); e == nil {
		_ = h.sessions.EndSession(r.Context(), old.Value)
	}
	cookie(w, SessionCookie, session, int((2 * time.Hour).Seconds()))
	destination := "/auth/complete"
	if pending, e := r.Cookie("__Host-vult-oauth-request"); e == nil && validToken(pending.Value) {
		destination = "/oauth/authorize?request=" + pending.Value
	}
	http.Redirect(w, r, destination, 303)
}

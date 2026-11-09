package access

import (
	"context"
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
	"time"
)

// LocalAgents lets the local browser decide requests from agents on this
// computer. The ledger service checks the owner's session itself.
type LocalAgents interface {
	AgentClient(ctx context.Context, session, request string) (string, error)
	DecideAgent(ctx context.Context, session, request, book, access string, deny bool) error
}

const agentCookie = "__Host-vult-agent"

// Approving an agent needs a passkey sign-in this recent. The ledger service
// allows five minutes; the page asks again a little sooner.
const agentFreshness = 4 * time.Minute

func validAgentRequest(s string) bool {
	if len(s) != 43 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

var agentPage = template.Must(template.New("agent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect {{.Client}} · Vult</title><link rel="stylesheet" href="/auth/style.css"><script src="/auth/particles.js" defer></script></head><body><main>` + vultLockup + `
<h1>Connect {{.Client}}</h1>
<p><strong>An app on this computer</strong> calls itself “{{.Client}}” and asked to use Vult. Allow it only if you just asked your agent to use Vult.</p>
<form method="post" action="/auth/agent">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="request" value="{{.Request}}">
<label>Book<select name="book">{{range .Books}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
<fieldset><legend>It may</legend>
<label><input type="radio" name="access" value="write" checked> Record: post entries directly</label>
<label><input type="radio" name="access" value="propose"> Propose: draft entries for your approval</label>
<label><input type="radio" name="access" value="read"> Read: view only</label>
</fieldset>
<p>It can't manage your Books, your sign-in or other agents. Access lasts 8 hours; your agent asks again after that.</p>
<button name="decision" value="allow" type="submit">Allow</button>
<button name="decision" value="deny" type="submit">Don't allow</button>
</form></main></body></html>`))

var agentMessage = template.Must(template.New("agent-message").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · Vult</title><link rel="stylesheet" href="/auth/style.css"><script src="/auth/particles.js" defer></script></head><body><main>` + vultLockup + `<h1>{{.Title}}</h1><p>{{.Body}}</p>{{if .Link}}<p><a href="{{.Link}}">{{.LinkText}}</a></p>{{end}}</main></body></html>`))

func agentNotice(w http.ResponseWriter, status int, title, body, link, linkText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = agentMessage.Execute(w, struct{ Title, Body, Link, LinkText string }{title, body, link, linkText})
}

// freshSession returns the owner's session when its passkey sign-in is recent.
func (h *HTTP) freshSession(r *http.Request) (string, bool) {
	raw, err := r.Cookie(SessionCookie)
	if err != nil || !validToken(raw.Value) {
		return "", false
	}
	id, err := h.sessions.Identity(r.Context(), raw.Value)
	if err != nil || time.Since(id.AuthenticatedAt) > agentFreshness {
		return "", false
	}
	return raw.Value, true
}

func (h *HTTP) agentRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if len(r.Header.Values("Authorization")) != 0 {
		fail(w, http.StatusForbidden)
		return
	}
	var request string
	switch r.Method {
	case http.MethodGet:
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["request"]) != 1 {
			fail(w, http.StatusBadRequest)
			return
		}
		request = query.Get("request")
	case http.MethodPost:
		if r.URL.RawQuery != "" || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin {
			fail(w, http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			fail(w, http.StatusBadRequest)
			return
		}
		request = r.PostForm.Get("request")
	default:
		fail(w, http.StatusMethodNotAllowed)
		return
	}
	if !validAgentRequest(request) {
		fail(w, http.StatusBadRequest)
		return
	}
	session, fresh := h.freshSession(r)
	if !fresh {
		// Sign in with the passkey, then come straight back here.
		cookie(w, agentCookie, request, 600)
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	cookie(w, agentCookie, "", -1)
	client, err := h.agents.AgentClient(r.Context(), session, request)
	if err != nil {
		agentNotice(w, http.StatusNotFound, "This request has ended", "It was already answered, or it expired after 10 minutes. Ask your agent to try again.", "/", "Open Vult")
		return
	}
	if r.Method == http.MethodPost {
		form := r.PostForm
		if len(form["csrf"]) != 1 || subtle.ConstantTimeCompare([]byte(form.Get("csrf")), []byte(csrf(session))) != 1 {
			fail(w, http.StatusForbidden)
			return
		}
		deny := form.Get("decision") == "deny"
		if !deny && form.Get("decision") != "allow" {
			fail(w, http.StatusBadRequest)
			return
		}
		book, access := "", ""
		if !deny {
			book, access = form.Get("book"), form.Get("access")
		}
		if err := h.agents.DecideAgent(r.Context(), session, request, book, access, deny); err != nil {
			agentNotice(w, http.StatusConflict, "That didn't go through", "Nothing was changed. Ask your agent to try again.", "/", "Open Vult")
			return
		}
		if deny {
			agentNotice(w, http.StatusOK, client+" was not connected", "It has no access to your Books. You can close this page.", "", "")
			return
		}
		if h.local {
			// Back to the Book, where the owner watches the agent's work.
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		agentNotice(w, http.StatusOK, client+" is connected", "Go back to "+client+". It continues on its own, and anything it records waits for your approval in Vult.", "/", "Open Vult")
		return
	}
	type choice struct{ ID, Name string }
	books := []choice{}
	rows, err := h.sessions.auth.Query(r.Context(), "SELECT book_id::text,name FROM access.consent_books($1,$2) LIMIT 101", Hash(session), h.sessions.origin)
	if err != nil {
		fail(w, http.StatusServiceUnavailable)
		return
	}
	for rows.Next() {
		var c choice
		if rows.Scan(&c.ID, &c.Name) != nil {
			rows.Close()
			fail(w, http.StatusServiceUnavailable)
			return
		}
		books = append(books, c)
	}
	rows.Close()
	if rows.Err() != nil || len(books) > 100 {
		fail(w, http.StatusServiceUnavailable)
		return
	}
	if len(books) == 0 {
		agentNotice(w, http.StatusOK, "Create your Book first", client+" records into a Book. Create one, then come back to this page.", "/", "Create your Book")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = agentPage.Execute(w, struct {
		Client, CSRF, Request string
		Books                 []choice
	}{client, csrf(session), request, books})
}

package access

import (
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const pendingCookie = "__Host-vult-oauth-request"

type OAuthHTTP struct{ s *Service }

func NewOAuthHTTP(s *Service) *OAuthHTTP { return &OAuthHTTP{s} }
func oauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func oauthFailure(w http.ResponseWriter, status int, code string) {
	oauthJSON(w, status, map[string]string{"error": code, "error_description": "Authorization could not be confirmed. Restart sign-in or reconnect the app."})
}
func single(v url.Values) bool {
	for _, values := range v {
		if len(values) != 1 {
			return false
		}
	}
	return true
}

func (h *OAuthHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/oauth/authorize" && len(r.Header.Values("Cookie")) != 0 {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	switch {
	case (r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource/mcp") && r.Method == "GET" && r.URL.RawQuery == "":
		oauthJSON(w, 200, map[string]any{"resource": h.s.Resource(), "authorization_servers": []string{h.s.origin}, "scopes_supported": []string{"ledger:read", "ledger:write"}, "bearer_methods_supported": []string{"header"}})
	case r.URL.Path == "/.well-known/oauth-authorization-server" && r.Method == "GET" && r.URL.RawQuery == "":
		oauthJSON(w, 200, map[string]any{"issuer": h.s.origin, "authorization_endpoint": h.s.origin + "/oauth/authorize", "token_endpoint": h.s.origin + "/oauth/token", "registration_endpoint": h.s.origin + "/oauth/register", "authorization_response_iss_parameter_supported": true, "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{"ledger:read", "ledger:write"}})
	case r.URL.Path == "/oauth/register" && r.Method == "POST" && r.URL.RawQuery == "":
		h.register(w, r)
	case r.URL.Path == "/oauth/token" && r.Method == "POST" && r.URL.RawQuery == "":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || len(r.Header.Values("Authorization")) != 0 {
			oauthFailure(w, 400, "invalid_request")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if r.ParseForm() != nil || !single(r.PostForm) {
			oauthFailure(w, 400, "invalid_request")
			return
		}
		tokens, e := h.s.Exchange(r.Context(), r.PostForm)
		if e != nil {
			oauthFailure(w, 400, "invalid_grant")
			return
		}
		oauthJSON(w, 200, tokens)
	case r.URL.Path == "/oauth/authorize" && r.Method == "GET":
		h.authorize(w, r)
	case r.URL.Path == "/oauth/authorize" && r.Method == "POST" && r.URL.RawQuery == "":
		h.consent(w, r)
	default:
		oauthFailure(w, 404, "invalid_request")
	}
}
func (h *OAuthHTTP) register(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		oauthFailure(w, 400, "invalid_client_metadata")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		oauthFailure(w, 400, "invalid_client_metadata")
		return
	}
	// Registration metadata is extensible; ignored fields never grant access.
	var metadata map[string]json.RawMessage
	if strictjson.Decode(b, &metadata) != nil {
		oauthFailure(w, 400, "invalid_client_metadata")
		return
	}
	known := make(map[string]json.RawMessage)
	for _, key := range []string{"client_name", "redirect_uris", "token_endpoint_auth_method", "grant_types", "response_types"} {
		if value, ok := metadata[key]; ok {
			known[key] = value
		}
	}
	b, _ = json.Marshal(known)
	var q struct {
		Name      string   `json:"client_name"`
		Redirects []string `json:"redirect_uris"`
		Auth      string   `json:"token_endpoint_auth_method"`
		Grants    []string `json:"grant_types"`
		Responses []string `json:"response_types"`
	}
	if strictjson.Decode(b, &q) != nil || q.Auth != "" && q.Auth != "none" {
		oauthFailure(w, 400, "invalid_client_metadata")
		return
	}
	for _, g := range q.Grants {
		if g != "authorization_code" && g != "refresh_token" {
			oauthFailure(w, 400, "invalid_client_metadata")
			return
		}
	}
	for _, g := range q.Responses {
		if g != "code" {
			oauthFailure(w, 400, "invalid_client_metadata")
			return
		}
	}
	if q.Name == "" {
		q.Name = "Agent"
	}
	client, e := h.s.RegisterClient(r.Context(), q.Name, q.Redirects)
	if e != nil {
		oauthFailure(w, 400, "invalid_client_metadata")
		return
	}
	oauthJSON(w, 201, client)
}

type bookChoice struct{ ID, Name string }
type connectionChoice struct{ ID, Name string }

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect an agent · Vult</title><link rel="stylesheet" href="/auth/style.css"></head><body><main><h1>Connect {{.Client}}</h1><p>Choose one Book and the access this app may use. Write access allows journal posting and account changes. It does not allow owner administration.</p><p>Callback: {{.Redirect}}</p><form method="post" action="/oauth/authorize"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="request" value="{{.Request}}"><label>Book<select name="book">{{range .Books}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><label>Access<select name="access"><option value="read">Read</option>{{if .Write}}<option value="write">Write</option>{{end}}</select></label><label>Connection<select name="connection"><option value="">New connection, or reuse this app's connection</option>{{range .Connections}}<option value="{{.ID}}">Reconnect {{.Name}}</option>{{end}}</select></label><p>Reconnect only an app you recognize. The selected connection must belong to this Book. Its recorded actions remain attributed to the same actor.</p><button name="decision" value="allow" type="submit">Connect</button><button name="decision" value="deny" type="submit">Cancel</button></form></main></body></html>`))

func (h *OAuthHTTP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !single(q) {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	request := q.Get("request")
	if request == "" {
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || (q.Get("scope") != "ledger:read" && q.Get("scope") != "ledger:write") {
			oauthFailure(w, 400, "invalid_request")
			return
		}
		var e error
		request, e = h.s.StartAuthorization(r.Context(), Authorization{q.Get("client_id"), q.Get("redirect_uri"), q.Get("resource"), q.Get("code_challenge"), q.Get("state"), strings.TrimPrefix(q.Get("scope"), "ledger:")})
		if e != nil {
			oauthFailure(w, 400, "invalid_request")
			return
		}
		cookie(w, pendingCookie, request, 300)
		http.Redirect(w, r, "/oauth/authorize?request="+request, 303)
		return
	}
	pending, e := r.Cookie(pendingCookie)
	if e != nil || !validToken(request) || subtle.ConstantTimeCompare([]byte(request), []byte(pending.Value)) != 1 {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	info, e := h.s.PendingAuthorization(r.Context(), request)
	if e != nil {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	session, e := r.Cookie(SessionCookie)
	if e != nil {
		http.Redirect(w, r, "/auth/login", 303)
		return
	}
	_, e = h.s.Identity(r.Context(), session.Value)
	if e != nil {
		http.Redirect(w, r, "/auth/login", 303)
		return
	}
	books := []bookChoice{}
	connections := []connectionChoice{}
	rows, e := h.s.auth.Query(r.Context(), "SELECT tenant_id::text,book_id::text,name FROM access.consent_books($1,$2)", Hash(session.Value), h.s.origin)
	if e != nil {
		oauthFailure(w, 503, "temporarily_unavailable")
		return
	}
	for rows.Next() {
		var tenant, id, name string
		if rows.Scan(&tenant, &id, &name) != nil {
			rows.Close()
			oauthFailure(w, 503, "temporarily_unavailable")
			return
		}
		books = append(books, bookChoice{tenant + "/" + id, name})
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(books) > 100 {
		oauthFailure(w, 503, "temporarily_unavailable")
		return
	}
	rows, e = h.s.auth.Query(r.Context(), "SELECT connection_id::text,name FROM access.consent_connections($1,$2)", Hash(session.Value), h.s.origin)
	if e != nil {
		oauthFailure(w, 503, "temporarily_unavailable")
		return
	}
	for rows.Next() {
		var choice connectionChoice
		if rows.Scan(&choice.ID, &choice.Name) != nil {
			rows.Close()
			oauthFailure(w, 503, "temporarily_unavailable")
			return
		}
		connections = append(connections, choice)
	}
	e = rows.Err()
	rows.Close()
	if e != nil || len(connections) > 100 {
		oauthFailure(w, 503, "temporarily_unavailable")
		return
	}
	if len(books) == 0 {
		oauthFailure(w, 403, "access_denied")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	_ = consentPage.Execute(w, struct {
		Client, Redirect, CSRF, Request string
		Write                           bool
		Books                           []bookChoice
		Connections                     []connectionChoice
	}{info.ClientName, info.RedirectURI, csrf(session.Value), request, info.Access == "write", books, connections})
}

func (h *OAuthHTTP) consent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil || !single(r.PostForm) {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	session, e := r.Cookie(SessionCookie)
	pending, pendingError := r.Cookie(pendingCookie)
	if e != nil || pendingError != nil || !validToken(pending.Value) || pending.Value != r.PostForm.Get("request") || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.s.origin || subtle.ConstantTimeCompare([]byte(csrf(session.Value)), []byte(r.PostForm.Get("csrf"))) != 1 {
		oauthFailure(w, 403, "access_denied")
		return
	}
	decision := r.PostForm.Get("decision")
	if decision != "allow" && decision != "deny" {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	book := strings.Split(r.PostForm.Get("book"), "/")
	if len(book) != 2 || !ledger.ValidID(book[0]) || !ledger.ValidID(book[1]) {
		oauthFailure(w, 400, "invalid_request")
		return
	}
	destination, e := h.s.ConsentAuthorization(r.Context(), session.Value, pending.Value, book[0], book[1], r.PostForm.Get("access"), r.PostForm.Get("connection"), decision == "deny")
	if e != nil {
		oauthFailure(w, 403, "access_denied")
		return
	}
	cookie(w, pendingCookie, "", -1)
	http.Redirect(w, r, destination, 303)
}

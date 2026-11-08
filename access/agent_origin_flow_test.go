package access_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/server"
)

const (
	browserHost = "ledger.example.com"
	agentHost   = "mcp.ledger.example.com"
)

func (f *fixture) agentNetwork(t *testing.T) http.Handler {
	t.Helper()
	if e := f.service.SetAgentOrigin("https://" + agentHost); e != nil {
		t.Fatal(e)
	}
	if e := f.service.SetSignInPath("/login"); e != nil {
		t.Fatal(e)
	}
	login, e := access.NewHTTP(f.service, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if e != nil {
		t.Fatal(e)
	}
	s, e := server.New(server.Config{Origin: "https://" + browserHost, MCPOrigin: "https://" + agentHost, SignInPath: "/login", Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1/32"}}, server.Options{Database: f.db.Runtime, Access: &server.Access{Sessions: f.service, HTTP: login}})
	if e != nil {
		t.Fatal(e)
	}
	return s.Handler
}

func hostRequest(ctx context.Context, h http.Handler, host, method, path, contentType, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body)).WithContext(ctx)
	r.URL.Scheme, r.URL.Host = "", ""
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	if host == browserHost {
		r.Header.Set("Origin", "https://"+browserHost)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type agentTransport struct {
	handler http.Handler
	token   string
}

func (t agentTransport) RoundTrip(in *http.Request) (*http.Response, error) {
	r := in.Clone(in.Context())
	u := *in.URL
	u.Scheme, u.Host = "", ""
	if u.Path == "" {
		u.Path = "/" // HTTP always sends at least "/" for an origin URL.
	}
	r.URL, r.Host, r.RemoteAddr = &u, in.URL.Host, "127.0.0.1:50000"
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	r.Header.Set("Origin", "https://agent.example.com")
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestAgentOriginCarriesTheWholeConnection(t *testing.T) {
	f := fresh(t)
	h := f.agentNetwork(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)

	w := hostRequest(ctx, h, agentHost, "POST", "/", "application/json", `{}`)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `resource_metadata="https://`+agentHost+`/.well-known/oauth-protected-resource"`) {
		t.Fatal("unauthenticated MCP did not point to the agent origin", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	var resource struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	w = hostRequest(ctx, h, agentHost, "GET", "/.well-known/oauth-protected-resource", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resource) != nil || resource.Resource != "https://"+agentHost || len(resource.Servers) != 1 || resource.Servers[0] != "https://"+agentHost {
		t.Fatal("protected resource metadata did not name the agent origin", w.Body.String())
	}
	var metadata map[string]any
	w = hostRequest(ctx, h, agentHost, "GET", "/.well-known/oauth-authorization-server", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &metadata) != nil || metadata["issuer"] != "https://"+agentHost || metadata["authorization_endpoint"] != "https://"+browserHost+"/oauth/authorize" || metadata["token_endpoint"] != "https://"+agentHost+"/oauth/token" || metadata["registration_endpoint"] != "https://"+agentHost+"/oauth/register" {
		t.Fatal("authorization server metadata was not split correctly", w.Body.String())
	}

	w = hostRequest(ctx, h, agentHost, "POST", "/oauth/register", "application/json", `{"client_name":"Example Agent","redirect_uris":["http://localhost:43123/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`)
	var client access.Client
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &client) != nil || client.ID == "" {
		t.Fatal("registration at the agent origin failed", w.Code, w.Body.String())
	}

	verifier := access.Random()
	q := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {client.Redirects[0]}, "resource": {"https://" + agentHost}, "code_challenge": {access.Challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {"ledger:read"}, "state": {"agent-state"}}
	if w = hostRequest(ctx, h, agentHost, "GET", "/oauth/authorize?"+q.Encode(), "", ""); w.Code != 404 {
		t.Fatal("agent origin served the browser consent page", w.Code)
	}
	w = hostRequest(ctx, h, browserHost, "GET", "/oauth/authorize?"+q.Encode(), "", "")
	if w.Code != http.StatusSeeOther {
		t.Fatal("authorization start failed", w.Body.String())
	}
	pending := responseCookie(t, w, access.PendingCookie)
	if w = hostRequest(ctx, h, browserHost, "GET", "/oauth/authorize?request="+pending.Value, "", "", pending); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatal("signed-out owner was not sent to the deployment sign-in page", w.Code, w.Header().Get("Location"))
	}
	if resume, ok := f.service.ResumeAuthorization(ctx, pending.Value); !ok || resume != "/oauth/authorize?request="+pending.Value {
		t.Fatal("pending authorization could not be resumed after sign-in")
	}
	if _, ok := f.service.ResumeAuthorization(ctx, access.Random()); ok {
		t.Fatal("unknown authorization resumed")
	}
	sessionCookie := &http.Cookie{Name: access.SessionCookie, Value: session}
	csrfResponse := hostRequest(ctx, h, browserHost, "GET", "/auth/session", "", "", sessionCookie)
	var proof struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(csrfResponse.Body.Bytes(), &proof) != nil || proof.CSRF == "" {
		t.Fatal("session CSRF missing")
	}
	form := url.Values{"request": {pending.Value}, "csrf": {proof.CSRF}, "book": {tenant + "/" + book.ID}, "access": {"read"}, "decision": {"allow"}}
	w = hostRequest(ctx, h, browserHost, "POST", "/oauth/authorize", "application/x-www-form-urlencoded", form.Encode(), pending, sessionCookie)
	if w.Code != http.StatusSeeOther {
		t.Fatal("consent failed", w.Body.String())
	}
	destination, _ := url.Parse(w.Header().Get("Location"))
	if destination.Query().Get("iss") != "https://"+agentHost || destination.Query().Get("state") != "agent-state" {
		t.Fatal("authorization response named the wrong issuer", destination.String())
	}

	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {destination.Query().Get("code")}, "client_id": {client.ID}, "resource": {"https://" + agentHost}, "redirect_uri": {client.Redirects[0]}, "code_verifier": {verifier}}
	if w = hostRequest(ctx, h, browserHost, "POST", "/oauth/token", "application/x-www-form-urlencoded", tokenForm.Encode()); w.Code != 404 {
		t.Fatal("browser origin still exchanged tokens", w.Code)
	}
	w = hostRequest(ctx, h, agentHost, "POST", "/oauth/token", "application/x-www-form-urlencoded", tokenForm.Encode())
	var tokens access.Tokens
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil || tokens.Access == "" || tokens.Scope != "ledger:read" {
		t.Fatal("token exchange at the agent origin failed", w.Body.String())
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.Refresh}, "client_id": {client.ID}, "resource": {"https://" + agentHost}}
	w = hostRequest(ctx, h, agentHost, "POST", "/oauth/token", "application/x-www-form-urlencoded", refresh.Encode())
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil || tokens.Access == "" {
		t.Fatal("refresh at the agent origin failed", w.Body.String())
	}

	if w = hostRequest(ctx, h, browserHost, "POST", "/mcp", "application/json", `{}`); w.Code != 404 {
		t.Fatal("browser origin still served MCP", w.Code)
	}
	m := mcp.NewClient(&mcp.Implementation{Name: "example-agent", Version: "1"}, nil)
	conn, e := m.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "https://" + agentHost, HTTPClient: &http.Client{Transport: agentTransport{handler: h, token: tokens.Access}}}, nil)
	if e != nil {
		t.Fatal("MCP at the agent origin root failed", e)
	}
	t.Cleanup(func() { conn.Close() })
	catalog, e := conn.ListTools(ctx, nil)
	if e != nil || len(catalog.Tools) == 0 {
		t.Fatal("MCP tool catalog unavailable", e)
	}
	result, e := conn.CallTool(ctx, &mcp.CallToolParams{Name: "get_book", Arguments: map[string]any{}})
	if e != nil || result.IsError {
		t.Fatal("read tool failed at the agent origin", e, result)
	}
}

package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func agentConfiguration() Config {
	c := configuration()
	c.MCPOrigin = "https://mcp.ledger.example.com"
	return c
}

func onHost(host, method, path string) *http.Request {
	r := request()
	r.Method, r.URL.Path, r.Host = method, path, host
	r.Header.Set("X-Forwarded-Host", host)
	return r
}

func TestMCPOriginRejectsAmbiguousConfiguration(t *testing.T) {
	for _, origin := range []string{"http://mcp.ledger.example.com", "https://ledger.example.com", "https://mcp.ledger.example.com/", "https://mcp.ledger.example.com/mcp", "https://MCP.ledger.example.com", "https://127.0.0.1", "off"} {
		c := agentConfiguration()
		c.MCPOrigin = origin
		if _, e := New(c, Options{}); !errors.Is(e, ErrConfiguration) {
			t.Fatal("ambiguous MCP origin accepted", origin)
		}
	}
}

func TestMCPOriginServesAgentRoutesOnlyOnItsOwnHost(t *testing.T) {
	s, e := New(agentConfiguration(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		return w
	}
	preflight := onHost("mcp.ledger.example.com", "OPTIONS", "/")
	preflight.Header.Set("Origin", "https://agent.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	if w := serve(preflight); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("MCP preflight at the agent origin root failed", w.Code)
	}
	for _, path := range []string{"/healthz", "/mcp", "/oauth/authorize", "/api/v1/books", "/auth/login", "/owner/books"} {
		if w := serve(onHost("mcp.ledger.example.com", "GET", path)); w.Code != http.StatusNotFound {
			t.Fatal("agent origin served a browser route", path, w.Code)
		}
	}
	for _, r := range []*http.Request{
		onHost("ledger.example.com", "POST", "/mcp"),
		onHost("ledger.example.com", "POST", "/oauth/register"),
		onHost("ledger.example.com", "POST", "/oauth/token"),
		onHost("ledger.example.com", "GET", "/.well-known/oauth-authorization-server"),
		onHost("ledger.example.com", "GET", "/.well-known/oauth-protected-resource"),
		onHost("ledger.example.com", "GET", "/.well-known/oauth-protected-resource/mcp"),
	} {
		if w := serve(r); w.Code != http.StatusNotFound {
			t.Fatal("browser origin still served an agent route", r.URL.Path, w.Code)
		}
	}
	if w := serve(onHost("ledger.example.com", "GET", "/healthz")); w.Code != http.StatusNoContent {
		t.Fatal("browser origin liveness failed", w.Code)
	}
	crossed := onHost("mcp.ledger.example.com", "POST", "/")
	crossed.Header.Set("X-Forwarded-Host", "ledger.example.com")
	if w := serve(crossed); w.Code != http.StatusForbidden {
		t.Fatal("mismatched forwarded host accepted", w.Code)
	}
	browser := onHost("mcp.ledger.example.com", "POST", "/oauth/token")
	browser.AddCookie(&http.Cookie{Name: "__Host-vult-session", Value: "example"})
	browser.Header.Set("Origin", "https://agent.example.com")
	if w := serve(browser); w.Code != http.StatusForbidden {
		t.Fatal("agent origin accepted ambient browser credentials", w.Code)
	}
}

package access

import "testing"

func TestAgentOriginBecomesIssuerAndResource(t *testing.T) {
	s := &Service{origin: "https://ledger.example.com"}
	if s.Issuer() != "https://ledger.example.com" || s.Resource() != "https://ledger.example.com/mcp" || s.realmResource() != "https://ledger.example.com/mcp" {
		t.Fatal("default MCP location changed")
	}
	for _, bad := range []string{"", "http://mcp.ledger.example.com", "https://ledger.example.com", "https://mcp.ledger.example.com/", "https://mcp.ledger.example.com/mcp", "https://user@mcp.ledger.example.com"} {
		if s.SetAgentOrigin(bad) == nil {
			t.Fatal("ambiguous agent origin accepted", bad)
		}
	}
	if e := s.SetAgentOrigin("https://mcp.ledger.example.com"); e != nil {
		t.Fatal(e)
	}
	if s.Issuer() != "https://mcp.ledger.example.com" || s.Resource() != "https://mcp.ledger.example.com" || s.AuthorizationEndpoint() != "https://ledger.example.com/oauth/authorize" {
		t.Fatal("agent origin was not published")
	}
	if s.realmResource() != "https://ledger.example.com/mcp" {
		t.Fatal("stored grant binding moved with the public resource")
	}
	if s.SetAgentOrigin("https://agents.ledger.example.com") == nil {
		t.Fatal("agent origin changed after it was set")
	}
}

func TestNativeAgentCallbacksAcceptLocalhost(t *testing.T) {
	for _, ok := range []string{"http://localhost:53682/callback", "http://127.0.0.1:53682/callback", "http://[::1]:53682/callback", "https://agent.example.com/callback"} {
		if !validRedirect(ok) {
			t.Fatal("native agent callback refused", ok)
		}
	}
	for _, bad := range []string{"http://localhost/callback", "http://LOCALHOST:53682/callback", "http://localhost.example.com:53682/callback", "http://sub.localhost:53682/callback", "http://user@localhost:53682/callback", "http://localhost:53682/callback#x", "http://192.0.2.10:53682/callback", "http://agent.example.com:53682/callback"} {
		if validRedirect(bad) {
			t.Fatal("unsafe callback accepted", bad)
		}
	}
}

func TestSignInPathIsSameOriginAndSetOnce(t *testing.T) {
	s := &Service{origin: "https://ledger.example.com"}
	if s.SignInPath() != "/auth/login" {
		t.Fatal("default sign-in page changed")
	}
	for _, bad := range []string{"", "login", "//evil.example.com/login", "https://evil.example.com/login", "/login?next=x", "/login#x", "/log in", "/\\evil"} {
		if s.SetSignInPath(bad) == nil {
			t.Fatal("unsafe sign-in path accepted", bad)
		}
	}
	if s.SetSignInPath("/login") != nil || s.SignInPath() != "/login" {
		t.Fatal("deployment sign-in page refused")
	}
	if s.SetSignInPath("/other") == nil {
		t.Fatal("sign-in page changed after it was set")
	}
}

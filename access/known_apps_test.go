package access

import (
	"strings"
	"testing"
)

func TestConsentNamesOnlyVerifiedApps(t *testing.T) {
	claude := identifyApp("Anything At All", "https://claude.ai/api/mcp/auth_callback")
	if !claude.Verified || claude.Name != "Claude" || claude.Destination != "claude.ai" {
		t.Fatal("published Claude callback was not verified", claude)
	}
	for _, lookalike := range []string{"https://claude.ai/api/mcp/auth_callback/", "https://claude.ai/api/mcp/auth_callback?x=1", "https://claude.ai.example.com/api/mcp/auth_callback", "https://CLAUDE.ai/api/mcp/auth_callback", "https://claude.ai/other"} {
		if a := identifyApp("Claude", lookalike); a.Verified {
			t.Fatal("lookalike callback verified", lookalike)
		}
	}
	local := identifyApp("Claude Code (vult)", "http://localhost:53682/callback")
	if local.Verified || !local.Local || local.Name != "Claude Code (vult)" {
		t.Fatal("local app misdescribed", local)
	}
	remote := identifyApp("Claude", "https://agent.example.com/callback")
	if remote.Verified || remote.Local || remote.Destination != "agent.example.com" {
		t.Fatal("unverified hosted app misdescribed", remote)
	}
}

func TestConsentPageStatesVerification(t *testing.T) {
	render := func(app appIdentity) string {
		var b strings.Builder
		data := struct {
			App           appIdentity
			CSRF, Request string
			Write         bool
			Books         []bookChoice
			Connections   []connectionChoice
		}{app, "csrf", "request", true, []bookChoice{{"t/b", "Example Book"}}, nil}
		if e := consentPage.Execute(&b, data); e != nil {
			t.Fatal(e)
		}
		return b.String()
	}
	if page := render(identifyApp("x", "https://claude.ai/api/mcp/auth_callback")); !strings.Contains(page, "<h1>Connect Claude</h1>") || !strings.Contains(page, "Verified app.") {
		t.Fatal("verified app not stated")
	}
	page := render(identifyApp(`Claude <script>`, "https://agent.example.com/callback"))
	if !strings.Contains(page, "<h1>Connect an unverified app</h1>") || !strings.Contains(page, "Unverified app.") || strings.Contains(page, "<script>") || !strings.Contains(page, "agent.example.com") {
		t.Fatal("unverified app not stated safely")
	}
	if page := render(identifyApp("Claude Code", "http://127.0.0.1:53682/callback")); !strings.Contains(page, "App on this computer.") {
		t.Fatal("local app not stated")
	}
}

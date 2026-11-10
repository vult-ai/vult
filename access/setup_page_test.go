package access

import (
	"strings"
	"testing"
)

// On this Mac, setup opens on the passkey dial, with any message right under
// the button and no setup code to type.
func TestLocalSetupPageOpensOnThePasskeyDial(t *testing.T) {
	var page strings.Builder
	if err := passkeySetupPage.Execute(&page, struct {
		CSRF          string
		Local         bool
		Authenticator bool
	}{"example", true, true}); err != nil {
		t.Fatal(err)
	}
	html := page.String()
	order := []string{`id="passkey-dial"`, "<h1 id=\"setup-title\">Secure Vult</h1>", `id="passkey-register"`, `id="passkey-status"`, `id="passkey-fine"`, `id="authenticator-switch"`, `id="setup-help-toggle"`}
	at := 0
	for _, part := range order {
		next := strings.Index(html[at:], part)
		if next < 0 {
			t.Fatal("the local setup page is missing or misorders", part)
		}
		at += next
	}
	threshold := strings.Index(html, `id="passkey-threshold"`)
	end := threshold + strings.Index(html[threshold:], "</section>")
	authenticator := strings.Index(html, `<section id="authenticator-setup" class="enroll"`)
	if threshold < 0 || authenticator < end || !strings.Contains(html, "<h1 id=\"authenticator-title\">Set up an authenticator</h1>") || !strings.Contains(html, `id="authenticator-back"`) {
		t.Fatal("the authenticator does not get its own screen after the passkey")
	}
	if !strings.Contains(html, `<label id="passkey-setup-label" hidden>`) || strings.Contains(html, "Set up sign-in</h1>") {
		t.Fatal("the local setup page offers a setup code")
	}
}

package access

import (
	"html/template"
	"strings"
	"testing"
)

// The local recovery codes page renders for each first method: the codes are
// kept before setup continues, and a summary follows with a backup offer only
// where one can be added.
func TestRecoveryCodesPageKeepsCodesThenSummarizes(t *testing.T) {
	for _, tc := range []struct {
		lost                      string
		passkey, authenticator    bool
		offer                     bool
		wantLede, wantOffer, deny string
	}{
		{"the passkey", true, false, true, "You sign in with a passkey.", "Add an authenticator app as a backup", ""},
		{"the authenticator", false, true, false, "You sign in with an authenticator app.", "", "Add an authenticator app as a backup"},
		{"the sign-in methods", true, true, false, "You sign in with a passkey or authenticator app.", "", "Add an authenticator app as a backup"},
	} {
		var page strings.Builder
		err := recoveryPage.Execute(&page, struct {
			CSRF                                             string
			Codes                                            []string
			BackupKey                                        string
			Download                                         template.URL
			Lost                                             string
			HasPasskey, HasAuthenticator, OfferAuthenticator bool
		}{"example", []string{"EXAMPLE-ONE"}, "", "data:text/plain,example", tc.lost, tc.passkey, tc.authenticator, tc.offer})
		if err != nil {
			t.Fatal(err)
		}
		html := page.String()
		for _, want := range []string{
			"<h1 id=\"codes-title\">Save recovery codes</h1>",
			"If you ever lose " + tc.lost + ", these codes are the only way back in.",
			"Never share them. Vult will never ask for them.",
			`<button id="recovery-continue" type="button" disabled`,
			"I wrote them down",
			"<h1 id=\"protected-title\">This account is protected</h1>",
			tc.wantLede,
		} {
			if !strings.Contains(html, want) {
				t.Fatal(tc.lost, "page is missing", want)
			}
		}
		if tc.wantOffer != "" && !strings.Contains(html, tc.wantOffer) || tc.deny != "" && strings.Contains(html, tc.deny) {
			t.Fatal(tc.lost, "offers the wrong backup")
		}
		if strings.Contains(strings.ToLower(html), "refund") || strings.Contains(html, "agent") {
			t.Fatal("recovery copy must not mention refunds or agents")
		}
	}
}

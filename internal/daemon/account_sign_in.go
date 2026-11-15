package daemon

import (
	"net/url"
	"os"
)

// accountSignInFromEnvironment is where a person signs in to a Vult account
// to connect Vult's agent to this Vult, when VULT_ACCOUNT_SIGN_IN_URL names
// an HTTPS page. With none set the page offers no sign-in.
func accountSignInFromEnvironment() string {
	raw := os.Getenv("VULT_ACCOUNT_SIGN_IN_URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > 512 {
		return ""
	}
	return raw
}

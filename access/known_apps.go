package access

import (
	"net/url"
	"strings"
)

// verifiedCallbacks maps exact OAuth callbacks that a vendor publishes for its
// hosted app to that app's name. Only an HTTPS callback the vendor controls can
// prove which app receives the code; a registered client name proves nothing.
var verifiedCallbacks = map[string]string{
	"https://claude.ai/api/mcp/auth_callback": "Claude",
	// OpenAI's documented stable redirect for ChatGPT connectors.
	"https://chatgpt.com/connector_platform_oauth_redirect": "ChatGPT",
}

// verifiedCallbackPrefixes are callback paths a vendor documents with a
// per-connection part, such as ChatGPT's connector/oauth/{callback_id}.
// The prefix ends at a path separator, on the vendor's own HTTPS host.
var verifiedCallbackPrefixes = map[string]string{
	"https://chatgpt.com/connector/oauth/": "ChatGPT",
}

// appIdentity is what the consent screen may truthfully say about an app.
type appIdentity struct {
	Name        string // verified name, or the name the app gave itself
	Verified    bool   // the callback belongs to a known app
	Local       bool   // the callback is on this computer
	Destination string // where the authorization code is sent
}

func identifyApp(selfName, redirect string) appIdentity {
	u, e := url.Parse(redirect)
	if e != nil {
		return appIdentity{Name: selfName}
	}
	if name, ok := verifiedCallbacks[redirect]; ok {
		return appIdentity{Name: name, Verified: true, Destination: u.Host}
	}
	for prefix, name := range verifiedCallbackPrefixes {
		rest, ok := strings.CutPrefix(redirect, prefix)
		if ok && u.Scheme == "https" && u.RawQuery == "" && u.Fragment == "" && rest != "" && !strings.ContainsAny(rest, "/?#%\\.") {
			return appIdentity{Name: name, Verified: true, Destination: u.Host}
		}
	}
	if u.Scheme == "http" {
		return appIdentity{Name: selfName, Local: true, Destination: "this computer"}
	}
	return appIdentity{Name: selfName, Destination: u.Host}
}

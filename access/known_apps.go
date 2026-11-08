package access

import "net/url"

// verifiedCallbacks maps exact OAuth callbacks that a vendor publishes for its
// hosted app to that app's name. Only an HTTPS callback the vendor controls can
// prove which app receives the code; a registered client name proves nothing.
var verifiedCallbacks = map[string]string{
	"https://claude.ai/api/mcp/auth_callback": "Claude",
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
	if u.Scheme == "http" {
		return appIdentity{Name: selfName, Local: true, Destination: "this computer"}
	}
	return appIdentity{Name: selfName, Destination: u.Host}
}

package server

import (
	"net/url"
	"strings"
)

// BankScreen is a bank connection provider whose own connection screen the
// browser opens. Whoever builds this server with such a provider supplies it;
// without one, Books bring activity in by upload and through agents.
type BankScreen struct {
	// Provider is the connector's key in Options.Banks.Connectors.
	Provider string
	// Name is how the owner sees the provider, if at all. Optional.
	Name string
	// Script is the same-origin path of a script the browser loads to open
	// the screen, such as "/bank-screen.js". The embedder serves it.
	Script string
	// ScriptSources, FrameSources and ConnectSources are the extra https
	// origins the screen needs in the page's content security policy.
	ScriptSources, FrameSources, ConnectSources []string
}

// validate checks a screen against the connectors this server has: its
// provider must be one of them when banks are on, its script a path on this
// origin, and every source a plain https origin.
func (b *BankScreen) validate(connectors map[string]bool, banks bool) error {
	if b == nil {
		return nil
	}
	if b.Provider == "" || banks && !connectors[b.Provider] || !sameOriginPath(b.Script) {
		return ErrConfiguration
	}
	for _, list := range [][]string{b.ScriptSources, b.FrameSources, b.ConnectSources} {
		for _, source := range list {
			if !httpsOrigin(source) {
				return ErrConfiguration
			}
		}
	}
	return nil
}

// sameOriginPath reports whether p is an absolute path on this origin: it
// starts with one slash and names no scheme, host, query or fragment.
func sameOriginPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\?#;'\" \t\r\n") {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil && u.Path == p
}

// httpsOrigin reports whether s is exactly an https origin, such as
// "https://cdn.example.com": no path, query, fragment, credentials or
// wildcard, and nothing that could end a policy directive.
func httpsOrigin(s string) bool {
	if strings.ContainsAny(s, "*;,'\" \t\r\n\\") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return "https://"+u.Host == s
}

// Package server provides the network boundary for the ledger service.
package server

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrConfiguration = errors.New("invalid server configuration")
	ErrDatabase      = errors.New("restricted ledger database unavailable")
	ErrUnavailable   = errors.New("server unavailable")
)

type Config struct {
	Origin         string          `json:"origin"`
	Listen         string          `json:"listen"`
	TrustedProxies []string        `json:"trusted_proxies"`
	PrivateIngress bool            `json:"private_ingress"`
	Authentication *Authentication `json:"authentication,omitempty"`
}

type ingress struct {
	origin, host string
	proxies      []netip.Prefix
}

func (c Config) validate() (ingress, error) {
	u, e := url.Parse(c.Origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host != strings.ToLower(u.Host) || strings.HasSuffix(u.Hostname(), ".") || strings.ContainsAny(u.Host, "\\ \t\r\n") {
		return ingress{}, ErrConfiguration
	}
	if p := u.Port(); p != "" && !validPort(p) {
		return ingress{}, ErrConfiguration
	}
	if strings.HasSuffix(u.Host, ":") || !validHostname(u.Hostname()) {
		return ingress{}, ErrConfiguration
	}
	address, port, e := net.SplitHostPort(c.Listen)
	ip, parse := netip.ParseAddr(address)
	if e != nil || parse != nil || !validPort(port) || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() && c.PrivateIngress) {
		return ingress{}, ErrConfiguration
	}
	if len(c.TrustedProxies) == 0 || len(c.TrustedProxies) > 32 {
		return ingress{}, ErrConfiguration
	}
	g := ingress{origin: c.Origin, host: u.Host}
	for _, raw := range c.TrustedProxies {
		p, e := netip.ParsePrefix(raw)
		if e != nil || p != p.Masked() || !privatePrefix(p) {
			return ingress{}, ErrConfiguration
		}
		g.proxies = append(g.proxies, p)
	}
	return g, nil
}

func validPort(raw string) bool {
	p, e := strconv.Atoi(raw)
	return e == nil && p > 0 && p <= 65535 && strconv.Itoa(p) == raw
}

func validHostname(host string) bool {
	if ip, e := netip.ParseAddr(host); e == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsLoopback()
	}
	if host == "localhost" || len(host) > 253 || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func privatePrefix(p netip.Prefix) bool {
	for _, network := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fc00::/7"} {
		allowed := netip.MustParsePrefix(network)
		if p.Addr().Is4() == allowed.Addr().Is4() && p.Bits() >= allowed.Bits() && allowed.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

func one(h http.Header, key string) string {
	v := h.Values(key)
	if len(v) != 1 {
		return ""
	}
	return v[0]
}

func (g ingress) check(r *http.Request) bool {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	peer, parse := netip.ParseAddr(host)
	if e != nil || parse != nil || r.Host != g.host || r.URL.IsAbs() || r.URL.Opaque != "" || r.URL.RawPath != "" {
		return false
	}
	trusted := false
	for _, p := range g.proxies {
		trusted = trusted || p.Contains(peer.Unmap())
	}
	if !trusted || one(r.Header, "X-Forwarded-Proto") != "https" || one(r.Header, "X-Forwarded-Host") != g.host {
		return false
	}
	client, e := netip.ParseAddr(one(r.Header, "X-Forwarded-For"))
	if e != nil || client.Zone() != "" || !client.IsGlobalUnicast() && !client.IsLoopback() {
		return false
	}
	for key := range r.Header {
		lower := strings.ToLower(key)
		if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") && lower != "x-forwarded-proto" && lower != "x-forwarded-host" && lower != "x-forwarded-for" {
			return false
		}
	}
	if values := r.Header.Values("Origin"); len(values) > 0 && (len(values) != 1 || values[0] != g.origin && !credentialRoute(r)) {
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && len(r.Header.Values("Cookie")) > 0 && one(r.Header, "Origin") != g.origin {
		return false
	}
	return credentialRoute(r) || http.NewCrossOriginProtection().Check(r) == nil
}

// These routes never accept ambient browser credentials. Explicit bearer/PKCE
// requests may originate in agent applications on a different HTTPS origin.
func credentialRoute(r *http.Request) bool {
	if len(r.Header.Values("Cookie")) != 0 {
		return false
	}
	switch r.URL.Path {
	case "/mcp", "/oauth/register", "/oauth/token", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-authorization-server":
		return true
	}
	return false
}

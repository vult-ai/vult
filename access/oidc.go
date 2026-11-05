package access

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDC struct {
	sessions *Service
	issuer   string
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

type boundedTransport struct{ base http.RoundTripper }
type boundedBody struct {
	io.Reader
	io.Closer
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, ErrDenied
	}
	response, e := t.base.RoundTrip(r)
	if e == nil {
		response.Body = boundedBody{io.LimitReader(response.Body, 1<<20), response.Body}
	}
	return response, e
}

// NewOIDC requires an explicit operator-verified MFA policy. The default client
// verifies HTTPS, limits provider responses and never follows credential redirects.
// A supplied client may provide an independently trusted transport or fixture CA.
func NewOIDC(ctx context.Context, s *Service, issuer, clientID, secret string, mfaPolicyVerified bool, client *http.Client) (*OIDC, error) {
	if s == nil || !mfaPolicyVerified || clientID == "" || secret == "" || !validIdentity(Identity{Issuer: issuer, Subject: "probe"}) || issuer == BuiltinIssuer {
		return nil, ErrDenied
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: boundedTransport{transport}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	provider, e := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if e != nil {
		return nil, ErrDenied
	}
	endpoints := provider.Endpoint()
	var metadata struct {
		Keys string `json:"jwks_uri"`
	}
	if provider.Claims(&metadata) != nil {
		return nil, ErrDenied
	}
	for _, raw := range []string{endpoints.AuthURL, endpoints.TokenURL, metadata.Keys} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, ErrDenied
		}
	}
	return &OIDC{sessions: s, issuer: issuer, client: client,
		oauth:    oauth2.Config{ClientID: clientID, ClientSecret: secret, Endpoint: endpoints, RedirectURL: s.origin + "/auth/callback", Scopes: []string{oidc.ScopeOpenID}},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256, oidc.PS256}})}, nil
}

func (o *OIDC) Start(ctx context.Context) (destination, cookie string, err error) {
	state, nonce, cookie, verifier := Random(), Random(), Random(), oauth2.GenerateVerifier()
	_, e := o.sessions.auth.Exec(ctx, "SELECT access.start_login($1,$2,$3,$4,$5,$6)", Hash(cookie), Hash(state), nonce, verifier, o.issuer, o.sessions.origin)
	if e != nil {
		return "", "", ErrDenied
	}
	destination = o.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce), oauth2.SetAuthURLParam("max_age", "0"), oauth2.SetAuthURLParam("prompt", "login"))
	return destination, cookie, nil
}

func (o *OIDC) Callback(ctx context.Context, cookie, state, code string) (string, error) {
	if !validToken(cookie) || !validToken(state) || len(code) < 1 || len(code) > 4096 {
		return "", ErrDenied
	}
	var nonce, verifier string
	e := o.sessions.auth.QueryRow(ctx, "SELECT nonce,verifier FROM access.consume_login($1,$2,$3,$4)", Hash(cookie), Hash(state), o.issuer, o.sessions.origin).Scan(&nonce, &verifier)
	if e != nil {
		return "", ErrDenied
	}
	providerContext := oidc.ClientContext(ctx, o.client)
	token, e := o.oauth.Exchange(providerContext, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return "", ErrDenied
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || len(raw) > 128<<10 {
		return "", ErrDenied
	}
	id, e := o.verifier.Verify(providerContext, raw)
	if e != nil || id.Issuer != o.issuer || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 {
		return "", ErrDenied
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
		AuthenticatedAt int64  `json:"auth_time"`
	}
	if id.Claims(&claims) != nil || ((len(id.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != o.oauth.ClientID) || claims.AuthenticatedAt <= 0 {
		return "", ErrDenied
	}
	identity := Identity{Issuer: id.Issuer, Subject: id.Subject, AuthenticatedAt: time.Unix(claims.AuthenticatedAt, 0)}
	if !validIdentity(identity) {
		return "", ErrDenied
	}
	return o.sessions.IssueSession(ctx, identity)
}

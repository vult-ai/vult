package access

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

func validRedirect(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip, e := netip.ParseAddr(u.Hostname())
	return u.Scheme == "http" && e == nil && ip.IsLoopback() && ip.Zone() == "" && u.Port() != ""
}
func validProof(raw string) bool {
	if len(raw) < 43 || len(raw) > 128 {
		return false
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func safeLabel(raw string) bool {
	if len(strings.TrimSpace(raw)) < 1 || len(raw) > 80 {
		return false
	}
	for _, c := range raw {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

type Client struct {
	ID         string   `json:"client_id"`
	Name       string   `json:"client_name"`
	Redirects  []string `json:"redirect_uris"`
	AuthMethod string   `json:"token_endpoint_auth_method"`
}

func (s *Service) Resource() string { return s.origin + "/mcp" }
func (s *Service) RegisterClient(ctx context.Context, name string, redirects []string) (Client, error) {
	if !safeLabel(name) || len(redirects) < 1 || len(redirects) > 8 {
		return Client{}, ErrDenied
	}
	seen := map[string]bool{}
	for _, raw := range redirects {
		if !validRedirect(raw) || seen[raw] {
			return Client{}, ErrDenied
		}
		seen[raw] = true
	}
	c := Client{Random(), name, redirects, "none"}
	var registered bool
	if e := s.auth.QueryRow(ctx, "SELECT access.register_client($1,$2,$3)", c.ID, name, redirects).Scan(&registered); e != nil || !registered {
		return Client{}, ErrDenied
	}
	return c, nil
}

type Authorization struct{ ClientID, RedirectURI, Resource, Challenge, State, Access string }

func (s *Service) StartAuthorization(ctx context.Context, q Authorization) (string, error) {
	if !validToken(q.ClientID) || !validRedirect(q.RedirectURI) || q.Resource != s.Resource() || !validToken(q.Challenge) || len(q.State) > 1024 || (q.Access != "read" && q.Access != "write") {
		return "", ErrDenied
	}
	raw := Random()
	if _, e := s.auth.Exec(ctx, "SELECT access.start_oauth($1,$2,$3,$4,$5,$6,$7,$8)", Hash(raw), q.ClientID, q.RedirectURI, q.Resource, q.Challenge, q.State, q.Access, s.origin); e != nil {
		return "", ErrDenied
	}
	return raw, nil
}

type Consent struct{ ClientName, RedirectURI, Access string }

func (s *Service) PendingAuthorization(ctx context.Context, request string) (Consent, error) {
	if !validToken(request) {
		return Consent{}, ErrDenied
	}
	var c Consent
	e := s.auth.QueryRow(ctx, "SELECT client_name,redirect_uri,requested_access FROM access.pending_oauth($1,$2)", Hash(request), s.origin).Scan(&c.ClientName, &c.RedirectURI, &c.Access)
	if e != nil {
		return Consent{}, ErrDenied
	}
	return c, nil
}
func (s *Service) ConsentAuthorization(ctx context.Context, session, request, tenant, book, permission, connection string, deny bool) (string, error) {
	if !validToken(session) || !validToken(request) || !ledger.ValidID(tenant) || !deny && (!ledger.ValidID(book) || (permission != "read" && permission != "write")) || connection != "" && !ledger.ValidID(connection) {
		return "", ErrDenied
	}
	var existing any
	if connection != "" {
		existing = connection
	}
	code := Random()
	var redirect, state string
	e := s.auth.QueryRow(ctx, "SELECT redirect_uri,state FROM access.consent_oauth($1,$2,$3,$4,$5,$6,$7,$8,$9)", Hash(session), s.origin, Hash(request), tenant, nullableID(book), permission, existing, Hash(code), deny).Scan(&redirect, &state)
	if e != nil {
		return "", ErrDenied
	}
	u, e := url.Parse(redirect)
	if e != nil || !validRedirect(redirect) {
		return "", ErrDenied
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("iss", s.origin)
	if deny {
		q.Set("error", "access_denied")
	} else {
		q.Set("code", code)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

type Tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Type    string `json:"token_type"`
	Expires int    `json:"expires_in"`
	Scope   string `json:"scope"`
}

func (s *Service) Exchange(ctx context.Context, v url.Values) (Tokens, error) {
	for _, values := range v {
		if len(values) != 1 {
			return Tokens{}, ErrDenied
		}
	}
	kind := v.Get("grant_type")
	secret := ""
	proof := ""
	switch kind {
	case "authorization_code":
		secret = v.Get("code")
		if !validProof(v.Get("code_verifier")) || !validRedirect(v.Get("redirect_uri")) {
			return Tokens{}, ErrDenied
		}
		proof = Challenge(v.Get("code_verifier"))
	case "refresh_token":
		secret = v.Get("refresh_token")
	default:
		return Tokens{}, ErrDenied
	}
	if !validToken(secret) || !validToken(v.Get("client_id")) || v.Get("resource") != s.Resource() {
		return Tokens{}, ErrDenied
	}
	out := Tokens{Access: Random(), Refresh: Random(), Type: "Bearer", Expires: 900}
	var permission *string
	e := s.auth.QueryRow(ctx, "SELECT access.exchange_oauth($1,$2,$3,$4,$5,$6,$7,$8,$9)", kind, Hash(secret), v.Get("client_id"), v.Get("resource"), v.Get("redirect_uri"), proof, Hash(out.Access), Hash(out.Refresh), s.origin).Scan(&permission)
	if e != nil || permission == nil || (*permission != "read" && *permission != "write") {
		return Tokens{}, ErrDenied
	}
	out.Scope = "ledger:" + *permission
	return out, nil
}
func (s *Service) ResolveAgent(ctx context.Context, token string) (ledger.Scope, error) {
	if !validToken(token) {
		return ledger.Scope{}, ErrDenied
	}
	var scope ledger.Scope
	e := s.auth.QueryRow(ctx, "SELECT tenant_id::text,book_id::text,connection_id::text,actor_id::text,permission FROM access.resolve_agent($1,$2)", Hash(token), s.Resource()).Scan(&scope.TenantID, &scope.BookID, &scope.ConnectionID, &scope.ActorID, &scope.Access)
	if e != nil {
		return ledger.Scope{}, ErrDenied
	}
	return scope, nil
}

// AgentAuthorizer rechecks and leases the credential, identity, membership and
// grant in the ledger transaction. Resolving an HTTP bearer alone is insufficient.
func (s *Service) AgentAuthorizer(token string) func(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error {
	return func(ctx context.Context, tx pgx.Tx, scope ledger.Scope, _ string, _ json.RawMessage) error {
		if !validToken(token) {
			return ErrDenied
		}
		if _, e := tx.Exec(ctx, "SELECT access.require_agent($1,$2,$3,$4,$5,$6,$7)", Hash(token), s.Resource(), scope.TenantID, scope.BookID, scope.ConnectionID, scope.ActorID, scope.Access); e != nil {
			return ErrDenied
		}
		return nil
	}
}

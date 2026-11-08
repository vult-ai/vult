// Package access binds verified identities to ledger ownership and sessions.
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

var ErrDenied = errors.New("authorization unavailable")

const BuiltinIssuer = "urn:vult:builtin"

type Identity struct {
	Issuer, Subject string
	AuthenticatedAt time.Time
}

func validIdentity(id Identity) bool {
	if !utf8.ValidString(id.Subject) || len(id.Subject) < 1 || len(id.Subject) > 256 || strings.ContainsAny(id.Subject, "\x00\r\n") {
		return false
	}
	if id.Issuer == BuiltinIssuer {
		return true
	}
	u, e := url.Parse(id.Issuer)
	return e == nil && len(id.Issuer) <= 512 && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery
}

func Random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Hash(raw string) string { s := sha256.Sum256([]byte(raw)); return hex.EncodeToString(s[:]) }
func validToken(raw string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(raw)
	return e == nil && len(b) == 32 && len(raw) == 43
}

type Service struct {
	auth, owners *pgxpool.Pool
	origin       string
	// agentOrigin, when set, is the only public origin for agent OAuth and MCP.
	// Browser sign-in and consent stay on origin.
	agentOrigin string
	// signInPath is where a signed-out owner is sent during agent consent.
	signInPath string
}

func New(auth, owners *pgxpool.Pool, origin string) (*Service, error) {
	u, e := url.Parse(origin)
	if auth == nil || owners == nil || auth.Config().ConnConfig.User == owners.Config().ConnConfig.User || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrDenied
	}
	return &Service{auth: auth, owners: owners, origin: origin}, nil
}

// SetSignInPath sends owners who are signed out during agent consent to the
// deployment's own sign-in page, a same-origin absolute path. The deployment
// must return them to ResumeAuthorization's path after sign-in.
func (s *Service) SetSignInPath(path string) error {
	if s.signInPath != "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\ \t\r\n") || len(path) > 128 {
		return ErrDenied
	}
	s.signInPath = path
	return nil
}

// SignInPath is where agent consent sends a signed-out owner.
func (s *Service) SignInPath() string {
	if s.signInPath != "" {
		return s.signInPath
	}
	return "/auth/login"
}

// SetAgentOrigin serves agent registration, token exchange and MCP from their
// own HTTPS origin, which then becomes the OAuth issuer and protected resource.
// It must be a different host from the browser origin and is set once.
func (s *Service) SetAgentOrigin(origin string) error {
	u, e := url.Parse(origin)
	browser, _ := url.Parse(s.origin)
	if s.agentOrigin != "" || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.String() != origin || u.Host == browser.Host {
		return ErrDenied
	}
	s.agentOrigin = origin
	return nil
}

// IssueSession accepts only an identity already verified by the configured
// provider. Membership is looked up in Open; an unknown identity cannot enroll
// itself or claim a tenant. Authentication must include the deployment's MFA.
func (s *Service) IssueSession(ctx context.Context, id Identity) (string, error) {
	if !validIdentity(id) || id.AuthenticatedAt.IsZero() {
		return "", ErrDenied
	}
	raw := Random()
	var who string
	if e := s.auth.QueryRow(ctx, "SELECT access.issue_session($1,$2,$3,$4,$5)::text", id.Issuer, id.Subject, s.origin, Hash(raw), id.AuthenticatedAt).Scan(&who); e != nil {
		return "", ErrDenied
	}
	return raw, nil
}

func (s *Service) Identity(ctx context.Context, raw string) (Identity, error) {
	if !validToken(raw) {
		return Identity{}, ErrDenied
	}
	var id Identity
	if e := s.auth.QueryRow(ctx, "SELECT issuer,subject,authenticated_at FROM access.session_identity($1,$2)", Hash(raw), s.origin).Scan(&id.Issuer, &id.Subject, &id.AuthenticatedAt); e != nil {
		return Identity{}, ErrDenied
	}
	return id, nil
}

func (s *Service) Tenants(ctx context.Context, raw string) ([]string, error) {
	return s.tenants(ctx, raw, 0)
}

// TenantsBounded returns an explicit limit error instead of a partial owner list.
func (s *Service) TenantsBounded(ctx context.Context, raw string, limit int) ([]string, error) {
	if limit < 1 || limit > 1000 {
		return nil, ledger.ErrInvalid
	}
	return s.tenants(ctx, raw, limit)
}

func (s *Service) tenants(ctx context.Context, raw string, limit int) ([]string, error) {
	if !validToken(raw) {
		return nil, ErrDenied
	}
	query := "SELECT access.session_tenants($1,$2)::text"
	args := []any{Hash(raw), s.origin}
	if limit > 0 {
		query += " LIMIT $3"
		args = append(args, limit+1)
	}
	rows, e := s.auth.Query(ctx, query, args...)
	if e != nil {
		return nil, ErrDenied
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return nil, ErrDenied
		}
		out = append(out, id)
	}
	if rows.Err() != nil || len(out) == 0 {
		return nil, ErrDenied
	}
	if limit > 0 && len(out) > limit {
		return nil, ledger.ErrLimit
	}
	return out, nil
}

func (s *Service) EndSession(ctx context.Context, raw string) error {
	if !validToken(raw) {
		return ErrDenied
	}
	if _, e := s.auth.Exec(ctx, "SELECT access.end_session($1,$2)", Hash(raw), s.origin); e != nil {
		return ErrDenied
	}
	return nil
}

// WithOwner keeps session, identity and ownership rows and rechecks clock-based
// authorization before commit. Its credentials have no installation authority.
func (s *Service) WithOwner(ctx context.Context, raw, tenant string, writing bool, call func(ledger.TenantAdmin) error) error {
	if call == nil {
		return ErrDenied
	}
	return s.withOwner(ctx, raw, tenant, writing, writing, "", func(_ pgx.Tx, o ledger.TenantAdmin, _ string) error { return call(o) })
}

func (s *Service) withOwner(ctx context.Context, raw, tenant string, writing, fresh bool, requestLock string, call func(pgx.Tx, ledger.TenantAdmin, string) error) error {
	return withOwnerPool(ctx, s.owners, s.origin, raw, tenant, writing, fresh, requestLock, call)
}

func withOwnerPool(ctx context.Context, owners *pgxpool.Pool, origin, raw, tenant string, writing, fresh bool, requestLock string, call func(pgx.Tx, ledger.TenantAdmin, string) error) error {
	if !validToken(raw) || !ledger.ValidID(tenant) || call == nil {
		return ErrDenied
	}
	level := pgx.RepeatableRead
	if writing {
		level = pgx.ReadCommitted
	}
	tx, e := owners.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
	if e != nil {
		return ErrDenied
	}
	defer tx.Rollback(context.Background())
	if requestLock != "" {
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", requestLock); e != nil {
			return ErrDenied
		}
	}
	if _, e = tx.Exec(ctx, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true)", Hash(raw), origin, tenant); e != nil {
		return ErrDenied
	}
	var who string
	if e = tx.QueryRow(ctx, "SELECT access.require_owner($1,$2)::text", tenant, fresh).Scan(&who); e != nil {
		return ErrDenied
	}
	owner, e := ledger.TenantTransaction(tx, tenant)
	if e != nil {
		return ErrDenied
	}
	if e = call(tx, owner, who); e != nil {
		return e
	}
	// The held rows cannot be revoked concurrently, but time can still expire
	// the session or its required fresh proof while the callback is running.
	var confirmed string
	if e = tx.QueryRow(ctx, "SELECT access.require_owner($1,$2)::text", tenant, fresh).Scan(&confirmed); e != nil || confirmed != who {
		return ErrDenied
	}
	if e = tx.Commit(ctx); e != nil {
		var p *pgconn.PgError
		if errors.As(e, &p) {
			if p.Code == "VP001" {
				return ledger.ErrProposalStale
			}
			if p.Code == "42501" {
				return ErrDenied
			}
			if strings.HasPrefix(p.Code, "23") {
				return ledger.ErrInvalid
			}
		}
		return ledger.ErrUncertain
	}
	return nil
}

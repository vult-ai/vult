package server

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
)

// Authentication selects one sign-in method. All credentials are private files;
// the network process never receives installation/operator credentials.
type Authentication struct {
	Mode              string `json:"mode"`
	AuthDSNFile       string `json:"auth_dsn_file"`
	OwnerDSNFile      string `json:"owner_dsn_file"`
	Username          string `json:"username,omitempty"`
	CredentialKeyFile string `json:"credential_key_file,omitempty"`
	Issuer            string `json:"issuer,omitempty"`
	ClientID          string `json:"client_id,omitempty"`
	ClientSecretFile  string `json:"client_secret_file,omitempty"`
	MFAPolicy         bool   `json:"mfa_policy,omitempty"`
	Passkeys          bool   `json:"passkeys,omitempty"`
}

type Access struct {
	Sessions     *access.Service
	HTTP         *access.HTTP
	auth, owners *pgxpool.Pool
	identity     http.Handler
}

func (a *Access) Close() { a.auth.Close(); a.owners.Close() }

// OpenAccess checks distinct restricted database roles and installs only core
// session/cookie handling. An external provider proves identity, never ownership.
// A local browser may pass the service that decides requests from agents on
// this computer.
func OpenAccess(ctx context.Context, c Config, runtime *pgxpool.Pool, external IdentityProvider, agents ...access.LocalAgents) (*Access, error) {
	if _, e := c.validate(); e != nil || runtime == nil || c.Authentication == nil || len(agents) > 1 || (len(agents) == 1 && (!c.LocalBrowser || agents[0] == nil)) {
		return nil, ErrConfiguration
	}
	var localAgents access.LocalAgents
	if len(agents) == 1 {
		localAgents = agents[0]
	}
	f := c.Authentication
	if f.AuthDSNFile == "" || f.OwnerDSNFile == "" || f.Mode != "builtin" && f.Mode != "passkey" && f.Mode != "oidc" && f.Mode != "external" {
		return nil, ErrConfiguration
	}
	if (f.Mode == "external") != (external != nil) {
		return nil, ErrConfiguration
	}
	maxConns := int32(8)
	if c.LocalBrowser {
		maxConns = 2
	}
	auth, e := openDatabase(ctx, f.AuthDSNFile, maxConns)
	if e != nil {
		return nil, ErrDatabase
	}
	owners, e := openDatabase(ctx, f.OwnerDSNFile, maxConns)
	if e != nil {
		auth.Close()
		return nil, ErrDatabase
	}
	a := &Access{auth: auth, owners: owners}
	ok := false
	defer func() {
		if !ok {
			a.Close()
		}
	}()
	role := runtime.Config().ConnConfig.User
	base := runtime.Config().ConnConfig
	for _, p := range []*pgxpool.Pool{auth, owners} {
		other := p.Config().ConnConfig
		if other.Host != base.Host || other.Port != base.Port || other.Database != base.Database {
			return nil, ErrConfiguration
		}
	}
	if role == auth.Config().ConnConfig.User || role == owners.Config().ConnConfig.User {
		return nil, ErrConfiguration
	}
	a.Sessions, e = access.New(auth, owners, c.Origin)
	if e != nil {
		return nil, ErrConfiguration
	}
	if c.MCPOrigin != "" && a.Sessions.SetAgentOrigin(c.MCPOrigin) != nil {
		return nil, ErrConfiguration
	}
	if c.SignInPath != "" && a.Sessions.SetSignInPath(c.SignInPath) != nil {
		return nil, ErrConfiguration
	}
	var b *access.Builtin
	var p *access.Passkeys
	var o *access.OIDC
	var provider access.ExternalIdentity
	var authenticator *access.LocalAuthenticator
	switch f.Mode {
	case "builtin":
		if f.Issuer != "" || f.ClientID != "" || f.ClientSecretFile != "" || f.MFAPolicy {
			return nil, ErrConfiguration
		}
		key, err := ReadPrivateFile(f.CredentialKeyFile, 32)
		if err != nil || len(key) != 32 {
			return nil, ErrConfiguration
		}
		b, e = access.NewBuiltin(ctx, a.Sessions, f.Username, key)
		clear(key)
		if e == nil && f.Passkeys {
			p, e = access.NewPasskeys(ctx, a.Sessions, f.Username)
		}
	case "passkey":
		if f.CredentialKeyFile != "" || f.Issuer != "" || f.ClientID != "" || f.ClientSecretFile != "" || f.MFAPolicy {
			return nil, ErrConfiguration
		}
		p, e = access.NewPasskeys(ctx, a.Sessions, f.Username)
		if e == nil && c.LocalBrowser {
			authenticator, e = localAuthenticator(p, f.AuthDSNFile)
		}
	case "oidc":
		if f.Username != "" || f.CredentialKeyFile != "" || f.Passkeys {
			return nil, ErrConfiguration
		}
		secret, err := ReadPrivateFile(f.ClientSecretFile, 4096)
		if err != nil {
			return nil, ErrConfiguration
		}
		o, e = access.NewOIDC(ctx, a.Sessions, f.Issuer, f.ClientID, string(secret), f.MFAPolicy, nil)
		clear(secret)
	case "external":
		if f.Username != "" || f.CredentialKeyFile != "" || f.Issuer != "" || f.ClientID != "" || f.ClientSecretFile != "" || f.MFAPolicy || f.Passkeys {
			return nil, ErrConfiguration
		}
		provider = func(r *http.Request) (access.Identity, error) { return external.Authenticate(r.Context(), r) }
	}
	if e != nil {
		return nil, ErrConfiguration
	}
	if pages, ok := external.(IdentityPages); ok && f.Mode == "external" {
		a.identity = pages.IdentityHandler()
		if a.identity == nil {
			return nil, ErrConfiguration
		}
		a.HTTP, e = access.NewExternalHTTP(a.Sessions, provider, pages.LoginPage)
	} else {
		a.HTTP, e = access.NewHTTP(a.Sessions, b, o, provider, access.HTTPOptions{Passkeys: p, Local: c.LocalBrowser, Agents: localAgents, Authenticator: authenticator})
	}
	if e != nil {
		return nil, ErrConfiguration
	}
	ok = true
	return a, nil
}

// localAuthenticator seals the local owner's authenticator secret with a key
// derived from the browser service's private database credential. The database
// holds only a verifier for that credential, so a copy of the database or one
// of its backups cannot open the secret.
func localAuthenticator(p *access.Passkeys, credentialFile string) (*access.LocalAuthenticator, error) {
	secret, err := ReadPrivateFile(credentialFile, 16<<10)
	if err != nil || len(secret) == 0 {
		return nil, ErrConfiguration
	}
	defer clear(secret)
	key, err := hkdf.Key(sha256.New, secret, nil, "vult-local-authenticator-v1", 32)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer clear(key)
	return access.NewLocalAuthenticator(p, key)
}

package access_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

type identityFixture struct {
	server                 *httptest.Server
	key, wrong             *rsa.PrivateKey
	mu                     sync.Mutex
	nonce, challenge, code string
	changes                map[string]any
	badSignature           bool
}

func identityProvider(t *testing.T) *identityFixture {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	wrong, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	p := &identityFixture{key: key, wrong: wrong}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "example", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if r.ParseForm() != nil {
				w.WriteHeader(400)
				return
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if p.code == "" || r.Form.Get("code") != p.code || base64.RawURLEncoding.EncodeToString(hash[:]) != p.challenge {
				w.WriteHeader(400)
				return
			}
			p.code = ""
			claims := map[string]any{"iss": p.server.URL, "sub": "alpha", "aud": "example-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "auth_time": time.Now().Unix(), "nonce": p.nonce}
			for k, v := range p.changes {
				claims[k] = v
			}
			signing := p.key
			if p.badSignature {
				signing = p.wrong
			}
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: signing, KeyID: "example"}}, nil)
			if e != nil {
				w.WriteHeader(500)
				return
			}
			raw, _ := json.Marshal(claims)
			signed, e := signer.Sign(raw)
			if e != nil {
				w.WriteHeader(500)
				return
			}
			token, _ := signed.CompactSerialize()
			json.NewEncoder(w).Encode(map[string]any{"access_token": "example-provider-token", "token_type": "Bearer", "expires_in": 60, "id_token": token})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}
func (p *identityFixture) prepare(t *testing.T, destination string) string {
	t.Helper()
	u, e := url.Parse(destination)
	if e != nil {
		t.Fatal(e)
	}
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("max_age") != "0" || u.Query().Get("prompt") != "login" {
		t.Fatal("login omitted fresh authentication or PKCE")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce = u.Query().Get("nonce")
	p.challenge = u.Query().Get("code_challenge")
	p.code = access.Random()
	return p.code
}

func TestOIDCVerifiesIdentityAndNeverTrustsOwnershipClaims(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	p := identityProvider(t)
	id := access.Identity{Issuer: p.server.URL, Subject: "alpha", AuthenticatedAt: time.Now()}
	tenant, e := f.operator.BindOwner(ctx, "", id)
	if e != nil {
		t.Fatal(e)
	}
	other, _, _ := f.owner(t, "other")
	o, e := access.NewOIDC(ctx, f.service, p.server.URL, "example-client", "example-secret", true, p.server.Client())
	if e != nil {
		t.Fatal(e)
	}
	p.changes = map[string]any{"tenant_id": other, "role": "owner"}
	destination, cookie, e := o.Start(ctx)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(destination)
	state := u.Query().Get("state")
	code := p.prepare(t, destination)
	if _, e = o.Callback(ctx, cookie, access.Random(), code); !errors.Is(e, access.ErrDenied) {
		t.Fatal("wrong state accepted")
	}
	session, e := o.Callback(ctx, cookie, state, code)
	if e != nil {
		t.Fatal("verified OIDC login failed", e)
	}
	if _, e = o.Callback(ctx, cookie, state, code); !errors.Is(e, access.ErrDenied) {
		t.Fatal("OIDC callback replay accepted")
	}
	book := f.book(t, tenant, session)
	if e = f.service.WithOwner(ctx, session, other, false, func(ledger.TenantAdmin) error { return nil }); !errors.Is(e, access.ErrDenied) {
		t.Fatal("provider ownership claim granted a tenant")
	}
	if e = f.service.WithOwner(ctx, session, tenant, false, func(a ledger.TenantAdmin) error { _, e := a.Book(ctx, book.ID); return e }); e != nil {
		t.Fatal(e)
	}
}

func TestOIDCRejectsInvalidProofAndMissingMFAPolicy(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	p := identityProvider(t)
	if _, e := f.operator.BindOwner(ctx, "", access.Identity{Issuer: p.server.URL, Subject: "alpha"}); e != nil {
		t.Fatal(e)
	}
	if _, e := access.NewOIDC(ctx, f.service, p.server.URL, "example-client", "example-secret", false, p.server.Client()); !errors.Is(e, access.ErrDenied) {
		t.Fatal("unverified provider MFA policy accepted")
	}
	o, e := access.NewOIDC(ctx, f.service, p.server.URL, "example-client", "example-secret", true, p.server.Client())
	if e != nil {
		t.Fatal(e)
	}
	cases := map[string]map[string]any{
		"issuer": {"iss": "https://other.example.com"}, "audience": {"aud": "other-client"},
		"authorized_party": {"aud": []string{"example-client", "other-client"}, "azp": "other-client"},
		"nonce":            {"nonce": "other"}, "expired": {"exp": time.Now().Add(-time.Hour).Unix()},
		"old_authentication":    {"auth_time": time.Now().Add(-time.Hour).Unix()},
		"future_authentication": {"auth_time": time.Now().Add(time.Hour).Unix()},
		"unknown_subject":       {"sub": "unbound"}, "signature": {},
	}
	for name, changes := range cases {
		t.Run(name, func(t *testing.T) {
			destination, cookie, e := o.Start(ctx)
			if e != nil {
				t.Fatal(e)
			}
			u, _ := url.Parse(destination)
			code := p.prepare(t, destination)
			p.mu.Lock()
			p.changes, p.badSignature = changes, name == "signature"
			p.mu.Unlock()
			if _, e = o.Callback(ctx, cookie, u.Query().Get("state"), code); !errors.Is(e, access.ErrDenied) {
				t.Fatal("invalid identity proof accepted")
			}
		})
	}
}

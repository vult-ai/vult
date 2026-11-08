package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestAuthenticationWiringRequiresSeparateRestrictedRoles(t *testing.T) {
	db := testutil.New(t)
	directory := t.TempDir()
	roles := []string{}
	files := []string{}
	for i := 0; i < 2; i++ {
		suffix := strings.ReplaceAll(ledger.ID(), "-", "")
		role := "signin_role_" + suffix
		if _, e := db.Admin.Exec(db.Context, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+suffix+"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); e != nil {
			t.Fatal(e)
		}
		roles = append(roles, role)
		c := db.Runtime.Config().Copy()
		c.ConnConfig.User, c.ConnConfig.Password = role, suffix
		file := filepath.Join(directory, role+".dsn")
		pool, e := pgxpool.NewWithConfig(db.Context, c)
		if e != nil {
			t.Fatal(e)
		}
		dsn := testutil.DSN(pool)
		pool.Close()
		if e := os.WriteFile(file, []byte(dsn), 0600); e != nil {
			t.Fatal(e)
		}
		files = append(files, file)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, r := range roles {
			if _, e := db.Admin.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{r}.Sanitize()); e != nil {
				t.Error("role cleanup failed")
			}
			if _, e := db.Admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{r}.Sanitize()); e != nil {
				t.Error("role removal failed")
			}
		}
	})
	op := access.Operator{DB: db.Admin}
	if e := op.ConfigureRoles(db.Context, db.Role, roles[0], roles[1]); e != nil {
		t.Fatal(e)
	}
	key := []byte(strings.Repeat("k", 32))
	keyFile := filepath.Join(directory, "credential.key")
	if e := os.WriteFile(keyFile, key, 0600); e != nil {
		t.Fatal(e)
	}
	enrollment, e := op.InitializeBuiltin(db.Context, "", "example", "Example horse battery staple 7428", key)
	if e != nil {
		t.Fatal(e)
	}
	c := Config{Origin: "https://ledger.example.com", Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1/32"}, Authentication: &Authentication{Mode: "builtin", AuthDSNFile: files[0], OwnerDSNFile: files[1], Username: "example", CredentialKeyFile: keyFile}}
	a, e := OpenAccess(db.Context, c, db.Runtime, nil)
	if e != nil {
		t.Fatal("configured built-in sign-in did not start", e)
	}
	for name, options := range map[string]Options{
		"missing-runtime":    {Access: a},
		"missing-access":     {Database: db.Runtime},
		"empty-access":       {Database: db.Runtime, Access: &Access{}},
		"readiness-override": {Database: db.Runtime, Access: a, Ready: func(context.Context) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			if s, e := New(c, options); !errors.Is(e, ErrConfiguration) || s != nil {
				t.Fatal("partial authentication configuration was accepted")
			}
		})
	}
	otherOrigin := c
	otherOrigin.Origin = "https://other.example.com"
	if s, e := New(otherOrigin, Options{Database: db.Runtime, Access: a}); !errors.Is(e, ErrConfiguration) || s != nil {
		t.Fatal("authentication from another origin was accepted")
	}
	a.Close()
	t.Run("built-in-passkeys", func(t *testing.T) {
		if _, err := op.InitializePasskeys(db.Context, enrollment.TenantID, "example", c.Origin); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"builtin", "passkey"} {
			config := c
			auth := *c.Authentication
			config.Authentication = &auth
			auth.Mode, auth.Passkeys = mode, true
			if mode == "passkey" {
				auth.CredentialKeyFile = ""
			}
			opened, err := OpenAccess(db.Context, config, db.Runtime, nil)
			if err != nil {
				t.Fatal("passkey configuration unavailable", mode, err)
			}
			service, err := New(config, Options{Database: db.Runtime, Access: opened})
			if err != nil {
				opened.Close()
				t.Fatal(err)
			}
			r := request()
			r.URL.Path = "/auth/login"
			w := httptest.NewRecorder()
			service.Handler.ServeHTTP(w, r)
			opened.Close()
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sign in with a passkey") || strings.Contains(w.Body.String(), "Passphrase") != (mode == "builtin") {
				t.Fatal("configured login methods changed", mode, w.Code)
			}
		}
	})
	t.Run("provider-pages", func(t *testing.T) {
		ext := c
		options := *c.Authentication
		ext.Authentication = &options
		options.Mode = "external"
		options.Username = ""
		options.CredentialKeyFile = ""
		provider := &exampleIdentityPages{}
		a, e := OpenAccess(db.Context, ext, db.Runtime, provider)
		if e != nil {
			t.Fatal(e)
		}
		defer a.Close()
		s, e := New(ext, Options{Database: db.Runtime, Access: a})
		if e != nil {
			t.Fatal(e)
		}
		r := request()
		r.URL.Path = "/auth/login"
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Referrer-Policy") != "same-origin" || provider.nonce == "" || !strings.Contains(w.Body.String(), "Example identity provider") {
			t.Fatal("provider page not bound to core login")
		}
		var cookie *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == "__Host-vult-login" {
				cookie = c
			}
		}
		if cookie == nil || cookie.Value != provider.nonce {
			t.Fatal("provider nonce differs from protected cookie")
		}
		for _, variant := range []string{"missing-proof", "foreign-origin", "untrusted-peer", "valid"} {
			t.Run(variant, func(t *testing.T) {
				r := request()
				r.Method = "POST"
				r.URL.Path = "/identity/challenge"
				r.Header.Set("Origin", ext.Origin)
				r.Header.Set("X-CSRF-Token", provider.nonce)
				r.AddCookie(cookie)
				switch variant {
				case "missing-proof":
					r.Header.Del("X-CSRF-Token")
				case "foreign-origin":
					r.Header.Set("Origin", "https://foreign.example.com")
				case "untrusted-peer":
					r.RemoteAddr = "192.0.2.2:12345"
				}
				w := httptest.NewRecorder()
				s.Handler.ServeHTTP(w, r)
				want := 403
				if variant == "valid" {
					want = 204
				}
				if w.Code != want {
					t.Fatal("provider ingress/CSRF boundary failed")
				}
			})
		}
		if provider.calls != 1 {
			t.Fatal("invalid requests reached provider ceremony")
		}
		r = request()
		r.URL.Path = "/unrelated"
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if provider.calls != 1 || w.Code != 503 {
			t.Fatal("provider installed routes outside identity prefix")
		}
	})
	for _, variant := range []string{"missing-provider", "same-role", "runtime-role", "wrong-key", "mixed-methods"} {
		t.Run(variant, func(t *testing.T) {
			changed := c
			options := *c.Authentication
			changed.Authentication = &options
			switch variant {
			case "missing-provider":
				options.Mode = "external"
				options.Username = ""
				options.CredentialKeyFile = ""
			case "same-role":
				options.OwnerDSNFile = options.AuthDSNFile
			case "runtime-role":
				runtimeFile := filepath.Join(directory, "runtime.dsn")
				if e := os.WriteFile(runtimeFile, []byte(testutil.DSN(db.Runtime)), 0600); e != nil {
					t.Fatal(e)
				}
				options.AuthDSNFile = runtimeFile
			case "wrong-key":
				options.CredentialKeyFile = filepath.Join(directory, "missing.key")
			case "mixed-methods":
				options.Issuer = "https://identity.example.com"
			}
			if a, e := OpenAccess(db.Context, changed, db.Runtime, nil); e == nil || a != nil {
				if a != nil {
					a.Close()
				}
				t.Fatal("unsafe authentication configuration accepted")
			}
		})
	}
}

type exampleIdentityPages struct {
	nonce string
	calls int
}

func (p *exampleIdentityPages) Authenticate(context.Context, *http.Request) (Identity, error) {
	return Identity{}, access.ErrDenied
}
func (p *exampleIdentityPages) LoginPage(w http.ResponseWriter, r *http.Request, nonce string) {
	p.nonce = nonce
	_, _ = io.WriteString(w, "Example identity provider")
}
func (p *exampleIdentityPages) IdentityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.calls++; w.WriteHeader(204) })
}

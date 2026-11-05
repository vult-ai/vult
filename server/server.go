package server

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
	"golang.org/x/time/rate"
)

// Identity contains only verified identity, never tenant or Book authority.
type Identity = access.Identity

type IdentityProvider interface {
	Authenticate(context.Context, *http.Request) (Identity, error)
}

// IdentityPages provides optional sign-in presentation and identity ceremonies.
// Only /identity/ routes are installed, behind the same ingress/body bounds and
// a login CSRF check on writes. Provider results still establish identity only.
type IdentityPages interface {
	LoginPage(http.ResponseWriter, *http.Request, string)
	IdentityHandler() http.Handler
}

// Admission may deny an already-authorized operation; it cannot grant access.
// Scope must be resolved by Open authorization, never request or provider claims.
// Durable financial quotas must additionally be enforced in the write transaction.
type Admission interface {
	Check(context.Context, ledger.Scope, string) error
}

// Options selects health-only or authenticated service operation.
// Database and Access must be supplied together to enable login, scoped owner
// administration, OAuth and MCP. Their lifecycle remains with the caller.
type Options struct {
	// Ready supplies a health-only database probe. It cannot override the
	// ledger schema check used by an authenticated service.
	Ready     func(context.Context) error
	Database  *pgxpool.Pool
	Access    *Access
	Admission Admission
}

// New constructs the bounded HTTPS ingress. Zero options expose liveness only;
// unconfigured financial, owner and login routes remain unavailable. Identity
// and admission extensions cannot grant tenant or Book authority.
func New(c Config, options Options) (*http.Server, error) {
	g, e := c.validate()
	if e != nil {
		return nil, e
	}
	ready := options.Ready
	var login *access.HTTP
	var oauth, mcp, owner, identity http.Handler
	if options.Access == nil {
		if options.Database != nil || options.Admission != nil {
			return nil, ErrConfiguration
		}
	} else {
		a, db := options.Access, options.Database
		if db == nil || a.Sessions == nil || a.HTTP == nil || a.Sessions.Resource() != c.Origin+"/mcp" || ready != nil {
			return nil, ErrConfiguration
		}
		var ownerAdmission access.OwnerAdmission
		if check, ok := options.Admission.(OwnerAdmission); ok {
			ownerAdmission = check.CheckOwnerTransaction
		}
		ready = func(ctx context.Context) error { return ledger.Ready(ctx, db) }
		login, identity = a.HTTP, a.identity
		oauth = access.NewOAuthHTTP(a.Sessions)
		mcp = remoteMCP(db, a.Sessions, options.Admission)
		owner = access.NewOwnerHTTP(a.Sessions, a.HTTP, ownerAdmission)
	}
	slots := make(chan struct{}, 32)
	requests := rate.NewLimiter(128, 256)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// Keep form origins intact without sending sign-in URLs to other origins.
		w.Header().Set("Referrer-Policy", "same-origin")
		defer func() {
			if recover() != nil {
				failure(w, http.StatusInternalServerError)
			}
		}()
		if !g.check(r) || r.Header.Get("Upgrade") != "" {
			failure(w, http.StatusForbidden)
			return
		}
		if credentialRoute(r) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id, MCP-Method, MCP-Name")
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, MCP-Protocol-Version")
		}
		if r.ContentLength > 1<<20 {
			failure(w, http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !requests.Allow() {
			w.Header().Set("Retry-After", "1")
			failure(w, http.StatusTooManyRequests)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			failure(w, http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if credentialRoute(r) && r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") && login != nil {
			login.ServeHTTP(w, r)
			return
		}
		if identity != nil && strings.HasPrefix(r.URL.Path, "/identity/") {
			if r.Method != "GET" && r.Method != "HEAD" && (login == nil || !login.CheckLoginCSRF(r)) {
				failure(w, 403)
				return
			}
			identity.ServeHTTP(w, r)
			return
		}
		if oauth != nil && (strings.HasPrefix(r.URL.Path, "/oauth/") || strings.HasPrefix(r.URL.Path, "/.well-known/oauth-")) {
			oauth.ServeHTTP(w, r)
			return
		}
		if mcp != nil && r.URL.Path == "/mcp" {
			mcp.ServeHTTP(w, r)
			return
		}
		if owner != nil && strings.HasPrefix(r.URL.Path, "/owner/") {
			owner.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && (r.Method == "GET" || r.Method == "HEAD") && r.URL.RawQuery == "" {
			if r.URL.Path == "/readyz" && (ready == nil || ready(ctx) != nil) {
				failure(w, http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		failure(w, http.StatusServiceUnavailable)
	})
	return &http.Server{Addr: c.Listen, Handler: handler, ErrorLog: log.New(io.Discard, "", 0),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}

func failure(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "{\"error\":\"request unavailable\"}\n")
}

// Serve closes its listener on cancellation and never returns raw network errors.
func Serve(ctx context.Context, s *http.Server, listener net.Listener) error {
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- s.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := s.Shutdown(shutdown); e != nil {
			_ = s.Close()
			return ErrUnavailable
		}
		<-done
		return nil
	case <-done:
		return ErrUnavailable
	}
}

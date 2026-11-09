package server

import (
	"context"
	"crypto/tls"
	"github.com/vult-ai/vult/internal/update"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
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
	Documents *documents.Vault
	// ReadDocument lets a local browser read through the protected service without
	// receiving its storage keys. It must recheck the exact owner's live session.
	ReadDocument func(context.Context, string, string, string, io.Writer) error
	// UploadDocument sends a local owner upload through the protected service.
	// The service must recheck the owner and verify the exact bytes.
	UploadDocument func(context.Context, string, string, ledger.AddDocument, io.Reader) (ledger.OwnerDocumentUpload, error)
	// LocalBooks lets a local browser create the owner's Books through the
	// protected service. Local only.
	LocalBooks LocalBooks
	// Updates tells the owner when a newer release is published; only Vult on
	// the owner's own computer sets it.
	Updates *update.Checker
	// Web is the built interface. It requires Access: every page reads the
	// signed-in session, and /api/v1 serves only that session.
	Web fs.FS
}

// New constructs the bounded HTTPS ingress. Zero options expose liveness only;
// unconfigured financial, owner and login routes remain unavailable. Identity
// and admission extensions cannot grant tenant or Book authority.
func New(c Config, options Options) (*http.Server, error) {
	g, e := c.validate()
	if e != nil {
		return nil, e
	}
	localTLS, e := localBrowserTLS(c)
	if e != nil {
		return nil, e
	}
	ready := options.Ready
	var login *access.HTTP
	var oauth, mcp, owner, identity http.Handler
	var transfers *documentTransfers
	var app *webApp
	var api *webAPI
	if options.Access == nil {
		if options.Database != nil || options.Admission != nil || options.Documents != nil || options.Web != nil || options.ReadDocument != nil || options.UploadDocument != nil || options.LocalBooks != nil {
			return nil, ErrConfiguration
		}
	} else {
		a, db := options.Access, options.Database
		if db == nil || a.Sessions == nil || a.HTTP == nil || a.Sessions.Resource() != c.mcpResource() || ready != nil {
			return nil, ErrConfiguration
		}
		if options.LocalBooks != nil && !c.LocalBrowser {
			return nil, ErrConfiguration
		}
		if (options.ReadDocument != nil || options.UploadDocument != nil) && (!c.LocalBrowser || options.Documents != nil) {
			return nil, ErrConfiguration
		}
		var ownerAdmission access.OwnerAdmission
		if check, ok := options.Admission.(OwnerAdmission); ok {
			ownerAdmission = check.CheckOwnerTransaction
		}
		ready = func(ctx context.Context) error { return ledger.Ready(ctx, db) }
		login, identity = a.HTTP, a.identity
		if !c.LocalBrowser {
			oauth = access.NewOAuthHTTP(a.Sessions)
		}
		if options.Documents != nil && !c.LocalBrowser {
			if options.Documents.Storage == nil || options.Documents.Keys == nil {
				return nil, ErrConfiguration
			}
			transfers = newDocumentTransfers(c.Origin, db, a.Sessions, options.Admission, options.Documents)
		}
		if !c.LocalBrowser {
			mcp = remoteMCP(db, a.Sessions, options.Admission, transfers)
			owner = access.NewOwnerHTTP(a.Sessions, a.HTTP, ownerAdmission)
		}
		if options.Web != nil {
			artwork, e := sharedAssetOrigin(c.SharedAssetOrigin)
			if e != nil {
				return nil, e
			}
			if app, e = newWebApp(options.Web, artwork); e != nil {
				return nil, e
			}
			api = &webAPI{
				sessions: a.Sessions, login: a.HTTP, admit: ownerAdmission,
				admitDecision: operationDecisionAdmission(options.Admission, ownerAdmission),
				documents:     options.Documents, readDocument: options.ReadDocument,
				uploadDocument: options.UploadDocument, documentUploads: make(chan struct{}, 4),
				localBooks: options.LocalBooks, updates: options.Updates,
			}
		}
	}
	slots := make(chan struct{}, 32)
	requests := rate.NewLimiter(128, 256)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if api != nil && ownerSettingsRoute(r.URL.Path) {
			w.Header().Set("X-Vult-Schema-Version", "1")
		}
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
		if c.LocalBrowser && (r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/oauth/") || strings.HasPrefix(r.URL.Path, "/.well-known/oauth-") || strings.HasPrefix(r.URL.Path, "/owner/") || strings.HasPrefix(r.URL.Path, "/identity/")) {
			failure(w, http.StatusNotFound)
			return
		}
		if g.misdirected(r) {
			failure(w, http.StatusNotFound)
			return
		}
		if g.agentRoute(r) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id, MCP-Method, MCP-Name")
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, MCP-Protocol-Version")
		}
		byteRoute := transfers != nil && strings.HasPrefix(r.URL.Path, transferPrefix)
		ownerUpload := api != nil && r.Method == http.MethodPost && r.URL.Path == ownerDocumentUploadPath
		limit := int64(1 << 20)
		if byteRoute || ownerUpload {
			limit = documents.MaxSize
		}
		if r.ContentLength > limit {
			failure(w, http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
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
		timeout := 10 * time.Second
		if byteRoute || api != nil && strings.HasPrefix(r.URL.Path, "/api/v1/documents/") && strings.HasSuffix(r.URL.Path, "/content") {
			timeout = 90 * time.Second
		}
		if ownerUpload {
			timeout = ownerDocumentUploadTimeout
		}
		if api != nil && r.Method == http.MethodGet && bookExportType(r.URL.Path) != "" {
			timeout = bookExportTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		if byteRoute {
			transfers.ServeHTTP(w, r)
			return
		}
		if g.agentRoute(r) && r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/auth/approve" && app != nil && api != nil {
			api.proposalReviewPage(w, r, app)
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
		if mcp != nil && (g.agentHost == "" && r.URL.Path == "/mcp" || g.agentHost != "" && r.Host == g.agentHost && r.URL.Path == "/") {
			mcp.ServeHTTP(w, r)
			return
		}
		if owner != nil && strings.HasPrefix(r.URL.Path, "/owner/") {
			owner.ServeHTTP(w, r)
			return
		}
		if api != nil && !c.LocalBrowser && bookLogoWrite(r) && len(r.Header.Values("Authorization")) != 0 {
			api.agentBookLogo(w, r, options.Database, options.Admission)
			return
		}
		if api != nil && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		if app != nil && (r.Method == "GET" || r.Method == "HEAD") && interfacePath(r.URL.Path) {
			app.ServeHTTP(w, r)
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
	return &http.Server{Addr: c.Listen, Handler: handler, TLSConfig: localTLS, ErrorLog: log.New(io.Discard, "", 0),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}

func failure(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	if w.Header().Get("X-Vult-Schema-Version") == "1" {
		_, _ = io.WriteString(w, "{\"schema_version\":1,\"error\":\"request unavailable\"}\n")
		return
	}
	_, _ = io.WriteString(w, "{\"error\":\"request unavailable\"}\n")
}

// Serve closes its listener on cancellation and never returns raw network errors.
func Serve(ctx context.Context, s *http.Server, listener net.Listener) error {
	defer listener.Close()
	if s.TLSConfig != nil {
		host, port, err := net.SplitHostPort(listener.Addr().String())
		ip, parse := netip.ParseAddr(host)
		_, expectedPort, configured := net.SplitHostPort(s.Addr)
		if err != nil || parse != nil || configured != nil || !ip.IsLoopback() || port != expectedPort {
			return ErrConfiguration
		}
		listener = tls.NewListener(listener, s.TLSConfig)
	}
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

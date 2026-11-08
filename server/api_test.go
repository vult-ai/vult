package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func browserServer(t *testing.T) (*documentServerFixture, *http.Server) {
	t.Helper()
	f := documentServer(t)
	return f, newBrowserServer(t, f, nil)
}

func newBrowserServer(t *testing.T, f *documentServerFixture, admission Admission) *http.Server {
	t.Helper()
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(configuration(), Options{
		Database:  f.db.Runtime,
		Access:    &Access{Sessions: f.sessions, HTTP: login},
		Admission: admission,
		Web:       fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func browserRequest(method, path, session string) *http.Request {
	r := request()
	r.Method = method
	r.URL, _ = r.URL.Parse(path)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
	}
	return r
}

func TestBrowserAPIListsOnlyLiveOwnedBooks(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	second, err := f.operator.BindOwner(ctx, "", f.identity)
	if err != nil {
		t.Fatal(err)
	}
	other := access.Identity{Issuer: f.identity.Issuer, Subject: "other", AuthenticatedAt: time.Now().UTC()}
	foreign, err := f.operator.BindOwner(ctx, "", other)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.sessions.IssueSession(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	create := func(tenant, session, name string) ledger.Book {
		t.Helper()
		var book ledger.Book
		if err := f.sessions.WithOwner(ctx, session, tenant, true, func(owner ledger.TenantAdmin) error {
			var err error
			book, err = owner.CreateBook(ctx, name, "USD", 2)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return book
	}
	secondBook := create(second, f.session, "Example second Book")
	foreignBook := create(foreign, otherSession, "Example separate Book")
	check := func(session string, ids ...string) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/books", session))
		var result struct {
			Books []webBook `json:"books"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Books) != len(ids) {
			t.Fatalf("incorrect scoped Book list: %d %s", w.Code, w.Body.String())
		}
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		for _, book := range result.Books {
			if !wanted[book.ID] || book.Currency != "USD" || book.Scale != 2 {
				t.Fatal("Book list crossed the session's owner scope")
			}
			delete(wanted, book.ID)
		}
		if len(wanted) != 0 || strings.Contains(w.Body.String(), "tenant_id") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("incomplete or cacheable session response")
		}
	}
	check(f.session, f.book.ID, secondBook.ID)
	check(otherSession, foreignBook.ID)
	for _, path := range []string{"/api/v1/books?tenant_id=" + foreign, "/api/v1/books/" + foreignBook.ID, "/api/v1/books/" + ledger.ID()} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), foreignBook.Name) {
			t.Fatal("unsupported or foreign scope became observable")
		}
	}
	if _, err := f.db.Admin.Exec(ctx, "DELETE FROM access.owners WHERE tenant_id=$1", second); err != nil {
		t.Fatal(err)
	}
	check(f.session, f.book.ID)
}

func TestBrowserAPIRejectsCredentialsAndUnprovedWrites(t *testing.T) {
	f, s := browserServer(t)
	token := f.token(t, "read")
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		status int
	}{
		{"no-cookie", func(r *http.Request) { r.Header.Del("Cookie") }, http.StatusUnauthorized},
		{"invalid-cookie", func(r *http.Request) { r.Header.Set("Cookie", access.SessionCookie+"="+access.Random()) }, http.StatusUnauthorized},
		{"ambiguous-cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: access.Random()}) }, http.StatusUnauthorized},
		{"bearer-with-session", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, http.StatusForbidden},
		{"bearer-only", func(r *http.Request) { r.Header.Del("Cookie"); r.Header.Set("Authorization", "Bearer "+token) }, http.StatusForbidden},
		{"foreign-read-origin", func(r *http.Request) { r.Header.Set("Origin", "https://other.example.com") }, http.StatusForbidden},
		{"write-no-origin", func(r *http.Request) { r.Method = http.MethodPost }, http.StatusForbidden},
		{"write-foreign-origin", func(r *http.Request) { r.Method = http.MethodPost; r.Header.Set("Origin", "https://other.example.com") }, http.StatusForbidden},
		{"write-no-csrf", func(r *http.Request) { r.Method = http.MethodPost; r.Header.Set("Origin", configuration().Origin) }, http.StatusForbidden},
		{"write-wrong-csrf", func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("Origin", configuration().Origin)
			r.Header.Set("X-CSRF-Token", "wrong")
		}, http.StatusForbidden},
		{"write-duplicate-csrf", func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("Origin", configuration().Origin)
			r.Header.Add("X-CSRF-Token", access.Hash("csrf:"+f.session))
			r.Header.Add("X-CSRF-Token", access.Hash("csrf:"+f.session))
		}, http.StatusForbidden},
		{"unsupported-proved-write", func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("Origin", configuration().Origin)
			r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+f.session))
		}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := browserRequest(http.MethodGet, "/api/v1/books", f.session)
			tc.change(r)
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), f.book.Name) {
				t.Fatalf("browser boundary: status %d, want %d", w.Code, tc.status)
			}
		})
	}
	var count int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.books").Scan(&count); err != nil || count != 1 {
		t.Fatal("a rejected or unsupported write changed Books", err)
	}
}

func TestBrowserAPIRechecksExpiredAndRevokedSessions(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/books", f.session))
		if w.Code != want {
			t.Fatalf("live session returned %d, want %d", w.Code, want)
		}
	}
	check(http.StatusOK)
	if err := f.sessions.EndSession(ctx, f.session); err != nil {
		t.Fatal(err)
	}
	check(http.StatusUnauthorized)
	var err error
	f.session, err = f.sessions.IssueSession(ctx, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	check(http.StatusOK)
	if _, err = f.db.Admin.Exec(ctx, "UPDATE access.sessions SET created_at=created_at-interval '3 hours',expires_at=expires_at-interval '3 hours' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	check(http.StatusUnauthorized)
	f.session, err = f.sessions.IssueSession(ctx, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.operator.RevokeIdentity(ctx, f.identity); err != nil {
		t.Fatal(err)
	}
	check(http.StatusUnauthorized)
}

type resourceLookupTrace struct{ queries int }

func (trace *resourceLookupTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	trace.queries++
	return ctx
}

func (*resourceLookupTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestBrowserResourceLookupDoesNotVisitOwnedTenants(t *testing.T) {
	f := documentServer(t)
	for range 20 {
		if _, err := f.operator.BindOwner(f.db.Context, "", f.identity); err != nil {
			t.Fatal(err)
		}
	}
	tracePool := func(pool *pgxpool.Pool, trace *resourceLookupTrace) *pgxpool.Pool {
		config := pool.Config()
		config.ConnConfig.Tracer = trace
		traced, err := pgxpool.NewWithConfig(f.db.Context, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(traced.Close)
		return traced
	}
	var authTrace, ownerTrace resourceLookupTrace
	sessions, err := access.New(tracePool(f.auth, &authTrace), tracePool(f.owners, &ownerTrace), configuration().Origin)
	if err != nil {
		t.Fatal(err)
	}
	handler := &webAPI{sessions: sessions}
	for _, path := range []string{bookAddressPath + "?book_id=" + ledger.ID(), connectionPath + "/" + ledger.ID() + "/books"} {
		authTrace.queries, ownerTrace.queries = 0, 0
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusNotFound || authTrace.queries != 2 || ownerTrace.queries != 0 {
			t.Fatalf("missing resource: status %d, auth queries %d, owner queries %d", w.Code, authTrace.queries, ownerTrace.queries)
		}
	}
}

func TestBrowserAPIBookLimitIsGlobalAcrossTenants(t *testing.T) {
	f, s := browserServer(t)
	second, err := f.operator.BindOwner(f.db.Context, "", f.identity)
	if err != nil {
		t.Fatal(err)
	}
	for tenant, count := range map[string]int{f.tenant: 499, second: 500} {
		if _, err = f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) SELECT $1,gen_random_uuid(),'Example Book '||n,'USD',2 FROM generate_series(1,$2) n", tenant, count); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/books", f.session))
	var result struct {
		Books []webBook `json:"books"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Books) != webBookLimit {
		t.Fatalf("exact-limit list failed: %d", w.Code)
	}
	if _, err = f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,gen_random_uuid(),'Example extra Book','USD',2)", second); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/books", f.session))
	if w.Code != http.StatusRequestEntityTooLarge || strings.Contains(w.Body.String(), "Example Book") || strings.Contains(w.Body.String(), "\"books\"") {
		t.Fatal("global limit returned a partial Book list")
	}
}

func TestBrowserPagesPreserveProtocolRouting(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}
	if s, err := New(configuration(), Options{Web: files}); err != ErrConfiguration || s != nil {
		t.Fatal("browser pages enabled without authentication")
	}
	f, s := browserServer(t)
	for _, path := range []string{"/", "/example-studio/accounts", "/healthz", "/readyz", "/auth/session", "/api/v1/books", "/owner", "/mcp/extra", "/oauth", "/.well-known"} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		page := path == "/" || path == "/example-studio/accounts"
		if strings.Contains(w.Header().Get("Content-Type"), "text/html") != page || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("protocol route %s was shadowed", path)
		}
		if page && (w.Code != http.StatusOK || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline")) {
			t.Fatal("unsafe or unavailable interface page")
		}
	}
}

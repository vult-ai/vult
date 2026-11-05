package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func configuration() Config {
	return Config{Origin: "https://ledger.example.com", Listen: "127.0.0.1:9000", TrustedProxies: []string{"127.0.0.1/32"}}
}

func TestBrowserMCPPreflightAllowsStandardHeadersWithoutCookies(t *testing.T) {
	s, e := New(configuration(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	r := request()
	r.URL.Path = "/mcp"
	r.Method = "OPTIONS"
	r.Header.Set("Origin", "https://agent.example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type,mcp-protocol-version,mcp-method,mcp-name")
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credential-free MCP preflight failed")
	}
	allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	for _, header := range []string{"authorization", "content-type", "mcp-protocol-version", "mcp-method", "mcp-name"} {
		if !strings.Contains(allowed, header) {
			t.Fatal("required MCP browser header unavailable", header)
		}
	}
	r.AddCookie(&http.Cookie{Name: "__Host-vult-session", Value: "example"})
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign browser origin reached cookie-authenticated scope")
	}
}

func request() *http.Request {
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.Host, r.RemoteAddr = "ledger.example.com", "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	return r
}

func TestServerRejectsAmbiguousConfiguration(t *testing.T) {
	for _, origin := range []string{"http://ledger.example.com", "https://LEDGER.example.com", "https://ledger.example.com/", "https://ledger.example.com?", "https://user@ledger.example.com", "https://ledger.example.com:", "https://ledger.example.com:0", "https://ledger.example.com.", "https://bad_name.example.com", "https://127.0.0.1"} {
		c := configuration()
		c.Origin = origin
		if _, e := New(c, Options{}); !errors.Is(e, ErrConfiguration) {
			t.Fatal("ambiguous origin accepted")
		}
	}
	for _, address := range []string{"localhost:9000", "0.0.0.0:9000", "[::]:9000", "192.0.2.1:9000", "127.0.0.1:0"} {
		c := configuration()
		c.Listen = address
		if _, e := New(c, Options{}); !errors.Is(e, ErrConfiguration) {
			t.Fatal("unprotected listener accepted")
		}
	}
	for _, proxies := range [][]string{nil, {"0.0.0.0/0"}, {"127.0.0.1/8"}, {"192.0.2.0/24"}, {"::/0"}} {
		c := configuration()
		c.TrustedProxies = proxies
		if _, e := New(c, Options{}); !errors.Is(e, ErrConfiguration) {
			t.Fatal("unbounded proxy trust accepted")
		}
	}
	c := configuration()
	c.Listen, c.PrivateIngress = "0.0.0.0:9000", true
	if _, e := New(c, Options{}); e != nil {
		t.Fatal("explicit private container ingress rejected", e)
	}
}

func TestIngressRejectsSpoofingAndBrowserWrites(t *testing.T) {
	s, e := New(configuration(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	cases := map[string]func(*http.Request){
		"host":                  func(r *http.Request) { r.Host = "other.example.com" },
		"peer":                  func(r *http.Request) { r.RemoteAddr = "192.0.2.2:12345" },
		"scheme":                func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") },
		"duplicate_scheme":      func(r *http.Request) { r.Header.Add("X-Forwarded-Proto", "https") },
		"forwarded_host":        func(r *http.Request) { r.Header.Set("X-Forwarded-Host", "other.example.com") },
		"client_chain":          func(r *http.Request) { r.Header.Set("X-Forwarded-For", "192.0.2.1, 192.0.2.2") },
		"client_zone":           func(r *http.Request) { r.Header.Set("X-Forwarded-For", "fe80::1%eth0") },
		"unexpected_forwarding": func(r *http.Request) { r.Header.Set("X-Forwarded-Port", "443") },
		"standard_forwarded":    func(r *http.Request) { r.Header.Set("Forwarded", "proto=https") },
		"origin":                func(r *http.Request) { r.Header.Set("Origin", "https://other.example.com") },
		"opaque_origin": func(r *http.Request) {
			r.Method = "POST"
			r.Header.Set("Origin", "null")
			r.AddCookie(&http.Cookie{Name: "__Host-vult-session", Value: "example"})
		},
		"duplicate_origin": func(r *http.Request) {
			r.Header.Add("Origin", configuration().Origin)
			r.Header.Add("Origin", configuration().Origin)
		},
		"browser_write":         func(r *http.Request) { r.Method = "POST"; r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"cookie_without_origin": func(r *http.Request) { r.Method = "POST"; r.Header.Set("Cookie", "example=synthetic") },
		"upgrade":               func(r *http.Request) { r.Header.Set("Upgrade", "websocket") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := request()
			change(r)
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatal("untrusted ingress was accepted", w.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, request())
	if w.Code != http.StatusNoContent || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("canonical proxy liveness failed")
	}
}

func TestUnconfiguredRoutesAndReadinessFailClosed(t *testing.T) {
	for _, path := range []string{"/readyz", "/mcp", "/api/books", "/auth/start", "/healthz?example=private"} {
		s, _ := New(configuration(), Options{})
		r := request()
		u, _ := r.URL.Parse(path)
		r.URL = u
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatal("unconfigured capability was available", path, w.Code)
		}
	}
	s, _ := New(configuration(), Options{Ready: func(context.Context) error { return nil }})
	r := request()
	r.URL.Path = "/readyz"
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatal("database-ready probe failed")
	}
	r.URL.Path, r.ContentLength = "/mcp", 1<<20+1
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversized request was not rejected")
	}
}

func TestServerDiagnosticsExcludeFinancialData(t *testing.T) {
	const detail = "Example financial description 987654321"
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, panicInstead := range []bool{false, true} {
		s, _ := New(configuration(), Options{Ready: func(context.Context) error {
			if panicInstead {
				panic(detail)
			}
			return errors.New(detail)
		}})
		r := request()
		r.URL.Path = "/readyz"
		r.Header.Set("Authorization", "Bearer "+detail)
		r.Body = io.NopCloser(strings.NewReader(detail))
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if w.Code < 500 || strings.Contains(w.Body.String(), detail) || logs.Len() != 0 {
			t.Fatal("diagnostics disclosed request or dependency data")
		}
	}
}

func TestServerBoundsConcurrentRequests(t *testing.T) {
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	s, _ := New(configuration(), Options{Ready: func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	defer close(release)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := request()
			r.URL.Path = "/readyz"
			s.Handler.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("test did not fill the request boundary")
		}
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, request())
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("server queued an unbounded request")
	}
	// Release all callbacks before returning so the race check observes teardown.
	for i := 0; i < 32; i++ {
		release <- struct{}{}
	}
	wg.Wait()
}

func TestServerStopsItsListenerOnCancellation(t *testing.T) {
	s, _ := New(configuration(), Options{})
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, s, l) }()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	r := request()
	r.URL.Scheme, r.URL.Host = "http", l.Addr().String()
	r.RequestURI = ""
	response, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatal("live server probe failed")
	}
	cancel()
	select {
	case e = <-finished:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if c, e := net.DialTimeout("tcp", l.Addr().String(), time.Second); e == nil {
		c.Close()
		t.Fatal("listener survived shutdown")
	}
}

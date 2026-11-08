package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestWebAppServesInterfaceAddressesAndImmutableAssets(t *testing.T) {
	files := fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/main-Abc12345.js": {Data: []byte("console.log(1)")},
		"assets/settings.js":      {Data: []byte("console.log(2)")},
		"brand/vult-logo.svg":     {Data: []byte("<svg/>")},
	}
	app, e := newWebApp(files, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		path, kind, cache, body string
		status                  int
	}{
		{"/", "text/html; charset=utf-8", "no-cache", "<!doctype", 200},
		{"/example-studio/transactions", "text/html; charset=utf-8", "no-cache", "<!doctype", 200},
		{"/assets/main-Abc12345.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", "console", 200},
		{"/assets/settings.js", "text/javascript; charset=utf-8", "no-cache", "console", 200},
		{"/brand/vult-logo.svg", "image/svg+xml", "no-cache", "<svg", 200},
		{"/assets/missing.js", "", "", "", 404},
		{"/../index.html", "text/html; charset=utf-8", "no-cache", "<!doctype", 200},
	} {
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest("GET", "https://ledger.example.com"+c.path, nil))
		if w.Code != c.status || c.status == 200 && (w.Header().Get("Content-Type") != c.kind || w.Header().Get("Cache-Control") != c.cache || !strings.HasPrefix(w.Body.String(), c.body)) {
			t.Fatalf("%s: %d %q %q", c.path, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}
		if policy := w.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "script-src 'self'") || strings.Contains(policy, "unsafe") {
			t.Fatalf("%s: policy %q", c.path, policy)
		}
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("HEAD", "https://ledger.example.com/", nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD returned a body")
	}
	if _, e = newWebApp(fstest.MapFS{}, ""); e == nil {
		t.Fatal("an interface without index.html was accepted")
	}
}

func TestInterfaceNeverShadowsProtocolRoutes(t *testing.T) {
	for _, p := range []string{"/api/v1/books", "/auth/session", "/oauth/token", "/.well-known/oauth-authorization-server", "/owner/tenants/x/books", "/identity/x", "/mcp", "/healthz", "/readyz", transferPrefix + "x"} {
		if interfacePath(p) {
			t.Fatalf("%s served as an interface page", p)
		}
	}
	for _, p := range []string{"/api", "/auth", "/oauth", "/owner", "/identity", "/.well-known", "/mcp", "/healthz", "/readyz", strings.TrimSuffix(transferPrefix, "/")} {
		if interfacePath(p) || interfacePath(p+"/") {
			t.Fatalf("reserved protocol namespace %s served as an interface page", p)
		}
	}
	for _, p := range []string{"/", "/settings", "/example-studio/accounts", "/assets/main.js"} {
		if !interfacePath(p) {
			t.Fatalf("%s not served as an interface page", p)
		}
	}
}

func TestWebAPIRequiresTheBrowserSessionAndRefusesBearerTokens(t *testing.T) {
	api := &webAPI{}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "https://ledger.example.com/api/v1/books", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "https://ledger.example.com/api/v1/books", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 43))
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("bearer token: %d", w.Code)
	}
}

func TestWebAppReadsArtworkOnlyFromTheSharedOrigin(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte(`<meta name="vult-shared-asset-origin" content="__VULT_SHARED_ASSET_ORIGIN__" />`)}}
	for _, c := range []struct{ raw, origin string }{{"", DefaultSharedAssetOrigin}, {"off", ""}, {"https://art.example.com", "https://art.example.com"}} {
		origin, e := sharedAssetOrigin(c.raw)
		if e != nil || origin != c.origin {
			t.Fatalf("%q: %q %v", c.raw, origin, e)
		}
		app, e := newWebApp(files, origin)
		if e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest("GET", "https://ledger.example.com/", nil))
		if body := w.Body.String(); strings.Contains(body, "__VULT_") || !strings.Contains(body, `content="`+origin+`"`) {
			t.Fatalf("%q: index %q", c.raw, body)
		}
		policy := w.Header().Get("Content-Security-Policy")
		withArtwork := "img-src 'self' data: blob: " + origin + ";"
		if origin == "" && (!strings.Contains(policy, "img-src 'self' data: blob:;") || !strings.Contains(policy, "connect-src 'self';")) ||
			origin != "" && (!strings.Contains(policy, withArtwork) || !strings.Contains(policy, "connect-src 'self' "+origin+";")) ||
			strings.Contains(policy, "script-src 'self' http") {
			t.Fatalf("%q: policy %q", c.raw, policy)
		}
	}
	for _, raw := range []string{"http://art.example.com", "https://art.example.com/", "https://art.example.com/x", "https://Art.example.com", "https://u@art.example.com", "https://localhost", "https://art.localhost", "https://127.0.0.1", "https://[::1]", "https://art.example.com:0", "https://art.example.com.", "https://art.example.com?q", "https://art", "*", "https://art.example.com; script-src *"} {
		if _, e := sharedAssetOrigin(raw); e == nil {
			t.Fatalf("%q accepted as an artwork origin", raw)
		}
	}
}

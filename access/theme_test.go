package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
)

// Every sign-in page shares the first-run scene's top bar: the quiet logo and
// a theme menu drawn by a same-origin script the page policy allows.
func TestSignInPagesShareTheTopBarAndTheme(t *testing.T) {
	f := fresh(t)
	if _, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com"); err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com"+path, nil))
		return w
	}
	for _, path := range []string{"/auth/login", "/auth/setup", "/auth/recover"} {
		w := get(path)
		body := w.Body.String()
		head := body[:strings.Index(body, "</head>")]
		if w.Code != http.StatusOK || !strings.Contains(head, `<script src="/auth/theme.js"></script>`) ||
			!strings.Contains(body, `<body><header class="bar"><span class="mark"><svg class="brand"`) ||
			!strings.Contains(body, `id="theme-menu" class="theme-menu" role="menu"`) ||
			strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatal("the sign-in page has no shared top bar", path, w.Code)
		}
	}
	script := get("/auth/theme.js")
	if script.Code != http.StatusOK || script.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(script.Body.String(), `"vult-theme"`) {
		t.Fatal("the theme script is not served", script.Code)
	}
	if css := get("/auth/style.css").Body.String(); !strings.Contains(css, ":root.dark-mode{") || !strings.Contains(css, "@media (prefers-color-scheme:dark){:root:not(.light-mode){") {
		t.Fatal("sign-in styles do not follow the chosen theme")
	}
}

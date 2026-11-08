package server

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// DefaultSharedAssetOrigin serves public institution and brand artwork and its
// catalogs. No copy of that artwork ships with Vult; without the origin, avatars
// show initials.
const DefaultSharedAssetOrigin = "https://assets.vult.ai"

// sharedAssetPlaceholder is replaced in index.html with the configured origin.
var sharedAssetPlaceholder = []byte("__VULT_SHARED_ASSET_ORIGIN__")

// webPolicy confines the bundled interface to this origin. It never frames,
// never posts forms elsewhere and loads no third-party script. The one other
// origin it may read is the shared artwork origin, for images and catalogs.
func webPolicy(artwork string) string {
	extra := ""
	if artwork != "" {
		extra = " " + artwork
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:" + extra + "; font-src 'self'; connect-src 'self'" + extra + "; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

var hashedAsset = regexp.MustCompile(`^[^/]+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

// webApp serves the bundled interface. Hashed assets are immutable; any other
// interface address returns index.html so Book addresses load directly.
type webApp struct {
	files  fs.FS
	index  []byte
	policy string
}

// newWebApp takes artwork as an origin already accepted by sharedAssetOrigin,
// or "" for none.
func newWebApp(files fs.FS, artwork string) (*webApp, error) {
	index, e := fs.ReadFile(files, "index.html")
	if e != nil || len(index) == 0 {
		return nil, ErrConfiguration
	}
	index = bytes.ReplaceAll(index, sharedAssetPlaceholder, []byte(artwork))
	return &webApp{files: files, index: index, policy: webPolicy(artwork)}, nil
}

// interfacePath reports whether a path belongs to the interface rather than to
// a protocol route. Protocol routes keep their own handlers and failures.
func interfacePath(p string) bool {
	for _, prefix := range []string{"/api", "/auth", "/oauth", "/.well-known", "/owner", "/identity", strings.TrimSuffix(transferPrefix, "/"), "/mcp", "/healthz", "/readyz"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return false
		}
	}
	return true
}

func (a *webApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", a.policy)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" && strings.Contains(path.Base(name), ".") {
		file, e := a.files.Open(name)
		if e != nil {
			failure(w, http.StatusNotFound)
			return
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() {
			failure(w, http.StatusNotFound)
			return
		}
		kind := mime.TypeByExtension(path.Ext(name))
		if kind == "" {
			kind = "application/octet-stream"
		}
		h.Set("Content-Type", kind)
		h.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		if strings.HasPrefix(name, "assets/") && hashedAsset.MatchString(path.Base(name)) {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, file)
		}
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	if r.URL.Path == "/auth/approve" {
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(a.index)
	}
}

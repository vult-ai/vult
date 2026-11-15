//go:build devweb && !webui

package web

import (
	"bytes"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// App, in a development build, serves the interface from the folder named by
// VULT_DEV_WEB, so a new `npm run build` shows on reload, and marks every page
// with a small stamp saying which build is running. Release builds embed the
// interface instead (-tags webui) and have no stamp.
func App() fs.FS {
	dir := os.Getenv("VULT_DEV_WEB")
	if dir == "" {
		return nil
	}
	return devFS{root: os.DirFS(dir), stamp: os.Getenv("VULT_DEV_STAMP"), started: time.Now(), program: ownProgram()}
}

// ownProgram is this development program's path, which the page shows in the
// command that connects an agent to this Vult instead of an installed one.
func ownProgram() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self
}

type devFS struct {
	root    fs.FS
	stamp   string
	started time.Time
	program string
}

const stampStyle = "dev-build-stamp.css"

// A tiny, faint badge in the very corner: branch and commit, with the build
// times when hovered. It stays out of the way of the page beneath it.
var stampCSS = []byte(`#vult-build-stamp{position:fixed;right:2px;bottom:2px;z-index:2147483000;padding:0 4px;border-radius:4px;` +
	`background:rgb(10 10 10 / .55);color:#fafafa;font:500 9px/13px ui-monospace,SFMono-Regular,Menlo,monospace;opacity:.35;max-width:40vw;` +
	`overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:default}#vult-build-stamp:hover{opacity:1}`)

// Live tells the server to read index.html again for each page.
func (d devFS) Live() bool { return true }

func (d devFS) Open(name string) (fs.File, error) {
	if name == stampStyle {
		return memoryFile(name, stampCSS, d.started), nil
	}
	f, err := d.root.Open(name)
	if err != nil || name != "index.html" {
		return f, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	page, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	badge := d.stamp
	if badge == "" {
		badge = "dev"
	}
	text := badge + " · web built " + info.ModTime().Local().Format("Jan 2 15:04") + " · server " + d.started.Local().Format("Jan 2 15:04")
	mark := `<link rel="stylesheet" href="/` + stampStyle + `"><div id="vult-build-stamp" aria-hidden="true" title="` + html.EscapeString(text) + `">` + html.EscapeString(badge) + `</div></body>`
	page = bytes.Replace(page, []byte("</body>"), []byte(mark), 1)
	if d.program != "" {
		page = bytes.Replace(page, []byte("</head>"), []byte(`<meta name="vult-local-program" content="`+html.EscapeString(d.program)+`"></head>`), 1)
	}
	return memoryFile(name, page, info.ModTime()), nil
}

type memory struct {
	*bytes.Reader
	info memoryInfo
}

func memoryFile(name string, data []byte, at time.Time) fs.File {
	return &memory{Reader: bytes.NewReader(data), info: memoryInfo{name: name, size: int64(len(data)), at: at}}
}
func (m *memory) Stat() (fs.FileInfo, error) { return m.info, nil }
func (m *memory) Close() error               { return nil }

type memoryInfo struct {
	name string
	size int64
	at   time.Time
}

func (i memoryInfo) Name() string       { return i.name }
func (i memoryInfo) Size() int64        { return i.size }
func (i memoryInfo) Mode() fs.FileMode  { return 0o444 }
func (i memoryInfo) ModTime() time.Time { return i.at }
func (i memoryInfo) IsDir() bool        { return false }
func (i memoryInfo) Sys() any           { return nil }

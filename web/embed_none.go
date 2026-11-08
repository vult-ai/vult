//go:build !webui

package web

import "io/fs"

// App is nil in builds without the interface, so the server keeps its protocol
// routes and serves no pages.
func App() fs.FS { return nil }

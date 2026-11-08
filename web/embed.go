//go:build webui

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// App returns the built interface. Release builds run `npm run build` in this
// directory and then build with `-tags webui`.
func App() fs.FS {
	dist, e := fs.Sub(assets, "dist")
	if e != nil {
		return nil
	}
	return dist
}

// Package web embeds the built frontend. Until `npm run build` has run, dist/
// holds only .gitkeep and the static handler serves a "not built" notice.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the built SPA rooted at dist/.
func DistFS() (fs.FS, error) { return fs.Sub(distFS, "dist") }

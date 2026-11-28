// Package web embeds the built React frontend assets from web/dist for
// serving directly by pkg/app.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var rawDistFS embed.FS

// DistFS returns the embedded production frontend filesystem and true when
// index.html has been built into web/dist. When web/dist only holds .gitkeep
// (such as during unbuilt Go tests), it returns nil, false.
func DistFS() (fs.FS, bool) {
	sub, err := fs.Sub(rawDistFS, "dist")
	if err != nil {
		return nil, false
	}
	f, err := sub.Open("index.html")
	if err != nil {
		return nil, false
	}
	_ = f.Close()
	return sub, true
}

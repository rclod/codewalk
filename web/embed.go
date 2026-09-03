// Package web holds the embedded single-page application served by
// `codewalk serve`.
//
// The UI is plain HTML, CSS and JavaScript with no build step, and every asset
// it needs is embedded in the binary. That keeps `go install` self-contained and
// means the local server never fetches anything from the network on a reader's
// behalf.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var assets embed.FS

// FS returns the web application's file system, rooted at the asset directory.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// The embedded tree is compiled in, so this cannot fail at runtime.
		panic(err)
	}
	return sub
}

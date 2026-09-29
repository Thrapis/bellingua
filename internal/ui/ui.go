// Package ui embeds the statically exported web app. scripts/build.* run
// `npm run build` in web/ and copy web/out here before `go build`.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the exported site. Without a UI build it holds only a
// placeholder, and Built reports false.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether the web app was embedded.
func Built() bool {
	_, err := fs.Stat(Dist(), "index.html")
	return err == nil
}

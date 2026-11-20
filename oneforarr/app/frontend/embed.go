// Package frontend makes the built web interface (dist/) part of the
// program, so the app ships as one file. Build it first with `make ui`.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Files returns the built web interface.
func Files() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is always there; the build would have failed otherwise
	}
	return sub
}

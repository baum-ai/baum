// Package viewer embeds the built standalone review viewer, the single-page app
// that the viewer package builds from its app directory. The built files are
// committed so that go install needs nothing but Go; rebuild them with
// "pnpm build:app" in the viewer package whenever its source changes.
package viewer

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built app with index.html at its root.
func Assets() fs.FS {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: the embed directive guarantees the directory exists.
		panic(err)
	}
	return assets
}

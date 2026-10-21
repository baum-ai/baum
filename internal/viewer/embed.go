// Package viewer embeds the built standalone review viewer, the single-page app
// that the viewer package builds from its app directory. Only a release commit,
// the commit a v* tag points at, carries the built files, so go install of a
// tagged version needs nothing but Go. Any other checkout has an empty dist
// until "just viewer-build" runs, and a binary built from it answers with
// NotBuilt in place of the app.
package viewer

import (
	"embed"
	"io/fs"
	"net/http"
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

// NotBuilt answers a request for an app this binary was built without, so the
// reader learns why there is no page and how to get a binary that has one.
func NotBuilt(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("This baum was built without its web app. Install a release " +
		"(go install github.com/baum-ai/baum/cmd/baum@latest), or run \"just viewer-build\" " +
		"in the source tree before building it.\n"))
}

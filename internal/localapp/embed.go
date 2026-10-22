// Package localapp embeds the local application. Only a release commit, the
// commit a v* tag points at, carries the built assets, so go install of a
// tagged version needs only Go. Elsewhere, build them with "just viewer-build"
// (or pnpm --dir app build inside Docker); a binary built without them answers
// with viewer.NotBuilt in place of the application.
package localapp

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/baum-ai/baum/internal/viewer"
)

//go:embed all:dist
var dist embed.FS

func Assets() fs.FS {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return files
}

// Handler serves the embedded application for uiserver.Options.App, which
// strips the session namespace before calling it. The application routes with
// query strings on its base path, so only "/" and existing files are served;
// directories are never listed.
func Handler() http.Handler {
	return handler(Assets())
}

func handler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err != nil && name == "index.html" {
			viewer.NotBuilt(w)
			return
		}
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, name)
	})
}

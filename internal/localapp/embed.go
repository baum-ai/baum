// Package localapp embeds the local application. Built assets are committed so
// go install needs only Go; rebuild with pnpm --dir app build inside Docker.
package localapp

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
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
	assets := Assets()
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
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, name)
	})
}

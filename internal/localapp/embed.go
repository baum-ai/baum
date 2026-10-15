// Package localapp embeds the local application. Built assets are committed so
// go install needs only Go; rebuild with pnpm --dir app build inside Docker.
package localapp

import (
	"embed"
	"io/fs"
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

package traceprocessing

import (
	"io/fs"
	"os"
)

// FileSources is an explicitly found list of files in a session tree.
type FileSources interface {
	Len() int
	At(i int) (name, path, kind string)
}

// LocalFiles opens files by their local paths.
type LocalFiles struct{}

func (LocalFiles) Open(name string) (fs.File, error) { return os.Open(name) }

package traceprocessing

import (
	"bytes"
	"io/fs"
	"path"
	"time"
)

type BytesFSPolicy uint8

const (
	LogicalFilePolicy BytesFSPolicy = iota
	TreeFilePolicy
)

// BytesFS exposes byte slices as independent read-only files.
func BytesFS(files map[string][]byte, policy BytesFSPolicy) fs.FS {
	return bytesFS{files: files, policy: policy}
}

type bytesFS struct {
	files  map[string][]byte
	policy BytesFSPolicy
}

func (f bytesFS) Open(name string) (fs.File, error) {
	tree := f.policy == TreeFilePolicy
	if tree && !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	data, ok := f.files[name]
	if !ok || data == nil && !tree {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	statName := name
	mode := fs.FileMode(0o400)
	if tree {
		statName = path.Base(name)
		mode = 0o600
	}
	return &bytesFile{Reader: bytes.NewReader(data), name: statName, mode: mode}, nil
}

type bytesFile struct {
	*bytes.Reader
	name string
	mode fs.FileMode
}

func (f *bytesFile) Close() error               { return nil }
func (f *bytesFile) Stat() (fs.FileInfo, error) { return f, nil }
func (f *bytesFile) Name() string               { return f.name }
func (f *bytesFile) Size() int64                { return f.Reader.Size() }
func (f *bytesFile) Mode() fs.FileMode          { return f.mode }
func (f *bytesFile) ModTime() time.Time         { return time.Time{} }
func (f *bytesFile) IsDir() bool                { return false }
func (f *bytesFile) Sys() any                   { return nil }

package traceprocessing

import (
	"io/fs"
	"sort"
	"strings"
	"time"
)

// HostedBytesFS supports task-directory discovery used by appliance checkpoints.
func HostedBytesFS(files map[string][]byte, policy BytesFSPolicy) fs.FS {
	return treeBytesFS{bytesFS{files: files, policy: policy}}
}

type treeBytesFS struct{ bytesFS }

// ReadDir lists a directory of a tree. Directories are implied by the file
// paths under them; logical files have no directories.
func (f treeBytesFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.policy != TreeFilePolicy || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	children := map[string]bytesDirEntry{}
	for file, data := range f.files {
		rest, ok := strings.CutPrefix(file, prefix)
		if !ok || rest == "" {
			continue
		}
		child, _, nested := strings.Cut(rest, "/")
		if nested {
			children[child] = bytesDirEntry{name: child, dir: true}
		} else if _, seen := children[child]; !seen {
			children[child] = bytesDirEntry{name: child, size: int64(len(data))}
		}
	}
	if len(children) == 0 {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	entries := make([]fs.DirEntry, 0, len(children))
	for _, entry := range children {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

type bytesDirEntry struct {
	name string
	dir  bool
	size int64
}

func (e bytesDirEntry) Name() string { return e.name }
func (e bytesDirEntry) IsDir() bool  { return e.dir }
func (e bytesDirEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e bytesDirEntry) Info() (fs.FileInfo, error) {
	mode := fs.FileMode(0o600)
	if e.dir {
		mode = fs.ModeDir | 0o700
	}
	return bytesDirInfo{name: e.name, mode: mode, size: e.size}, nil
}

type bytesDirInfo struct {
	name string
	mode fs.FileMode
	size int64
}

func (i bytesDirInfo) Name() string       { return i.name }
func (i bytesDirInfo) Size() int64        { return i.size }
func (i bytesDirInfo) Mode() fs.FileMode  { return i.mode }
func (i bytesDirInfo) ModTime() time.Time { return time.Time{} }
func (i bytesDirInfo) IsDir() bool        { return i.mode.IsDir() }
func (i bytesDirInfo) Sys() any           { return nil }

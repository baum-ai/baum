package codextranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Bounds on reading Codex's thread index: the part of the file read, and the
// longest line looked into (an index line is a few hundred bytes).
const (
	maxIndexRead = 16 << 20
	maxIndexLine = 64 << 10
)

// IndexPath is the thread index of the Codex home a rollout file lives in,
// `<home>/session_index.jsonl` for `<home>/sessions/…/rollout-….jsonl`; empty
// when the rollout is not under a `sessions` directory.
func IndexPath(rollout string) string {
	for dir := filepath.Dir(rollout); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "sessions" {
			return filepath.Join(filepath.Dir(dir), "session_index.jsonl")
		}
	}
	return ""
}

// indexCache holds the names read from each index file, so a name is looked
// up without reading the file again while its size and modification time
// stay as they were.
var indexCache = struct {
	sync.Mutex
	byPath map[string]indexNames
}{byPath: map[string]indexNames{}}

type indexNames struct {
	size  int64
	mtime time.Time
	names map[string]string
}

// ThreadName is the last name Codex's thread index at path gives the thread
// id: the name Codex shows for the conversation. Empty, with no error, when
// the index does not name it. The file is read again only when its size or
// modification time changed.
func ThreadName(path, id string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	indexCache.Lock()
	cached, ok := indexCache.byPath[path]
	indexCache.Unlock()
	if ok && cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
		return cached.names[id], nil
	}
	names, err := readIndex(path)
	if err != nil {
		return names[id], err
	}
	indexCache.Lock()
	indexCache.byPath[path] = indexNames{size: info.Size(), mtime: info.ModTime(), names: names}
	indexCache.Unlock()
	return names[id], nil
}

// readIndex is the last name the index at path gives each thread.
func readIndex(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.LimitReader(f, maxIndexRead), maxIndexLine)
	names := map[string]string{}
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A line too long to be an index entry: skip the rest of it.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			line = nil
		}
		if len(line) > 0 {
			var v struct {
				ID         string `json:"id"`
				ThreadName string `json:"thread_name"`
			}
			if json.Unmarshal(bytes.TrimSpace(line), &v) == nil && v.ID != "" && strings.TrimSpace(v.ThreadName) != "" {
				names[v.ID] = v.ThreadName
			}
		}
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return names, err
		}
	}
}

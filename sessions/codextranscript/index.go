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

// ThreadName is the last name Codex's thread index at path gives the thread
// id: the name Codex shows for the conversation. Empty, with no error, when
// the index does not name it.
func ThreadName(path, id string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	needle := []byte(`"` + id + `"`)
	r := bufio.NewReaderSize(io.LimitReader(f, maxIndexRead), maxIndexLine)
	var name string
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A line too long to be an index entry: skip the rest of it.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			line = nil
		}
		if len(line) > 0 && bytes.Contains(line, needle) {
			var v struct {
				ID         string `json:"id"`
				ThreadName string `json:"thread_name"`
			}
			if json.Unmarshal(bytes.TrimSpace(line), &v) == nil && v.ID == id && strings.TrimSpace(v.ThreadName) != "" {
				name = v.ThreadName
			}
		}
		if errors.Is(err, io.EOF) {
			return name, nil
		}
		if err != nil {
			return name, err
		}
	}
}

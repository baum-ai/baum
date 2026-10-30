package traceprocessing

import (
	"errors"
	"io"
	"io/fs"
	"testing"
)

func TestHostedBytesFSPolicies(t *testing.T) {
	for _, test := range []struct {
		name                string
		policy              BytesFSPolicy
		invalid, nilMissing error
		statName            string
		mode                fs.FileMode
	}{
		{"logical files", LogicalFilePolicy, fs.ErrNotExist, fs.ErrNotExist, "dir/file", 0o400},
		{"tree files", TreeFilePolicy, fs.ErrInvalid, nil, "file", 0o600},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem := HostedBytesFS(map[string][]byte{
				"dir/file": []byte("body"),
				"nil":      nil,
			}, test.policy)
			if _, err := filesystem.Open(".."); !errors.Is(err, test.invalid) {
				t.Fatalf("invalid path error = %v, want %v", err, test.invalid)
			}
			if _, err := filesystem.Open("missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("missing file error = %v, want not exist", err)
			}
			nilFile, err := filesystem.Open("nil")
			if !errors.Is(err, test.nilMissing) {
				t.Fatalf("nil file error = %v, want %v", err, test.nilMissing)
			}
			if err == nil {
				info, statErr := nilFile.Stat()
				if statErr != nil || info.Size() != 0 {
					t.Fatalf("nil file stat = %+v, %v", info, statErr)
				}
				if err := nilFile.Close(); err != nil {
					t.Fatalf("close nil file: %v", err)
				}
			}

			first, err := filesystem.Open("dir/file")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(io.LimitReader(first, 1)); err != nil {
				t.Fatal(err)
			}
			second, err := filesystem.Open("dir/file")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(second)
			if err != nil || string(body) != "body" {
				t.Fatalf("independent read = %q, %v", body, err)
			}
			info, err := second.Stat()
			if err != nil || info.Name() != test.statName || info.Mode() != test.mode {
				t.Fatalf("stat = %+v, %v", info, err)
			}
			if err := first.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if err := second.Close(); err != nil {
				t.Fatalf("close second: %v", err)
			}
		})
	}
}

func TestHostedBytesFSListsTreeDirectories(t *testing.T) {
	filesystem := HostedBytesFS(map[string][]byte{
		"metadata.json":          []byte("{}"),
		"tasks/a/task.json":      []byte("{}"),
		"tasks/a/agent-x.jsonl":  []byte("line"),
		"tasks/b/task.json":      []byte("{}"),
		"tasksibling/other.json": []byte("{}"),
	}, TreeFilePolicy)
	entries, err := fs.ReadDir(filesystem, "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "a" || !entries[0].IsDir() || entries[1].Name() != "b" {
		t.Fatalf("tasks entries = %v", entries)
	}
	files, err := fs.ReadDir(filesystem, "tasks/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name() != "agent-x.jsonl" || files[0].IsDir() {
		t.Fatalf("task entries = %v", files)
	}
	if info, err := files[0].Info(); err != nil || info.Size() != 4 {
		t.Fatalf("file info = %+v, %v", info, err)
	}
	if _, err := fs.ReadDir(filesystem, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory error = %v, want not exist", err)
	}
	if _, err := fs.ReadDir(HostedBytesFS(map[string][]byte{"a/b": nil}, LogicalFilePolicy), "a"); err == nil {
		t.Fatal("logical files listed a directory")
	}
}

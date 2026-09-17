package traceprocessing

import (
	"errors"
	"io"
	"io/fs"
	"testing"
)

func TestBytesFSPolicies(t *testing.T) {
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
			filesystem := BytesFS(map[string][]byte{
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

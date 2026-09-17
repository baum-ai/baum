package entire

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

func TestOpenStripsSelectedSubtree(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	files := map[string]string{
		"ab/cdef012345/metadata.json":   `{"checkpoint_id":"abcdef012345"}`,
		"ab/cdef012345/0/metadata.json": `{"session_id":"s-1"}`,
	}
	for name, content := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Open(&archive, "ab/cdef012345")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(checkpoint, "0/metadata.json")
	if err != nil || !strings.Contains(string(data), `"s-1"`) {
		t.Fatalf("data=%s error=%v", data, err)
	}
}

// git archive leads with a pax_global_header and emits an entry per directory.
// Hand-built fixtures omit both, so exercise the real layout.
func TestOpenAcceptsGitArchiveLayout(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name:       "pax_global_header",
		Typeflag:   tar.TypeXGlobalHeader,
		Format:     tar.FormatPAX,
		PAXRecords: map[string]string{"comment": "0142aa77fe01"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"ab/", "ab/cdef012345/"} {
		if err := writer.WriteHeader(&tar.Header{Name: dir, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	content := `{"checkpoint_id":"abcdef012345"}`
	if err := writer.WriteHeader(&tar.Header{
		Name: "ab/cdef012345/metadata.json", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Open(&archive, "ab/cdef012345")
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(checkpoint, "metadata.json")
	if err != nil || string(data) != content {
		t.Fatalf("data=%s error=%v", data, err)
	}
}

func TestOpenRejectsUnexpectedArchiveEntry(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	content := "bad"
	if err := writer.WriteHeader(&tar.Header{Name: "other/file", Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(content))
	_ = writer.Close()
	if _, err := Open(&archive, "ab/cdef012345"); err == nil {
		t.Fatal("expected invalid archive error")
	}
}

func TestOpenRejectsUnsafeArchivePaths(t *testing.T) {
	for _, name := range []string{
		"ab/cdef012345/.",
		"ab/cdef012345/..",
		"ab/cdef012345/../outside",
	} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(&archive, "ab/cdef012345"); err == nil {
				t.Fatalf("expected %q to be rejected", name)
			}
		})
	}
}

func TestOpenReportsTruncatedFile(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name: "ab/cdef012345/metadata.json", Mode: 0o600, Size: 10, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not close the writer: Close would pad the declared file
	// and hide the truncated-input condition this test exercises.
	if _, err := Open(&archive, "ab/cdef012345"); err == nil ||
		!strings.Contains(err.Error(), "archive is truncated") {
		t.Fatalf("error = %v, want explicit truncation error", err)
	}
}

func TestOpenRejectsOversizedHeaderWithoutOverflow(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name: "ab/cdef012345/metadata.json", Mode: 0o600,
		Size: maxCheckpointBytes + 1, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(&archive, "ab/cdef012345"); err == nil ||
		!strings.Contains(err.Error(), "exceeds limits") {
		t.Fatalf("error = %v, want size-limit error", err)
	}
}

package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestApplyCheckoutOmissionsMarksHunksTextlessWithGroupedNotes(t *testing.T) {
	b := &Bundle{
		Diff: []Hunk{
			{ID: "h1", Path: "big.bin", Patch: "some patch text"},
			{ID: "h2", Path: "vendor/lib", Patch: "gitlink patch text"},
			{ID: "h3", Path: "vendor/lib2", Patch: "gitlink patch text"},
			{ID: "h4", Path: "normal.go", Patch: "unaffected patch text"},
		},
	}
	ApplyCheckoutOmissions(b, []CheckoutOmission{
		{Path: "big.bin", Reason: OmittedSizeReason},
		{Path: "vendor/lib", Reason: OmittedSubmoduleReason},
		{Path: "vendor/lib2", Reason: OmittedSubmoduleReason},
		{Path: "no-such-hunk.txt", Reason: OmittedSizeReason}, // Untouched files still count toward checkout omissions.
	})

	byPath := map[string]Hunk{}
	for _, hunk := range b.Diff {
		byPath[hunk.Path] = hunk
	}
	if h := byPath["big.bin"]; !h.Textless || h.TextlessReason != OmittedSizeReason || h.Patch != "" {
		t.Fatalf("big.bin hunk = %+v, want textless size omission with no patch", h)
	}
	if h := byPath["vendor/lib"]; !h.Textless || h.TextlessReason != OmittedSubmoduleReason || h.Patch != "" {
		t.Fatalf("vendor/lib hunk = %+v, want textless submodule omission with no patch", h)
	}
	if h := byPath["normal.go"]; h.Textless || h.Patch == "" {
		t.Fatalf("normal.go hunk = %+v, want it untouched", h)
	}

	if len(b.Truncated) != 2 {
		t.Fatalf("Truncated = %v, want one note per reason", b.Truncated)
	}
	joined := strings.Join(b.Truncated, "\n")
	if !strings.Contains(joined, "2 file(s): "+OmittedSizeReason) {
		t.Fatalf("Truncated = %v, want a note counting 2 files for %s", b.Truncated, OmittedSizeReason)
	}
	if !strings.Contains(joined, "2 file(s): "+OmittedSubmoduleReason) {
		t.Fatalf("Truncated = %v, want a note counting 2 files for %s", b.Truncated, OmittedSubmoduleReason)
	}
}

func TestParseGitmodulesPaths(t *testing.T) {
	content := []byte("[submodule \"vendor/a\"]\n\tpath = vendor/a\n\turl = https://example.test/a.git\n" +
		"[submodule \"vendor/b\"]\n\tpath=vendor/b\n\turl = https://example.test/b.git\n")
	got := parseGitmodulesPaths(content)
	want := []string{"vendor/a", "vendor/b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseGitmodulesPaths = %v, want %v", got, want)
	}
}

func TestUnpackCheckoutRejectsUnsafePathsAndOverBudgetArchives(t *testing.T) {
	for _, name := range []string{"../escape", "head/../../escape", "/head/escape", "head/.git/config", "head\\escape", "head/a/../escape", "other/file", "head/link", "head/big"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			entry := &tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1}
			if name == "head/link" {
				entry.Typeflag = tar.TypeSymlink
				entry.Linkname = "/etc/passwd"
				entry.Size = 0
			}
			_ = tw.WriteHeader(entry)
			if entry.Size > 0 {
				_, _ = tw.Write([]byte("x"))
			}
			_ = tw.Close()
			_ = gz.Close()
			root := t.TempDir()
			path := filepath.Join(t.TempDir(), "input.gz")
			_ = os.WriteFile(path, archive.Bytes(), 0600)
			caps := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxTotalFiles: 10}
			if name == "head/big" {
				caps.MaxTotalBytes = 0
			}
			if err := UnpackCheckout(context.Background(), path, root, caps); err == nil {
				t.Fatal("accepted unsafe or over-budget archive")
			}
		})
	}
}

// TestWriteArchiveRoundTripsThroughUnpackCheckout covers a pull request that
// replaces a directory by a file and declares a submodule: the archive holds
// each file once, the oversized file and the submodule are omitted, and the unpacked trees are read-only.
func TestWriteArchiveRoundTripsThroughUnpackCheckout(t *testing.T) {
	trees := map[string]map[string]string{
		"head":       {"foo": "now a file\n", "src/main.go": "package main\n", ".gitmodules": "[submodule \"v\"]\n\tpath = vendor/lib\n", "big.bin": strings.Repeat("0123456789", 10)},
		"merge-base": {"foo/a": "a\n", "foo/b": "b\n"},
	}
	export := fakeExport{
		head:      trees["head"],
		mergeBase: trees["merge-base"],
	}
	hunks := []Hunk{{Path: "foo"}, {Path: "foo/a"}, {Path: "foo/b"}, {Path: "vendor/lib"}}
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	config := CheckoutConfig{MaxFileBytes: 64, MaxTotalBytes: 1024, MaxTotalFiles: 10}
	omissions, err := NewCheckoutBuilder(config).WriteArchive(context.Background(), export, "head", "base", hunks, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	want := []CheckoutOmission{{Path: "big.bin", Reason: OmittedSizeReason}, {Path: "vendor/lib", Reason: OmittedSubmoduleReason}}
	if !slices.Equal(omissions, want) {
		t.Fatalf("omissions = %+v, want %+v", omissions, want)
	}
	path := filepath.Join(t.TempDir(), "checkout.tar.gz")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The unpacked checkout is read-only; let TempDir remove it.
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(name string, _ os.DirEntry, _ error) error { return os.Chmod(name, 0o700) })
	})
	if err := UnpackCheckout(context.Background(), path, dir, config); err != nil {
		t.Fatalf("UnpackCheckout: %v", err)
	}
	for name, want := range map[string]string{"head/foo": "now a file\n", "head/src/main.go": "package main\n", "merge-base/foo/a": "a\n", "merge-base/foo/b": "b\n"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(content) != want {
			t.Fatalf("%s = %q, %v", name, content, err)
		}
	}
}

// fakeExport serves two commits' files as git archive would, tar streams of
// regular files: "head" is the head commit and anything else the merge base.
// Asking the merge base for a path it does not hold reports ErrNotFound.
type fakeExport struct{ head, mergeBase map[string]string }

func (f fakeExport) tree(commit string) map[string]string {
	if commit == "head" {
		return f.head
	}
	return f.mergeBase
}

func (f fakeExport) archive(files map[string]string) io.ReadCloser {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[name]))})
		_, _ = tw.Write([]byte(files[name]))
	}
	_ = tw.Close()
	return io.NopCloser(&out)
}

func (f fakeExport) ExportCommit(_ context.Context, commit string) (io.ReadCloser, error) {
	return f.archive(f.tree(commit)), nil
}

func (f fakeExport) ExportCommitPath(_ context.Context, commit, path string) (io.ReadCloser, error) {
	files := map[string]string{}
	for name, content := range f.tree(commit) {
		if name == path || strings.HasPrefix(name, path+"/") {
			files[name] = content
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("path %q: %w", path, ErrNotFound)
	}
	return f.archive(files), nil
}

package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// writeCheckoutArchive gzips a tar of regular one-byte files, a symlink when
// the name is "head/link", and one entry per "+"-separated name.
func writeCheckoutArchive(t *testing.T, names string) string {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, name := range strings.Split(names, "+") {
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
	}
	_ = tw.Close()
	_ = gz.Close()
	path := filepath.Join(t.TempDir(), "input.gz")
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnpackCheckoutRejectsUnsafePathsAsInvalidArchives(t *testing.T) {
	for _, name := range []string{"../escape", "head/../../escape", "/head/escape", "head/.git/config", "head\\escape", "head/a/../escape", "other/file", "head/link", "head/a+head/a/b"} {
		t.Run(name, func(t *testing.T) {
			caps := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxTotalFiles: 10}
			err := UnpackCheckout(context.Background(), writeCheckoutArchive(t, name), t.TempDir(), caps)
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("err = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestUnpackCheckoutEnforcesBudgets(t *testing.T) {
	caps := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 0, MaxTotalFiles: 10}
	err := UnpackCheckout(context.Background(), writeCheckoutArchive(t, "head/big"), t.TempDir(), caps)
	if err == nil || err.Error() != BudgetExceededReason("bytes") {
		t.Fatalf("err = %v, want the bytes budget", err)
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

// TestWriteArchiveWithoutMergeBaseHasHeadOnly covers a default branch
// checkout: no merge base and no compared paths give one head/ tree under the
// same budgets and omissions, and it unpacks.
func TestWriteArchiveWithoutMergeBaseHasHeadOnly(t *testing.T) {
	export := fakeExport{head: map[string]string{
		"a.go": "package a\n", "b.go": "package b\n", "logo.png": "\x89PNG\x00",
		"big.bin": strings.Repeat("0123456789", 10), ".gitmodules": "[submodule \"v\"]\n\tpath = vendor/lib\n",
	}}
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	config := CheckoutConfig{MaxFileBytes: 64, MaxTotalBytes: 1024, MaxTotalFiles: 2}
	omissions, err := NewCheckoutBuilder(config).WriteArchive(context.Background(), export, "head", "", nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	want := []CheckoutOmission{
		{Path: "b.go", Reason: OmittedBudgetReason}, {Path: "big.bin", Reason: OmittedSizeReason},
		{Path: "logo.png", Reason: OmittedBinaryReason}, {Path: "vendor/lib", Reason: OmittedSubmoduleReason},
	}
	if !slices.Equal(omissions, want) {
		t.Fatalf("omissions = %+v, want %+v", omissions, want)
	}
	path := filepath.Join(t.TempDir(), "checkout.tar.gz")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(name string, _ os.DirEntry, _ error) error { return os.Chmod(name, 0o700) })
	})
	if err := UnpackCheckout(context.Background(), path, dir, config); err != nil {
		t.Fatalf("UnpackCheckout: %v", err)
	}
	var got []string
	_ = filepath.WalkDir(dir, func(name string, _ os.DirEntry, _ error) error {
		rel, _ := filepath.Rel(dir, name)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if wantPaths := []string{".", "head", "head/.gitmodules", "head/a.go"}; !slices.Equal(got, wantPaths) {
		t.Fatalf("unpacked = %v, want %v", got, wantPaths)
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

func TestUnpackCheckoutKeepsScratchFilesystemErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores the read-only attribute on directories")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	caps := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxTotalFiles: 10}
	err := UnpackCheckout(context.Background(), writeCheckoutArchive(t, "head/a"), dir, caps)
	if err == nil || errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("err = %v, want a host filesystem error, not ErrInvalidArchive", err)
	}
}

// writeAndReadArchive writes export's checkout with config and returns the
// omissions and the archive's regular files by name.
func writeAndReadArchive(t *testing.T, export fakeExport, hunks []Hunk, config CheckoutConfig) ([]CheckoutOmission, map[string]string) {
	t.Helper()
	var archive bytes.Buffer
	omissions, err := NewCheckoutBuilder(config).WriteArchive(context.Background(), export, "head", "base", hunks, &archive)
	if err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	files := map[string]string{}
	reader := tar.NewReader(&archive)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			files[header.Name] = string(content)
		}
	}
	return omissions, files
}

// A file git would call binary, a NUL byte in its first 8000 bytes, is left
// out with the reason a binary hunk has; a NUL after that does not count.
func TestWriteArchiveOmitsBinaryFiles(t *testing.T) {
	late := strings.Repeat("a", binarySniffBytes) + "\x00"
	export := fakeExport{
		head:      map[string]string{"logo.png": "\x89PNG\r\n\x1a\n\x00\x00", "notes.txt": "text\n", "late.txt": late, "empty": ""},
		mergeBase: map[string]string{"logo.png": "\x89PNG\x00"},
	}
	// Exactly room for the three text files: binary files take no budget.
	textBytes := int64(len(export.head["notes.txt"]) + len(late))
	config := CheckoutConfig{MaxFileBytes: 1 << 20, MaxTotalBytes: textBytes, MaxTotalFiles: 3}
	omissions, files := writeAndReadArchive(t, export, []Hunk{{Path: "logo.png", Textless: true, TextlessReason: "binary"}}, config)
	if want := []CheckoutOmission{{Path: "logo.png", Reason: OmittedBinaryReason}}; !slices.Equal(omissions, want) {
		t.Fatalf("omissions = %+v, want %+v", omissions, want)
	}
	for name, want := range map[string]string{"head/notes.txt": "text\n", "head/late.txt": late, "head/empty": ""} {
		if got, ok := files[name]; !ok || got != want {
			t.Fatalf("%s = %q (present %v), want %q", name, got, ok, want)
		}
	}
	if _, ok := files["head/logo.png"]; ok {
		t.Fatal("binary head file was written")
	}
	if _, ok := files["merge-base/logo.png"]; ok {
		t.Fatal("binary merge-base file was written")
	}
}

// A compared file the diff holds as text is kept even when its exported bytes
// hold a NUL, as a working-tree-encoding of UTF-16 produces; the same bytes
// outside the diff are omitted as binary.
func TestWriteArchiveKeepsComparedTextWithNULs(t *testing.T) {
	utf16 := "h\x00i\x00\n\x00"
	export := fakeExport{
		head:      map[string]string{"notes.txt": utf16, "other.txt": utf16},
		mergeBase: map[string]string{"notes.txt": "h\x00\n\x00"},
	}
	hunks := []Hunk{{Path: "notes.txt", Patch: "-h\n+hi\n"}}
	config := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxTotalFiles: 10}
	omissions, files := writeAndReadArchive(t, export, hunks, config)
	if files["head/notes.txt"] != utf16 || files["merge-base/notes.txt"] != "h\x00\n\x00" {
		t.Fatalf("compared text file missing: %q", files)
	}
	if want := []CheckoutOmission{{Path: "other.txt", Reason: OmittedBinaryReason}}; !slices.Equal(omissions, want) {
		t.Fatalf("omissions = %+v, want %+v", omissions, want)
	}
}

// Over the byte or file budget, the export keeps the compared paths at both
// trees, fills the rest of the budget with other head files, and omits what
// does not fit instead of failing; the archive unpacks under the same caps.
func TestWriteArchiveKeepsComparedPathsWithinTheBudget(t *testing.T) {
	head := map[string]string{"z/changed.go": "package z // head\n", "a.txt": "aaaa\n", "b.txt": "bbbb\n", "c.txt": "cccc\n"}
	mergeBase := map[string]string{"z/changed.go": "package z // base\n"}
	hunks := []Hunk{{Path: "z/changed.go"}}
	compared := int64(len(head["z/changed.go"]) + len(mergeBase["z/changed.go"]))
	for name, config := range map[string]CheckoutConfig{
		// Room for the compared files and one five-byte file.
		"bytes": {MaxFileBytes: 1024, MaxTotalBytes: compared + 5, MaxTotalFiles: 100},
		"files": {MaxFileBytes: 1024, MaxTotalBytes: 1 << 20, MaxTotalFiles: 3},
	} {
		t.Run(name, func(t *testing.T) {
			omissions, files := writeAndReadArchive(t, fakeExport{head: head, mergeBase: mergeBase}, hunks, config)
			if files["head/z/changed.go"] != head["z/changed.go"] || files["merge-base/z/changed.go"] != mergeBase["z/changed.go"] {
				t.Fatalf("compared path missing: %v", files)
			}
			// The head tree's archive order puts a.txt first.
			if files["head/a.txt"] != "aaaa\n" || len(files) != 3 {
				t.Fatalf("files = %v, want the compared paths and a.txt", files)
			}
			want := []CheckoutOmission{{Path: "b.txt", Reason: OmittedBudgetReason}, {Path: "c.txt", Reason: OmittedBudgetReason}}
			if !slices.Equal(omissions, want) {
				t.Fatalf("omissions = %+v, want %+v", omissions, want)
			}
			var archive bytes.Buffer
			compressed := gzip.NewWriter(&archive)
			if _, err := NewCheckoutBuilder(config).WriteArchive(context.Background(), fakeExport{head: head, mergeBase: mergeBase}, "head", "base", hunks, compressed); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "checkout.tar.gz")
			if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			t.Cleanup(func() {
				_ = filepath.WalkDir(dir, func(name string, _ os.DirEntry, _ error) error { return os.Chmod(name, 0o700) })
			})
			if err := UnpackCheckout(context.Background(), path, dir, config); err != nil {
				t.Fatalf("UnpackCheckout: %v", err)
			}
		})
	}
}

// When the compared files alone exceed the budget, the ones that fit are kept
// and the rest are omitted, which makes their hunks textless.
func TestWriteArchiveOmitsComparedPathsThatDoNotFit(t *testing.T) {
	head := map[string]string{"a.go": "package a\n", "b.go": "package b\n"}
	hunks := []Hunk{{ID: "a", Path: "a.go", Patch: "+a"}, {ID: "b", Path: "b.go", Patch: "+b"}}
	config := CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: int64(len(head["a.go"])), MaxTotalFiles: 10}
	omissions, files := writeAndReadArchive(t, fakeExport{head: head, mergeBase: map[string]string{}}, hunks, config)
	if len(files) != 1 || files["head/a.go"] != head["a.go"] {
		t.Fatalf("files = %v, want only head/a.go", files)
	}
	if want := []CheckoutOmission{{Path: "b.go", Reason: OmittedBudgetReason}}; !slices.Equal(omissions, want) {
		t.Fatalf("omissions = %+v, want %+v", omissions, want)
	}
	b := &Bundle{Diff: hunks}
	ApplyCheckoutOmissions(b, omissions)
	if !b.Diff[1].Textless || b.Diff[1].TextlessReason != OmittedBudgetReason || b.Diff[0].Textless {
		t.Fatalf("hunks = %+v, want only b.go textless", b.Diff)
	}
}

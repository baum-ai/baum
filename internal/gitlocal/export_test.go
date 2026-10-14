package gitlocal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
)

// tarFiles reads the regular files of a tar archive into name -> content.
func tarFiles(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	files := map[string]string{}
	reader := tar.NewReader(r)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = string(content)
	}
}

func readAll(t *testing.T, rc io.ReadCloser, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// bigFixture is a clone whose head holds a 4 MiB incompressible file, so an
// export is still streaming long after git starts.
func bigFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "checkout", "-q", "feature")
	write(t, f.dir, "big.bin", string(data), 0o644)
	f.head = commit(t, f.dir, "big")
	git(t, f.dir, "checkout", "-q", "main")
	return f
}

func whole1(t *testing.T, r *Repo, oid string) []byte {
	t.Helper()
	rc, err := r.ExportCommit(context.Background(), oid)
	return readAll(t, rc, err)
}

func TestExportCommitAndPath(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "checkout", "-q", "feature")
	write(t, f.dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = https://example.com/sub.git\n", 0o644)
	head := commit(t, f.dir, "modules")
	git(t, f.dir, "checkout", "-q", "main")
	r := open(t, f.dir, Options{})
	ctx := context.Background()

	rc, err := r.ExportCommit(ctx, head)
	files := tarFiles(t, bytes.NewReader(readAll(t, rc, err)))
	for name, want := range map[string]string{
		"new.txt":              "new\nfile\n",
		"keep.txt":             "keep\n",
		"dir/renamed_only.txt": lines("rename only", 12),
		".gitmodules":          "[submodule \"sub\"]\n\tpath = sub\n\turl = https://example.com/sub.git\n",
	} {
		if files[name] != want {
			t.Errorf("%s = %q, want %q", name, files[name], want)
		}
	}
	if _, ok := files["del.txt"]; ok {
		t.Error("export holds a file the commit deleted")
	}

	for path, want := range map[string]string{"new.txt": "new\nfile\n", ".gitmodules": files[".gitmodules"]} {
		rc, err := r.ExportCommitPath(ctx, head, path)
		got := tarFiles(t, bytes.NewReader(readAll(t, rc, err)))
		if len(got) != 1 || got[path] != want {
			t.Errorf("path %s export = %q", path, got)
		}
	}
	// A directory exports its files only.
	rc, err = r.ExportCommitPath(ctx, head, "dir")
	if got := tarFiles(t, bytes.NewReader(readAll(t, rc, err))); len(got) != 1 || got["dir/renamed_only.txt"] == "" {
		t.Errorf("directory export = %q", got)
	}
}

func TestExportCommitErrors(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	missing := strings.Repeat("c", 40)
	tests := []struct {
		name string
		call func() (io.ReadCloser, error)
		want error
	}{
		{"unknown commit", func() (io.ReadCloser, error) { return r.ExportCommit(ctx, missing) }, ErrObjectNotFound},
		{"unknown commit, path", func() (io.ReadCloser, error) { return r.ExportCommitPath(ctx, missing, "new.txt") }, ErrObjectNotFound},
		{"unknown path", func() (io.ReadCloser, error) { return r.ExportCommitPath(ctx, f.head, "nope.txt") }, ErrObjectNotFound},
		{"not a commit", func() (io.ReadCloser, error) {
			return r.ExportCommit(ctx, git(t, f.dir, "rev-parse", f.head+":new.txt"))
		}, ErrObjectNotFound},
		{"option as commit", func() (io.ReadCloser, error) { return r.ExportCommit(ctx, "--output=/tmp/x") }, ErrInvalidInput},
		{"ref as commit", func() (io.ReadCloser, error) { return r.ExportCommit(ctx, "feature") }, ErrInvalidInput},
		{"parent path", func() (io.ReadCloser, error) { return r.ExportCommitPath(ctx, f.head, "../x") }, ErrInvalidInput},
		{"absolute path", func() (io.ReadCloser, error) { return r.ExportCommitPath(ctx, f.head, "/etc/passwd") }, ErrInvalidInput},
		{"empty path", func() (io.ReadCloser, error) { return r.ExportCommitPath(ctx, f.head, "") }, ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, err := tt.call()
			if rc != nil {
				_ = rc.Close()
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	// A pathspec magic or glob in the path is literal, so it names nothing.
	if _, err := r.ExportCommitPath(ctx, f.head, "*.txt"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("glob path err = %v, want ErrObjectNotFound", err)
	}
	if _, err := r.ExportCommitPath(ctx, f.head, ":(top)new.txt"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("magic path err = %v, want ErrObjectNotFound", err)
	}
}

func TestExportOutputCap(t *testing.T) {
	f := bigFixture(t)
	ctx := context.Background()
	whole := whole1(t, open(t, f.dir, Options{}), f.head)

	// A cap over the archive's size passes it; one byte over the cap fails.
	r := open(t, f.dir, Options{MaxArchiveBytes: int64(len(whole))})
	if got := whole1(t, r, f.head); !bytes.Equal(got, whole) {
		t.Fatal("an archive exactly at the cap differs")
	}
	r = open(t, f.dir, Options{MaxArchiveBytes: int64(len(whole)) - 1})
	rc, err := r.ExportCommit(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, rc)
	if !errors.Is(err, bundle.ErrOutputTooLarge) {
		t.Fatalf("read err = %v, want ErrOutputTooLarge", err)
	}
	// The error is sticky, and Close reaps git.
	if _, err := rc.Read(make([]byte, 8)); !errors.Is(err, bundle.ErrOutputTooLarge) {
		t.Fatalf("second read err = %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if rc.(*archiveReader).cmd.ProcessState == nil {
		t.Fatal("git was not reaped")
	}
	// A small cap stops the read early, well short of the archive.
	r = open(t, f.dir, Options{MaxArchiveBytes: 64 << 10})
	rc, err = r.ExportCommit(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rc)
	_ = rc.Close()
	if !errors.Is(err, bundle.ErrOutputTooLarge) || n > 64<<10 {
		t.Fatalf("read %d bytes, err = %v", n, err)
	}
}

func TestExportCancellationKillsGit(t *testing.T) {
	f := bigFixture(t)
	r := open(t, f.dir, Options{})

	// A cancelled context ends the stream with its error and reaps git.
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := r.ExportCommit(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rc, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = io.Copy(io.Discard, rc)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read err = %v, want context.Canceled", err)
	}
	_ = rc.Close()
	if rc.(*archiveReader).cmd.ProcessState == nil {
		t.Fatal("git was not reaped")
	}

	// A short-lived context cuts off a reader that stalls.
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rc, err = r.ExportCommit(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_, err = io.Copy(io.Discard, rc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read err = %v, want context.DeadlineExceeded", err)
	}
	_ = rc.Close()
	if rc.(*archiveReader).cmd.ProcessState == nil {
		t.Fatal("git was not reaped")
	}

	// Close without reading kills git too.
	rc, err = r.ExportCommit(context.Background(), f.head)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if rc.(*archiveReader).cmd.ProcessState == nil {
		t.Fatal("git was not reaped")
	}
}

func TestExportArchiveTimeout(t *testing.T) {
	f := bigFixture(t)
	r := open(t, f.dir, Options{ArchiveTimeout: time.Nanosecond})
	rc, err := r.ExportCommit(context.Background(), f.head)
	if err == nil {
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// TestRepoCloseDoesNotWaitForAnUnclosedExport: a reader nobody closes does
// not hold Repo.Close up, whether git was stopped by the timeout already or
// is still streaming.
func TestRepoCloseDoesNotWaitForAnUnclosedExport(t *testing.T) {
	f := bigFixture(t)
	for _, tt := range []struct {
		name    string
		timeout time.Duration
		pause   time.Duration
	}{
		{"after the timeout", 300 * time.Millisecond, time.Second},
		{"before the timeout", time.Minute, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			temp := t.TempDir()
			r := open(t, f.dir, Options{TempDir: temp, ArchiveTimeout: tt.timeout})
			rc, err := r.ExportCommit(context.Background(), f.head)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(rc, make([]byte, 4096)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(tt.pause)
			closed := make(chan error, 1)
			go func() { closed <- r.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Repo.Close is blocked by an unclosed export")
			}
			if _, err := io.Copy(io.Discard, rc); err == nil {
				t.Fatal("the stopped export read to a clean end")
			}
			_ = rc.Close()
			if rc.(*archiveReader).cmd.ProcessState == nil {
				t.Fatal("git was not reaped")
			}
			if _, err := r.ExportCommit(context.Background(), f.head); !errors.Is(err, ErrClosed) {
				t.Fatalf("ExportCommit err = %v, want ErrClosed", err)
			}
			if entries, _ := os.ReadDir(temp); len(entries) != 0 {
				t.Fatalf("temp dir not empty after Close: %v", entries)
			}
		})
	}
}

// TestExportCloseFromAnotherGoroutine: Close while another goroutine reads
// ends the read with an error, under -race.
func TestExportCloseFromAnotherGoroutine(t *testing.T) {
	f := bigFixture(t)
	r := open(t, f.dir, Options{})
	for range 5 {
		rc, err := r.ExportCommit(context.Background(), f.head)
		if err != nil {
			t.Fatal(err)
		}
		copied := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.Discard, rc)
			copied <- err
		}()
		time.Sleep(5 * time.Millisecond)
		if err := rc.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-copied:
		case <-time.After(5 * time.Second):
			t.Fatal("the read did not end after Close")
		}
		if rc.(*archiveReader).cmd.ProcessState == nil {
			t.Fatal("git was not reaped")
		}
	}
}

// TestExportFailureIsNotATruncatedArchive: a git that fails after it has
// started writing is an error from Read, not a clean end of stream.
func TestExportFailureIsNotATruncatedArchive(t *testing.T) {
	f := bigFixture(t)
	r := open(t, f.dir, Options{})
	// Break the blob after the commit and the path checks would pass:
	// remove the big file's loose object.
	oid := git(t, f.dir, "rev-parse", f.head+":big.bin")
	object := filepath.Join(f.dir, ".git", "objects", oid[:2], oid[2:])
	if _, err := os.Stat(object); err != nil {
		t.Skip("blob is not a loose object:", err)
	}
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	rc, err := r.ExportCommit(context.Background(), f.head)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, rc)
	_ = rc.Close()
	var gitErr *Error
	if !errors.As(err, &gitErr) || gitErr.Command != "archive" {
		t.Fatalf("read err = %v, want a git archive error", err)
	}
	if strings.Contains(err.Error(), f.dir) {
		t.Fatalf("error leaks a path: %v", err)
	}
}

// TestNotFoundSatisfiesTheBundleContract: a missing .gitmodules, a path the
// merge base lacks and an unknown commit are bundle.ErrNotFound, which is
// what bundle.PinnedExport and bundle.BlobFetcher callers test for.
func TestNotFoundSatisfiesTheBundleContract(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	if !errors.Is(ErrObjectNotFound, bundle.ErrNotFound) {
		t.Fatal("ErrObjectNotFound is not bundle.ErrNotFound")
	}
	calls := map[string]func() error{
		"missing .gitmodules":           func() error { _, err := r.ExportCommitPath(ctx, f.head, ".gitmodules"); return err },
		"path absent at the merge base": func() error { _, err := r.ExportCommitPath(ctx, f.base, "new.txt"); return err },
		"unknown commit":                func() error { _, err := r.ExportCommit(ctx, strings.Repeat("c", 40)); return err },
		"blob":                          func() error { _, err := r.Blob(ctx, f.head, "REVIEW.md"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, bundle.ErrNotFound) || !errors.Is(err, ErrObjectNotFound) {
			t.Errorf("%s: err = %v, want bundle.ErrNotFound", name, err)
		}
	}
	// LoadReviewConfig takes the Repo as its BlobFetcher.
	cfg, err := bundle.LoadReviewConfig(ctx, r, f.base)
	if err != nil || cfg.Present {
		t.Fatalf("absent REVIEW.md: %+v, %v", cfg, err)
	}
	git(t, f.dir, "checkout", "-q", "feature")
	write(t, f.dir, "REVIEW.md", "Be kind.\n", 0o644)
	rev := commit(t, f.dir, "review")
	git(t, f.dir, "checkout", "-q", "main")
	cfg, err = bundle.LoadReviewConfig(ctx, open(t, f.dir, Options{}), rev)
	if err != nil || !cfg.Present {
		t.Fatalf("present REVIEW.md: %+v, %v", cfg, err)
	}
}

// TestCheckoutEndToEnd pipes the checkout builder's archive, built from a
// local clone, into UnpackCheckout.
func TestCheckoutEndToEnd(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	write(t, dir, "keep.txt", "keep\n", 0o644)
	write(t, dir, "del.txt", "deleted by the pull request\n", 0o644)
	write(t, dir, "unrelated.txt", "not in the diff\n", 0o644)
	base := commit(t, dir, "base")
	git(t, dir, "checkout", "-q", "-b", "feature")
	git(t, dir, "rm", "-q", "del.txt")
	write(t, dir, "big.bin", strings.Repeat("x", 4096), 0o644)
	write(t, dir, "added.txt", "added\n", 0o644)
	write(t, dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = https://example.com/sub.git\n", 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",sub")
	git(t, dir, "commit", "-q", "-m", "head")
	head := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "main")
	before := snapshot(t, dir)

	r := open(t, dir, Options{TempDir: t.TempDir()})
	ctx := context.Background()
	cfg := bundle.CheckoutConfig{MaxFileBytes: 1024, MaxTotalBytes: 1 << 20, MaxTotalFiles: 100}
	hunks := []bundle.Hunk{
		{Path: "del.txt", Change: "removed"}, {Path: "big.bin", Change: "added"},
		{Path: "added.txt", Change: "added"}, {Path: "sub", Change: "added"},
	}
	pr, pw := io.Pipe()
	type built struct {
		omissions []bundle.CheckoutOmission
		err       error
	}
	result := make(chan built, 1)
	go func() {
		compressed := gzip.NewWriter(pw)
		omissions, err := bundle.NewCheckoutBuilder(cfg).WriteArchive(ctx, r, head, base, hunks, compressed)
		err = errors.Join(err, compressed.Close())
		_ = pw.CloseWithError(err)
		result <- built{omissions, err}
	}()
	archive := filepath.Join(t.TempDir(), "checkout.tar.gz")
	out, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, pr); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	res := <-result
	if res.err != nil {
		t.Fatal(res.err)
	}
	wantOmissions := []bundle.CheckoutOmission{
		{Path: "big.bin", Reason: bundle.OmittedSizeReason}, {Path: "sub", Reason: bundle.OmittedSubmoduleReason},
	}
	if !reflect.DeepEqual(res.omissions, wantOmissions) {
		t.Fatalf("omissions = %+v, want %+v", res.omissions, wantOmissions)
	}

	out2 := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(out2, func(name string, _ os.DirEntry, _ error) error { return os.Chmod(name, 0o700) })
	})
	if err := bundle.UnpackCheckout(ctx, archive, out2, cfg); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	err = filepath.WalkDir(out2, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out2, path)
		content, err := os.ReadFile(path)
		got[filepath.ToSlash(rel)] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"head/keep.txt":      "keep\n",
		"head/unrelated.txt": "not in the diff\n",
		"head/added.txt":     "added\n",
		"head/.gitmodules":   "[submodule \"sub\"]\n\tpath = sub\n\turl = https://example.com/sub.git\n",
		"merge-base/del.txt": "deleted by the pull request\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checkout = %q\nwant %q", got, want)
	}
	for name := range got {
		for _, part := range strings.Split(name, "/") {
			if strings.EqualFold(part, ".git") {
				t.Errorf("checkout holds git metadata: %s", name)
			}
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, dir); after != before {
		t.Fatalf("clone changed:\n%s\n=== after ===\n%s", before, after)
	}
}

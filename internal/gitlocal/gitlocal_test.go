package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/bundle"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

// git runs git in dir for test setup and returns trimmed stdout.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func lines(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s line %d\n", prefix, i)
	}
	return b.String()
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", message)
	return git(t, dir, "rev-parse", "HEAD")
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

// fixture builds a base commit and a head commit that exercise every change
// kind, on branch "feature" of a repository whose HEAD stays on main.
type fixture struct {
	dir  string
	base string
	head string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	write(t, dir, "mod.txt", lines("mod", 10), 0o644)
	write(t, dir, "rename_only.txt", lines("rename only", 12), 0o644)
	write(t, dir, "rename_edit.txt", lines("rename edit", 12), 0o644)
	write(t, dir, "bin.dat", "before\x00\x01\x02", 0o644)
	write(t, dir, "mode.sh", "#!/bin/sh\necho hi\n", 0o644)
	write(t, dir, "del.txt", lines("del", 3), 0o644)
	write(t, dir, "keep.txt", "keep\n", 0o644)
	base := commit(t, dir, "base")

	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "mod.txt", strings.Replace(lines("mod", 10), "mod line 4\n", "mod line four\nextra\n", 1), 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "mv", "rename_only.txt", "dir/renamed_only.txt")
	git(t, dir, "mv", "rename_edit.txt", "renamed_edit.txt")
	write(t, dir, "renamed_edit.txt", strings.Replace(lines("rename edit", 12), "rename edit line 3\n", "changed\n", 1), 0o644)
	write(t, dir, "bin.dat", "after\x00\x03\x04\x05", 0o644)
	if err := os.Chmod(filepath.Join(dir, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "rm", "-q", "del.txt")
	write(t, dir, "new.txt", "new\nfile\n", 0o644)
	head := commit(t, dir, "head")
	git(t, dir, "checkout", "-q", "main")
	return fixture{dir: dir, base: base, head: head}
}

func open(t *testing.T, dir string, opts Options) *Repo {
	t.Helper()
	r, err := Open(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func TestCompare(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	got, err := r.Compare(context.Background(), f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseOID != f.base || got.MergeBaseOID != f.base || got.HeadOID != f.head {
		t.Fatalf("oids = %+v", got)
	}
	type want struct {
		status, oldPath, newPath, oldMode, newMode string
		add, del                                   int64 // -1: binary, no counts
	}
	wants := map[string]want{
		"mod.txt":              {"modified", "mod.txt", "mod.txt", "100644", "100644", 2, 1},
		"dir/renamed_only.txt": {"renamed", "rename_only.txt", "dir/renamed_only.txt", "100644", "100644", 0, 0},
		"renamed_edit.txt":     {"renamed", "rename_edit.txt", "renamed_edit.txt", "100644", "100644", 1, 1},
		"bin.dat":              {"modified", "bin.dat", "bin.dat", "100644", "100644", -1, -1},
		"mode.sh":              {"modified", "mode.sh", "mode.sh", "100644", "100755", 0, 0},
		"del.txt":              {"deleted", "del.txt", "", "100644", "000000", 0, 3},
		"new.txt":              {"added", "", "new.txt", "000000", "100644", 2, 0},
	}
	if len(got.Files) != len(wants) {
		t.Fatalf("got %d files, want %d: %+v", len(got.Files), len(wants), got.Files)
	}
	for _, file := range got.Files {
		key := file.NewPath
		if key == "" {
			key = file.OldPath
		}
		w, ok := wants[key]
		if !ok {
			t.Fatalf("unexpected file %+v", file)
		}
		if file.Status != w.status || file.OldPath != w.oldPath || file.NewPath != w.newPath || file.OldMode != w.oldMode || file.NewMode != w.newMode {
			t.Errorf("%s = %+v, want %+v", key, file, w)
		}
		if w.add < 0 {
			if file.Additions != nil || file.Deletions != nil {
				t.Errorf("%s: binary file has counts", key)
			}
		} else if file.Additions == nil || file.Deletions == nil || *file.Additions != w.add || *file.Deletions != w.del {
			t.Errorf("%s counts = %v/%v, want %d/%d", key, file.Additions, file.Deletions, w.add, w.del)
		}
		if file.NewPath != "" {
			if oid := git(t, f.dir, "rev-parse", f.head+":"+file.NewPath); oid != file.NewOID {
				t.Errorf("%s new oid = %s, want %s", key, file.NewOID, oid)
			}
		}
		if len(file.PatchHash) != 64 {
			t.Errorf("%s patch hash = %q", key, file.PatchHash)
		}
	}
	// Files come in path order, as git lists them.
	for i := 1; i < len(got.Files); i++ {
		if effective(got.Files[i-1]) > effective(got.Files[i]) {
			t.Errorf("files out of order at %d", i)
		}
	}
}

func effective(f bundle.FileChange) string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

func TestCompareOutputLimitFallsBackToObjectHash(t *testing.T) {
	f := newFixture(t)
	full, err := open(t, f.dir, Options{}).Compare(context.Background(), f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	// Large enough for the raw and numstat listings, too small for the full
	// patch, as the hosted comparison does when its patch read is oversize.
	listing := max(
		len(git(t, f.dir, "diff-tree", "-r", "--no-commit-id", "--raw", "-z", "-M", "-l1000", f.base, f.head)),
		len(git(t, f.dir, "diff-tree", "-r", "--no-commit-id", "--numstat", "-z", "-M", "-l1000", f.base, f.head)),
	) + 1
	if patch := git(t, f.dir, "diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", f.base, f.head); len(patch) <= listing {
		t.Fatalf("full patch (%d bytes) fits the listing limit %d", len(patch), listing)
	}
	r := open(t, f.dir, Options{MaxComparisonBytes: int64(listing)})
	small, err := r.Compare(context.Background(), f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range small.Files {
		if file.PatchHash != objectPatchHash(file) {
			t.Errorf("%s: patch hash is not the object hash", file.NewPath)
		}
		if file.Additions != nil && file.PatchHash == full.Files[i].PatchHash {
			t.Errorf("%s: fallback hash equals the patch hash", file.NewPath)
		}
	}
	tiny := open(t, f.dir, Options{MaxComparisonBytes: 10})
	if _, err := tiny.Compare(context.Background(), f.base, f.head); !errors.Is(err, bundle.ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
}

func TestCompareUsesMergeBase(t *testing.T) {
	f := newFixture(t)
	// main moves on after the branch point.
	write(t, f.dir, "main_only.txt", "x\n", 0o644)
	newBase := commit(t, f.dir, "main moves")
	r := open(t, f.dir, Options{})
	got, err := r.Compare(context.Background(), newBase, f.head)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeBaseOID != f.base || got.BaseOID != f.base {
		t.Fatalf("merge base = %s, want %s", got.MergeBaseOID, f.base)
	}
	for _, file := range got.Files {
		if file.NewPath == "main_only.txt" {
			t.Fatal("base-only change leaked into the comparison")
		}
	}
}

func TestPatchFetcherAndDiffIndex(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	cmp, err := r.Compare(ctx, f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	hunks, err := bundle.BuildDiffIndex(cmp, r.PatchFetcher(ctx, cmp), bundle.RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ path, change, reason string }
	var got []key
	for _, h := range hunks {
		got = append(got, key{h.Path, h.Change, h.TextlessReason})
	}
	want := []key{
		{"bin.dat", "modified", "binary"},
		{"del.txt", "removed", ""},
		{"dir/renamed_only.txt", "renamed", "rename_only"},
		{"mod.txt", "modified", ""},
		{"mode.sh", "modified", "mode_change"},
		{"new.txt", "added", ""},
		{"renamed_edit.txt", "renamed", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("hunks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hunk %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, h := range hunks {
		switch h.Path {
		case "renamed_edit.txt":
			if h.OldPath != "rename_edit.txt" || h.Textless || h.Additions != 1 || h.Deletions != 1 {
				t.Errorf("renamed edit = %+v", h)
			}
		case "bin.dat", "dir/renamed_only.txt", "mode.sh":
			if !h.Textless {
				t.Errorf("%s should be textless", h.Path)
			}
		}
	}
	// The patch of the binary file is git's one-line notice, not data.
	patch, err := r.PatchFetcher(ctx, cmp)("bin.dat")
	if err != nil || !strings.Contains(string(patch), "Binary files ") || strings.Contains(string(patch), "GIT binary patch") {
		t.Fatalf("binary patch = %q, %v", patch, err)
	}
}

func TestPatchFetcherLimitsAndUnreadable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cmp, err := open(t, f.dir, Options{}).Compare(ctx, f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		opts    Options
		cmp     bundle.Comparison
		paths   []string
		wantErr error
		reason  string
	}{
		{"over limit", Options{MaxPatchBytes: 20}, cmp, []string{"mod.txt"}, bundle.ErrOutputTooLarge, "too_large"},
		{"unknown revision", Options{}, bundle.Comparison{BaseOID: strings.Repeat("a", 40), HeadOID: f.head}, []string{"mod.txt"}, bundle.ErrPatchUnreadable, "unreadable"},
		{"invalid path", Options{}, cmp, []string{"../x"}, ErrInvalidInput, ""},
		{"no path", Options{}, cmp, nil, ErrInvalidInput, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := open(t, f.dir, tt.opts).PatchFetcher(ctx, tt.cmp)(tt.paths...)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
	// Through BuildDiffIndex the over-limit and unreadable files are textless
	// rather than failing the build.
	small := open(t, f.dir, Options{MaxPatchBytes: 20})
	hunks, notes, err := bundle.BuildDiffIndexWithin(cmp, small.PatchFetcher(ctx, cmp), bundle.RoleForPath, bundle.DiffLimits{})
	if err != nil || len(notes) == 0 {
		t.Fatalf("notes = %v, err = %v", notes, err)
	}
	for _, h := range hunks {
		if h.Path == "mod.txt" && !h.Textless {
			t.Error("over-limit file kept its patch")
		}
	}
}

func TestReadBlob(t *testing.T) {
	f := newFixture(t)
	write(t, f.dir, "REVIEW.md", "# Review\n", 0o644)
	if err := os.Symlink("REVIEW.md", filepath.Join(f.dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	rev := commit(t, f.dir, "add review")
	r := open(t, f.dir, Options{MaxBlobBytes: 64})
	ctx := context.Background()
	got, err := r.ReadBlob(ctx, rev, "REVIEW.md")
	if err != nil || string(got.Content) != "# Review\n" || got.Mode != "100644" {
		t.Fatalf("blob = %+v, %v", got, err)
	}
	// A symbolic link reads as its target, as the hosted mirror reads it.
	got, err = r.ReadBlob(ctx, rev, "link.md")
	if err != nil || string(got.Content) != "REVIEW.md" || got.Mode != "120000" {
		t.Fatalf("link = %+v, %v", got, err)
	}
	write(t, f.dir, "big.md", strings.Repeat("x", 200), 0o644)
	big := commit(t, f.dir, "big")
	tests := []struct {
		name, rev, path string
		want            error
	}{
		{"missing at revision", f.base, "REVIEW.md", ErrObjectNotFound},
		{"directory", rev, "dir", ErrObjectNotFound},
		{"over limit", big, "big.md", bundle.ErrOutputTooLarge},
		{"bad path", rev, "../REVIEW.md", ErrInvalidInput},
		{"not an oid", "main", "REVIEW.md", ErrInvalidInput},
		{"unknown commit", strings.Repeat("b", 40), "REVIEW.md", ErrObjectNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := r.ReadBlob(ctx, tt.rev, tt.path); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	t.Run("timeout is not a missing file", func(t *testing.T) {
		slow := open(t, f.dir, Options{})
		slow.opts.CommandTimeout = time.Nanosecond
		_, err := slow.ReadBlob(ctx, rev, "REVIEW.md")
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrObjectNotFound) {
			t.Fatalf("err = %v, want a deadline error", err)
		}
	})
}

// gitDir is the clone's common git directory.
func gitDir(t *testing.T, dir string) string {
	t.Helper()
	path := git(t, dir, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path
}

// tree lists every file under dir with its content, or "" for a missing
// directory.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
			return nil
		}
		content, err := os.ReadFile(path)
		fmt.Fprintf(&b, "%s: %q %v\n", rel, content, err)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "<absent>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// snapshot captures everything an operation must not change in the clone.
// Fetched refs under refs/baum/ and fetched objects are allowed to change.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	common := gitDir(t, dir)
	refs := git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/remotes", "refs/replace")
	return strings.Join([]string{
		git(t, dir, "rev-parse", "HEAD"),
		git(t, dir, "symbolic-ref", "HEAD"),
		refs,
		git(t, dir, "status", "--porcelain=v2", "--branch", "-uall"),
		git(t, dir, "diff", "--no-ext-diff", "HEAD"),
		git(t, dir, "ls-files", "--stage"),
		"config: " + readFile(t, filepath.Join(common, "config")),
		"packed-refs: " + readFile(t, filepath.Join(common, "packed-refs")),
		"FETCH_HEAD: " + readFile(t, filepath.Join(common, "FETCH_HEAD")),
		"shallow: " + readFile(t, filepath.Join(common, "shallow")),
		"worktrees:\n" + tree(t, filepath.Join(common, "worktrees")),
		"info:\n" + tree(t, filepath.Join(common, "info")),
		"hooks:\n" + tree(t, filepath.Join(common, "hooks")),
	}, "\n--\n")
}

func TestOperationsLeaveTheCloneUntouched(t *testing.T) {
	f := newFixture(t)
	// A worktree of the user's that has since been moved is a stale
	// registration that a prune would delete.
	mine := filepath.Join(t.TempDir(), "mine")
	git(t, f.dir, "worktree", "add", "-q", "--detach", mine, f.base)
	if err := os.Rename(mine, filepath.Join(t.TempDir(), "moved")); err != nil {
		t.Fatal(err)
	}
	if git(t, f.dir, "worktree", "prune", "--dry-run") == "" {
		t.Fatal("moved worktree is not a stale registration")
	}
	git(t, f.dir, "checkout", "-q", "-b", "wip", f.base)
	write(t, f.dir, "mod.txt", "dirty edit\n", 0o644)
	write(t, f.dir, "untracked.txt", "u\n", 0o644)
	write(t, f.dir, "staged.txt", "s\n", 0o644)
	git(t, f.dir, "add", "staged.txt")
	before := snapshot(t, f.dir)

	temp := t.TempDir()
	r := open(t, f.dir, Options{TempDir: temp})
	ctx := context.Background()
	pull, err := r.ResolvePull(ctx, PullRequest{Number: 1, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := r.Compare(ctx, pull.BaseOID, pull.HeadOID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.BuildDiffIndex(cmp, r.PatchFetcher(ctx, cmp), bundle.RoleForPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadBlob(ctx, f.head, "new.txt"); err != nil {
		t.Fatal(err)
	}
	if got := tarFiles(t, bytes.NewReader(whole1(t, r, f.head)))["new.txt"]; got != "new\nfile\n" {
		t.Fatalf("export content = %q", got)
	}
	rc, err := r.ExportCommitPath(ctx, f.head, "new.txt")
	if got := tarFiles(t, bytes.NewReader(readAll(t, rc, err))); len(got) != 1 {
		t.Fatalf("path export = %q", got)
	}
	if after := snapshot(t, f.dir); after != before {
		t.Fatalf("clone changed while exporting:\n%s\n=== after ===\n%s", before, after)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(temp); len(entries) != 0 {
		t.Fatalf("temp dir not empty: %v", entries)
	}
	if after := snapshot(t, f.dir); after != before {
		t.Fatalf("clone changed:\n%s\n=== after ===\n%s", before, after)
	}
}

// remoteFixture is a "remote" whose pull request head is on no branch, and a
// clone made before the base and head existed.
type remoteFixture struct {
	remote, clone string
	base, head    string
}

func newRemoteFixture(t *testing.T) remoteFixture {
	t.Helper()
	requireGit(t)
	remote := initRepo(t)
	// Protocol v0 serves an unadvertised commit only with this set.
	git(t, remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	write(t, remote, "a.txt", lines("a", 5), 0o644)
	commit(t, remote, "initial")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, remote, "clone", "-q", remote, clone)

	write(t, remote, "b.txt", "base\n", 0o644)
	base := commit(t, remote, "base moves")
	git(t, remote, "checkout", "-q", "-b", "pr")
	write(t, remote, "a.txt", lines("a", 5)+"added\n", 0o644)
	head := commit(t, remote, "pr change")
	git(t, remote, "update-ref", "refs/pull/7/head", head)
	git(t, remote, "checkout", "-q", "main")
	git(t, remote, "branch", "-q", "-D", "pr")
	return remoteFixture{remote: remote, clone: clone, base: base, head: head}
}

func TestResolvePullFetchesMissingCommits(t *testing.T) {
	tests := []struct {
		name       string
		remoteName string
		prepare    func(t *testing.T, f remoteFixture)
		pr         func(f remoteFixture) PullRequest
		wantRefs   []string
		wantErr    error
	}{
		{
			name: "fetches base and head into the private namespace", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}
			},
			wantRefs: []string{"refs/baum/pull/7/base", "refs/baum/pull/7/head"},
		},
		{
			name: "custom remote name", remoteName: "upstream",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "refs/heads/main"}
			},
			wantRefs: []string{"refs/baum/pull/7/base", "refs/baum/pull/7/head"},
		},
		{
			name: "present commits make no fetch", remoteName: "origin",
			prepare: func(t *testing.T, f remoteFixture) {
				git(t, f.clone, "fetch", "-q", "origin", "main")
				git(t, f.clone, "fetch", "-q", "origin", "refs/pull/7/head")
				git(t, f.clone, "remote", "set-url", "origin", "/nonexistent")
			},
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head}
			},
		},
		{
			name: "missing base without a base ref", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head}
			},
			wantErr: ErrInvalidInput,
		},
		{
			name: "pull ref that does not exist", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 8, BaseSHA: f.base, HeadSHA: strings.Repeat("e", 40), BaseRef: "main"}
			},
			wantErr: &Error{},
		},
		{
			name: "missing pull ref falls back to the commit ID", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 8, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}
			},
			wantRefs: []string{"refs/baum/pull/8/base", "refs/baum/pull/8/head"},
		},
		{
			name: "head moved after it was recorded", remoteName: "origin",
			prepare: func(t *testing.T, f remoteFixture) {
				// A force push: the pull ref now names a sibling commit, so
				// fetching it does not bring the recorded head.
				git(t, f.remote, "checkout", "-q", "--detach", f.base)
				write(t, f.remote, "a.txt", "rewritten\n", 0o644)
				git(t, f.remote, "update-ref", "refs/pull/7/head", commit(t, f.remote, "force pushed"))
				git(t, f.remote, "checkout", "-q", "main")
			},
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}
			},
			wantRefs: []string{"refs/baum/pull/7/base", "refs/baum/pull/7/head"},
		},
		{
			name: "head the remote never had", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: strings.Repeat("d", 40), BaseRef: "main"}
			},
			wantErr: ErrObjectNotFound,
		},
		{
			name: "invalid sha", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: "main", HeadSHA: f.head, BaseRef: "main"}
			},
			wantErr: ErrInvalidInput,
		},
		{
			name: "hostile base ref", remoteName: "origin",
			pr: func(f remoteFixture) PullRequest {
				return PullRequest{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "--upload-pack=x"}
			},
			wantErr: ErrInvalidInput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			if tt.remoteName != "origin" {
				git(t, f.clone, "remote", "rename", "origin", tt.remoteName)
			}
			if tt.prepare != nil {
				tt.prepare(t, f)
			}
			// Dirty the clone first to prove it stays as it was, and install
			// a hook a fetch would run if hooks were enabled.
			write(t, f.clone, "a.txt", "dirty\n", 0o644)
			write(t, f.clone, "scratch.txt", "x\n", 0o644)
			marker := filepath.Join(t.TempDir(), "hook-ran")
			write(t, filepath.Join(f.clone, ".git", "hooks"), "reference-transaction", "#!/bin/sh\ntouch "+marker+"\n", 0o755)
			before := snapshot(t, f.clone)

			r := open(t, f.clone, Options{Remote: tt.remoteName})
			pull, err := r.ResolvePull(context.Background(), tt.pr(f))
			switch want := tt.wantErr.(type) {
			case nil:
				if err != nil {
					t.Fatal(err)
				}
			case *Error:
				var ge *Error
				if !errors.As(err, &ge) {
					t.Fatalf("err = %v, want a git error", err)
				}
			default:
				if !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v", err, want)
				}
			}
			if tt.wantErr == nil {
				if pull.BaseOID != f.base || pull.HeadOID != f.head || pull.MergeBaseOID == "" {
					t.Fatalf("pull = %+v", pull)
				}
				// The merge base of base and head is base: the head branches
				// from it.
				if pull.MergeBaseOID != f.base {
					t.Fatalf("merge base = %s, want %s", pull.MergeBaseOID, f.base)
				}
				cmp, err := r.Compare(context.Background(), pull.BaseOID, pull.HeadOID)
				if err != nil || len(cmp.Files) != 1 || cmp.Files[0].NewPath != "a.txt" {
					t.Fatalf("comparison = %+v, %v", cmp, err)
				}
			}
			for _, ref := range tt.wantRefs {
				want := f.base
				if strings.HasSuffix(ref, "/head") {
					want = f.head
				}
				if got := git(t, f.clone, "rev-parse", "--verify", ref); got != want {
					t.Errorf("%s = %s, want %s", ref, got, want)
				}
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the fetch ran a hook")
			}
			if tt.wantRefs == nil && tt.wantErr == nil {
				if refs := git(t, f.clone, "for-each-ref", "refs/baum"); refs != "" {
					t.Errorf("unexpected private refs: %s", refs)
				}
			}
			if after := snapshot(t, f.clone); after != before {
				t.Fatalf("clone changed:\n%s\n=== after ===\n%s", before, after)
			}
		})
	}
}

func TestErrorsDoNotLeakCredentials(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	write(t, dir, "a", "a\n", 0o644)
	commit(t, dir, "c")
	git(t, dir, "remote", "add", "origin", "https://user:s3cr3t-token@127.0.0.1:1/x.git")
	r := open(t, dir, Options{})
	_, err := r.ResolvePull(context.Background(), PullRequest{Number: 1, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), BaseRef: "main"})
	if err == nil {
		t.Fatal("expected a fetch error")
	}
	if strings.Contains(err.Error(), "s3cr3t-token") {
		t.Fatalf("error leaks the token: %v", err)
	}
	if len(err.Error()) > 2048 {
		t.Fatalf("error is %d bytes", len(err.Error()))
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"url credentials", "fatal: unable to access 'https://x-access-token:abc@github.com/o/r.git/'", "fatal: unable to access 'https://***@github.com/o/r.git/'"},
		{"control characters", "a\x1b[31mb\x00c\nd", "a[31mbc d"},
		{"bounded", strings.Repeat("x", 2000), strings.Repeat("x", maxStderrSnippet) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitize([]byte(tt.in)); got != tt.want {
				t.Errorf("sanitize = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunTimeoutAndHermeticEnv(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	t.Setenv("GIT_PAGER", "/definitely/not/a/pager")
	t.Setenv("GIT_DIR", "/nonexistent")
	r := open(t, dir, Options{})
	if r.Dir() == "" {
		t.Fatal("no dir")
	}
	_, err := run(context.Background(), runSpec{dir: dir, timeout: 1, env: userEnv()}, "status")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}

func TestClosedRepoRefusesOperations(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err := r.Compare(ctx, f.base, f.head)
	if !errors.Is(err, ErrClosed) || errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Compare err = %v, want ErrClosed", err)
	}
	if _, err := r.ReadBlob(ctx, f.head, "new.txt"); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadBlob err = %v, want ErrClosed", err)
	}
	if _, err := r.TextPatch(ctx, f.base, f.head, "new.txt"); !errors.Is(err, ErrClosed) {
		t.Errorf("TextPatch err = %v, want ErrClosed", err)
	}
	if _, err := r.ResolvePull(ctx, PullRequest{Number: 1, BaseSHA: f.base, HeadSHA: f.head}); !errors.Is(err, ErrClosed) {
		t.Errorf("ResolvePull err = %v, want ErrClosed", err)
	}
	if _, err := r.ExportCommit(ctx, f.head); !errors.Is(err, ErrClosed) {
		t.Errorf("ExportCommit err = %v, want ErrClosed", err)
	}
	if _, err := r.ExportCommitPath(ctx, f.head, "new.txt"); !errors.Is(err, ErrClosed) {
		t.Errorf("ExportCommitPath err = %v, want ErrClosed", err)
	}
}

func TestSupportedGitVersion(t *testing.T) {
	tests := []struct {
		version string
		ok      bool
	}{
		{"git version 2.29.0", true},
		{"git version 2.39.5 (Apple Git-154)", true},
		{"git version 2.45.1.windows.1", true},
		{"git version 3.0.0", true},
		{"git version 2.28.1", false},
		{"git version 1.9.5", false},
		{"something else", true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if err := supportedGitVersion(tt.version); (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

// TestCloseWaitsForWholeOperation: Close called between the git commands of
// one Compare lets that Compare finish instead of failing with ErrClosed.
func TestCloseWaitsForWholeOperation(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	reached := make(chan struct{})
	release := make(chan struct{})
	afterMergeBase = func() {
		close(reached)
		<-release
	}
	t.Cleanup(func() { afterMergeBase = nil })
	type result struct {
		cmp bundle.Comparison
		err error
	}
	compared := make(chan result, 1)
	go func() {
		cmp, err := r.Compare(context.Background(), f.base, f.head)
		compared <- result{cmp, err}
	}()
	<-reached
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during Compare: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	res := <-compared
	if res.err != nil || len(res.cmp.Files) == 0 {
		t.Fatalf("Compare = %d files, %v", len(res.cmp.Files), res.err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

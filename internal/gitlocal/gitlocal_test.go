package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/bundle"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
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
	r := open(t, f.dir, Options{MaxComparisonBytes: 1100})
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

func TestWorkingTreeAttributesDoNotHideText(t *testing.T) {
	f := newFixture(t)
	// A -diff attribute in the clone's working tree would make git call
	// every .txt file binary; a bare mirror never sees it.
	write(t, f.dir, ".gitattributes", "*.txt -diff\n", 0o644)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	cmp, err := r.Compare(ctx, f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range cmp.Files {
		if file.NewPath == "mod.txt" && (file.Additions == nil || *file.Additions != 2) {
			t.Fatalf("mod.txt counts hidden by attributes: %+v", file)
		}
	}
	patch, err := r.TextPatch(ctx, cmp.BaseOID, cmp.HeadOID, "mod.txt")
	if err != nil || !strings.Contains(string(patch), "+extra") {
		t.Fatalf("patch = %q, %v", patch, err)
	}
}

func TestLocalConfigDoesNotChangePatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := open(t, f.dir, Options{})
	cmp, err := r.Compare(ctx, f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.TextPatch(ctx, cmp.BaseOID, cmp.HeadOID, "mod.txt")
	if err != nil {
		t.Fatal(err)
	}
	git(t, f.dir, "config", "diff.noprefix", "true")
	git(t, f.dir, "config", "diff.context", "9")
	git(t, f.dir, "config", "diff.algorithm", "patience")
	git(t, f.dir, "config", "color.ui", "always")
	after, err := r.TextPatch(ctx, cmp.BaseOID, cmp.HeadOID, "mod.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !strings.Contains(string(after), "diff --git a/mod.txt b/mod.txt") {
		t.Fatalf("patch changed with local config:\n%s\n---\n%s", before, after)
	}
}

func TestReadBlob(t *testing.T) {
	f := newFixture(t)
	write(t, f.dir, "REVIEW.md", "# Review\n", 0o644)
	rev := commit(t, f.dir, "add review")
	r := open(t, f.dir, Options{MaxBlobBytes: 64})
	ctx := context.Background()
	got, err := r.ReadBlob(ctx, rev, "REVIEW.md")
	if err != nil || string(got.Content) != "# Review\n" || got.Mode != "100644" {
		t.Fatalf("blob = %+v, %v", got, err)
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
}

// snapshot captures everything a read-only operation must not change.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	refs := git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/remotes")
	return strings.Join([]string{
		git(t, dir, "rev-parse", "HEAD"),
		git(t, dir, "symbolic-ref", "HEAD"),
		refs,
		git(t, dir, "status", "--porcelain=v2", "--branch", "-uall"),
		git(t, dir, "diff", "--no-ext-diff", "HEAD"),
		git(t, dir, "ls-files", "--stage"),
	}, "\n--\n")
}

func worktreeCount(t *testing.T, dir string) int {
	t.Helper()
	return strings.Count(git(t, dir, "worktree", "list", "--porcelain"), "worktree ")
}

func TestOperationsLeaveTheCloneUntouched(t *testing.T) {
	f := newFixture(t)
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
	wt, err := r.AddWorktree(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(wt.Dir, "new.txt")); string(got) != "new\nfile\n" {
		t.Fatalf("worktree content = %q", got)
	}
	if inside(f.dir, wt.Dir) {
		t.Fatalf("worktree %s is inside the clone", wt.Dir)
	}
	if worktreeCount(t, f.dir) != 2 {
		t.Fatal("worktree not registered while open")
	}
	if err := wt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir remains: %v", err)
	}
	if entries, _ := os.ReadDir(temp); len(entries) != 0 {
		t.Fatalf("temp dir not empty: %v", entries)
	}
	if worktreeCount(t, f.dir) != 1 {
		t.Fatal("worktree still registered after Close")
	}
	if after := snapshot(t, f.dir); after != before {
		t.Fatalf("clone changed:\n%s\n=== after ===\n%s", before, after)
	}
}

func TestWorktreeCleanedUpOnFailure(t *testing.T) {
	f := newFixture(t)
	temp := t.TempDir()
	r := open(t, f.dir, Options{TempDir: temp})
	_, err := r.AddWorktree(context.Background(), strings.Repeat("c", 40))
	if err == nil {
		t.Fatal("expected an error for an unknown commit")
	}
	if entries, _ := os.ReadDir(temp); len(entries) != 0 {
		t.Fatalf("temp dir not empty after failure: %v", entries)
	}
	if worktreeCount(t, f.dir) != 1 {
		t.Fatal("failed add left a registration")
	}
	// A cancelled context still cleans up a worktree that was created.
	ctx, cancel := context.WithCancel(context.Background())
	wt, err := r.AddWorktree(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := wt.Close(); err != nil {
		t.Fatal(err)
	}
	if worktreeCount(t, f.dir) != 1 {
		t.Fatal("registration left after Close with cancelled context")
	}
	// A worktree directory inside the clone is refused.
	inner := open(t, f.dir, Options{TempDir: filepath.Join(f.dir, "sub")})
	if err := os.MkdirAll(filepath.Join(f.dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.AddWorktree(context.Background(), f.head); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestWorktreeSkipsHooks(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(f.dir, ".git", "hooks", "post-checkout")
	write(t, filepath.Dir(hook), "post-checkout", "#!/bin/sh\ntouch "+marker+"\n", 0o755)
	r := open(t, f.dir, Options{})
	wt, err := r.AddWorktree(context.Background(), f.head)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wt.Close() })
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("post-checkout hook ran")
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
				return PullRequest{Number: 8, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}
			},
			wantErr: &Error{},
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
			// Dirty the clone first to prove it stays as it was.
			write(t, f.clone, "a.txt", "dirty\n", 0o644)
			write(t, f.clone, "scratch.txt", "x\n", 0o644)
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
				git(t, f.clone, "rev-parse", "--verify", ref)
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
	_, err := run(context.Background(), runSpec{dir: dir, timeout: 1, mode: envHermetic}, "status")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}

package gitlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fork's worktree on baum's branch: its commits are only under refs/baum/.
func addForkWorktree(t *testing.T, f fixFixture) (string, *Repo) {
	t.Helper()
	git(t, f.remote, "update-ref", "refs/pull/9/head", f.head)
	dir := filepath.Join(t.TempDir(), "worktrees", "one", "repo")
	repo := open(t, f.clone, Options{})
	added, err := repo.AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 9, Fork: true, Suffix: "abc"})
	if err != nil || added.Branch != "baum/pr-9" {
		t.Fatalf("added %+v %v", added, err)
	}
	return dir, repo
}

// Changes counts every changed, staged and untracked path, and names them.
func TestWorktreeChanges(t *testing.T) {
	f := newFixFixture(t)
	dir, _ := addForkWorktree(t, f)
	ctx := context.Background()
	if got, err := WorktreeChanges(ctx, dir); err != nil || got.Count != 0 || len(got.Paths) != 0 {
		t.Fatalf("a clean worktree: %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "deep", "er"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deep", "er", "file.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := WorktreeChanges(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Paths, ",")
	if got.Count != 3 || !strings.Contains(joined, "new.txt") || !strings.Contains(joined, "deep/er/file.txt") || strings.Contains(joined, "ignored.txt") {
		t.Fatalf("changes %+v", got)
	}
}

// Commits at the fetched head are not unpushed; one made on top is, until a
// remote-tracking ref has it.
func TestUnpushedCommits(t *testing.T) {
	f := newFixFixture(t)
	dir, repo := addForkWorktree(t, f)
	ctx := context.Background()
	if n, err := repo.UnpushedCommits(ctx, "baum/pr-9"); err != nil || n != 0 {
		t.Fatalf("at the fetched head: %d %v", n, err)
	}
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "local")
	if n, err := repo.UnpushedCommits(ctx, "baum/pr-9"); err != nil || n != 1 {
		t.Fatalf("one local commit: %d %v", n, err)
	}
	git(t, f.clone, "update-ref", "refs/remotes/origin/pushed", git(t, dir, "rev-parse", "HEAD"))
	if n, err := repo.UnpushedCommits(ctx, "baum/pr-9"); err != nil || n != 0 {
		t.Fatalf("pushed: %d %v", n, err)
	}
	if n, err := repo.UnpushedCommits(ctx, "no-such-branch"); err != nil || n != 0 {
		t.Fatalf("no branch: %d %v", n, err)
	}
}

// RemoveWorktree removes a dirty worktree, DeleteBranch its branch; a
// worktree whose directory is gone is pruned.
func TestRemoveWorktreeAndDeleteBranch(t *testing.T) {
	f := newFixFixture(t)
	dir, repo := addForkWorktree(t, f)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteBranch(ctx, "baum/pr-9"); err == nil {
		t.Fatal("a checked-out branch was deleted")
	}
	if err := repo.RemoveWorktree(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory stays: %v", err)
	}
	if err := repo.DeleteBranch(ctx, "baum/pr-9"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.clone, "branch", "--list", "baum/pr-9"); got != "" {
		t.Fatalf("the branch stays: %q", got)
	}
	if err := repo.DeleteBranch(ctx, "baum/pr-9"); err != nil {
		t.Fatalf("a branch already gone: %v", err)
	}

	// A worktree whose directory is gone loses only its own entry: another
	// one whose directory is missing too (on a drive not mounted) stays.
	other := filepath.Join(t.TempDir(), "elsewhere")
	git(t, f.clone, "worktree", "add", "-q", "--detach", other)
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	gone, _ := addForkWorktree(t, f)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveWorktree(ctx, gone); err != nil {
		t.Fatal(err)
	}
	list := git(t, f.clone, "worktree", "list", "--porcelain")
	if strings.Count(list, "worktree ") != 2 || strings.Contains(list, gone) || !strings.Contains(list, "elsewhere") {
		t.Fatalf("worktrees left: %s", list)
	}
}

// Commits only a detached HEAD holds are work; a worktree whose .git file is
// gone is an error, never the status of a repository above it.
func TestWorktreeChangesCountsDetachedCommitsAndStaysInTheWorktree(t *testing.T) {
	f := newFixFixture(t)
	dir, _ := addForkWorktree(t, f)
	ctx := context.Background()
	git(t, dir, "checkout", "-q", "--detach")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "detached")
	if got, err := WorktreeChanges(ctx, dir); err != nil || got.Detached != 1 || got.Count != 0 {
		t.Fatalf("detached: %+v %v", got, err)
	}
	git(t, dir, "checkout", "-q", "baum/pr-9")
	git(t, dir, "checkout", "-q", "--detach", "HEAD")
	if got, err := WorktreeChanges(ctx, dir); err != nil || got.Detached != 0 {
		t.Fatalf("detached at the branch: %+v %v", got, err)
	}

	// A repository above the worktree, which would call it clean.
	above := t.TempDir()
	git(t, above, "init", "-q")
	inner := filepath.Join(above, "repo")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(above, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WorktreeChanges(ctx, inner); err == nil {
		t.Fatal("a directory with no repository of its own read as clean")
	}
}

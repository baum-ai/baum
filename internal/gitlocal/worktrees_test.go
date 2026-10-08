package gitlocal

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every worktree of the clone is listed the same way from any of them: the
// main one first, then the linked ones, a deleted one marked prunable.
func TestWorktreesFromEveryCheckout(t *testing.T) {
	requireGit(t)
	main := initRepo(t)
	if resolved, err := filepath.EvalSymlinks(main); err == nil {
		main = resolved
	}
	write(t, main, "a.txt", "a\n", 0o644)
	commit(t, main, "start")
	nested := filepath.Join(main, ".agents", "worktrees", "two words")
	sibling := filepath.Join(filepath.Dir(main), filepath.Base(main)+"-other")
	gone := filepath.Join(filepath.Dir(main), filepath.Base(main)+"-gone")
	git(t, main, "worktree", "add", "-q", "-b", "nested", nested)
	git(t, main, "worktree", "add", "-q", "-b", "sibling", sibling)
	git(t, main, "worktree", "add", "-q", "-b", "gone", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	want := []Worktree{
		{Path: main, Main: true},
		{Path: nested},
		{Path: sibling},
		{Path: gone, Prunable: true},
	}
	for _, from := range []string{main, nested, sibling, filepath.Join(main, ".agents")} {
		got, err := Worktrees(context.Background(), from)
		if err != nil {
			t.Fatalf("from %s: %v", from, err)
		}
		slices.SortStableFunc(got[1:], func(a, b Worktree) int { return order(want, a) - order(want, b) })
		if !slices.Equal(got, want) {
			t.Errorf("from %s:\n got %+v\nwant %+v", from, got, want)
		}
	}
}

func order(list []Worktree, w Worktree) int {
	return slices.IndexFunc(list, func(x Worktree) bool { return x.Path == w.Path })
}

// The caller's GIT_DIR does not redirect the listing to another repository.
func TestWorktreesIgnoresTheCallersGitDir(t *testing.T) {
	requireGit(t)
	main := initRepo(t)
	write(t, main, "a.txt", "a\n", 0o644)
	commit(t, main, "start")
	other := initRepo(t)
	write(t, other, "a.txt", "a\n", 0o644)
	commit(t, other, "start")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	got, err := Worktrees(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Main || filepath.Base(got[0].Path) != filepath.Base(main) {
		t.Errorf("worktrees = %+v, want only %s", got, main)
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /srv/repo.git\x00bare\x00\x00" +
		"worktree /srv/wt\x00HEAD 1111111111111111111111111111111111111111\x00branch refs/heads/a\x00\x00" +
		"worktree /srv/gone\x00HEAD 2222222222222222222222222222222222222222\x00detached\x00prunable gitdir file points to non-existent location\x00\x00" +
		"worktree /srv/locked\x00locked\x00\x00"
	got := parseWorktrees([]byte(out))
	want := []Worktree{
		{Path: filepath.FromSlash("/srv/wt")},
		{Path: filepath.FromSlash("/srv/gone"), Prunable: true},
		{Path: filepath.FromSlash("/srv/locked")},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := parseWorktrees(nil); len(got) != 0 {
		t.Errorf("empty output = %+v", got)
	}
}

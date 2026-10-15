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

// plant writes a worktree bookkeeping entry by hand, naming gitFile as the
// worktree's .git file, the way anyone who controls the clone could.
func plant(t *testing.T, common, name, gitFile string) {
	t.Helper()
	dir := filepath.Join(common, "worktrees", name)
	write(t, dir, "gitdir", gitFile+"\n", 0o644)
	write(t, dir, "HEAD", "ref: refs/heads/main\n", 0o644)
	write(t, dir, "commondir", "../..\n", 0o644)
}

// Entries planted in the clone's bookkeeping that would widen discovery to the
// home directory, the file system root, a directory above the clone or
// another project are ignored; genuine worktrees, a deleted one and one moved
// without git's knowledge included, are kept.
func TestWorktreesIgnorePlantedEntries(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	main := filepath.Join(base, "work", "demo")
	other := filepath.Join(base, "work", "other")
	impostor := filepath.Join(base, "work", "impostor")
	for _, dir := range []string{home, main, other, impostor} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, main, "init", "-q", "-b", "main")
	write(t, main, "a.txt", "a\n", 0o644)
	commit(t, main, "start")
	git(t, other, "init", "-q", "-b", "main")
	write(t, impostor, ".git", "gitdir: "+filepath.Join(other, ".git", "worktrees", "x")+"\n", 0o644)

	genuine := filepath.Join(base, "work", "demo-feature")
	gone := filepath.Join(base, "work", "demo-gone")
	moved := filepath.Join(base, "work", "demo-moved")
	git(t, main, "worktree", "add", "-q", "-b", "feature", genuine)
	git(t, main, "worktree", "add", "-q", "-b", "gone", gone)
	git(t, main, "worktree", "add", "-q", "-b", "moved", moved)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, moved+"-elsewhere"); err != nil {
		t.Fatal(err)
	}

	common := filepath.Join(main, ".git")
	plant(t, common, "home", filepath.Join(home, ".git"))
	plant(t, common, "root", filepath.Join(string(filepath.Separator), ".git"))
	plant(t, common, "above", filepath.Join(base, "work", ".git"))
	plant(t, common, "other", filepath.Join(other, ".git"))
	plant(t, common, "impostor", filepath.Join(impostor, ".git"))

	want := []string{main, genuine, gone, moved}
	slices.Sort(want)
	for _, from := range []string{main, genuine, moved + "-elsewhere"} {
		got, err := Worktrees(context.Background(), from)
		if err != nil {
			t.Fatalf("from %s: %v", from, err)
		}
		var paths []string
		for _, w := range got {
			paths = append(paths, w.Path)
		}
		slices.Sort(paths)
		if !slices.Equal(paths, want) {
			t.Errorf("from %s:\n got %v\nwant %v", from, paths, want)
		}
	}
}

func TestTrustedWorktreesRefusesBroadMainEntries(t *testing.T) {
	home := filepath.FromSlash("/srv/user")
	listed := []Worktree{{Path: filepath.FromSlash("/srv/user"), Main: true}}
	if got := trustedWorktrees(listed, filepath.FromSlash("/srv/user/.git"), home); len(got) != 0 {
		t.Errorf("home as the main worktree = %+v", got)
	}
	root := string(filepath.Separator)
	if got := trustedWorktrees([]Worktree{{Path: root, Main: true}}, filepath.Join(root, ".git"), home); len(got) != 0 {
		t.Errorf("the file system root as the main worktree = %+v", got)
	}
}

func TestCommonDirIsSharedAcrossLinkedWorktrees(t *testing.T) {
	requireGit(t)
	main := initRepo(t)
	write(t, main, "a.txt", "a\n", 0o644)
	commit(t, main, "start")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, main, "worktree", "add", "-q", "-b", "linked", linked)
	var common string
	for _, dir := range []string{main, linked} {
		repo, err := Open(context.Background(), dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := repo.Close(); err != nil {
				t.Error(err)
			}
		}()
		got, err := filepath.EvalSymlinks(repo.CommonDir())
		if err != nil {
			t.Fatal(err)
		}
		if common == "" {
			common = got
		} else if got != common {
			t.Fatalf("linked common dir %s differs from %s", got, common)
		}
	}
}

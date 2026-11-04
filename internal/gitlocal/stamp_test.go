package gitlocal

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func stampGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCheckoutStamp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	stampGit(t, repo, "init", "-q", "-b", "main")
	unborn, top, ok := CheckoutStamp(filepath.Join(repo, "pkg"))
	if !ok || top != repo {
		t.Fatalf("unborn: %q %q %v", unborn, top, ok)
	}
	stampGit(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	first, _, _ := CheckoutStamp(repo)
	if first == unborn {
		t.Error("a first commit did not change the stamp")
	}

	// An index rewrite ("git status" in an editor) changes nothing.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(repo, ".git", "index"), later, later); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if again, _, _ := CheckoutStamp(repo); again != first {
		t.Error("an index rewrite changed the stamp")
	}

	stampGit(t, repo, "commit", "-q", "--allow-empty", "-m", "two")
	second, _, _ := CheckoutStamp(repo)
	stampGit(t, repo, "remote", "add", "origin", "https://example.test/acme/repo.git")
	third, _, _ := CheckoutStamp(repo)
	stampGit(t, repo, "checkout", "-q", "--detach")
	detached, _, _ := CheckoutStamp(repo)
	stampGit(t, repo, "pack-refs", "--all")
	stampGit(t, repo, "checkout", "-q", "main")
	packed, _, _ := CheckoutStamp(repo)
	seen := map[string]string{}
	for name, stamp := range map[string]string{"first": first, "commit": second, "remote": third, "detached": detached, "packed": packed} {
		if other, dup := seen[stamp]; dup {
			t.Errorf("%s and %s have the same stamp", name, other)
		}
		seen[stamp] = name
	}

	// A linked worktree has its own HEAD.
	worktree := filepath.Join(root, "wt")
	stampGit(t, repo, "worktree", "add", "-q", "-b", "side", worktree)
	linked, top, ok := CheckoutStamp(worktree)
	if !ok || top != worktree || linked == packed {
		t.Errorf("worktree: %q %v, same as main = %v", top, ok, linked == packed)
	}

	if _, _, ok := CheckoutStamp(t.TempDir()); ok {
		t.Error("a directory outside any checkout has a stamp")
	}
}

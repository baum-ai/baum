package hookrefresh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/prlink"
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

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLocalGitReadsTheWorkingTreeTheHookRanIn(t *testing.T) {
	requireGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// gitlocal refuses a temporary directory inside the repository.
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	if err := os.Mkdir(filepath.Join(root, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "app")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q", "-b", "main")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, clone, "remote", "add", "origin", "https://github.com/acme/app.git")
	git(t, clone, "remote", "add", "fork", "git@github.com:someone/app.git")
	// A linked worktree inside the clone, as agents create them.
	git(t, clone, "worktree", "add", "-q", "-b", "feature", filepath.Join(clone, ".agents", "worktrees", "feature"))
	// A repository of its own nested in the clone's directory.
	nested := filepath.Join(clone, "vendor", "lib")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q", "-b", "main")
	// The linked worktree's branch is pushed under another name; main's
	// upstream is in the fork, another repository.
	git(t, clone, "config", "branch.feature.remote", "origin")
	git(t, clone, "config", "branch.feature.merge", "refs/heads/pr-head")
	git(t, clone, "config", "branch.main.remote", "fork")
	git(t, clone, "config", "branch.main.merge", "refs/heads/main-fork")
	ctx := context.Background()
	common := filepath.Join(clone, ".git")
	work, err := LocalGit(ctx, clone, "acme/app", "")
	if err != nil || work.Work.Branch != "main" || work.Work.Upstream != "" {
		t.Fatalf("clone work %+v %v", work, err)
	}

	top, err := LocalGit(ctx, clone, "acme/app", "origin")
	if err != nil || top.CommonDir != common || top.Pushed != "acme/app" || top.Work != (prlink.Work{}) {
		t.Fatalf("clone %+v %v", top, err)
	}
	linked, err := LocalGit(ctx, filepath.Join(clone, ".agents", "worktrees", "feature"), "acme/app", "")
	if err != nil || linked.CommonDir != common || linked.Work.Branch != "feature" || linked.Work.Upstream != "pr-head" || len(linked.Work.Head) != 40 || linked.Pushed != "" {
		t.Fatalf("linked worktree %+v %v", linked, err)
	}
	inner, err := LocalGit(ctx, nested, "acme/app", "")
	if err != nil || inner.CommonDir == common {
		t.Fatalf("nested repository %+v %v", inner, err)
	}
	fork, err := LocalGit(ctx, clone, "acme/app", "fork")
	if err != nil || fork.Pushed != "someone/app" {
		t.Fatalf("fork remote %+v %v", fork, err)
	}
	url, err := LocalGit(ctx, clone, "acme/app", "https://github.com/acme/app.git")
	if err != nil || url.Pushed != "" {
		t.Fatalf("URL argument %+v %v", url, err)
	}
	missing, err := LocalGit(ctx, clone, "acme/app", "nowhere")
	if err != nil || missing.Pushed != "" {
		t.Fatalf("missing remote %+v %v", missing, err)
	}
	// A distinct pushurl decides, whichever way it points.
	git(t, clone, "remote", "add", "split", "https://github.com/acme/app.git")
	git(t, clone, "remote", "set-url", "--push", "split", "git@github.com:someone/app.git")
	toFork, err := LocalGit(ctx, clone, "acme/app", "split")
	if err != nil || toFork.Pushed != "someone/app" {
		t.Fatalf("fetch from the registered repository, push to a fork %+v %v", toFork, err)
	}
	git(t, clone, "remote", "add", "mirror", "https://github.com/someone/app.git")
	git(t, clone, "remote", "set-url", "--push", "mirror", "https://github.com/acme/app.git")
	toRegistered, err := LocalGit(ctx, clone, "acme/app", "mirror")
	if err != nil || toRegistered.Pushed != "acme/app" {
		t.Fatalf("fetch from a fork, push to the registered repository %+v %v", toRegistered, err)
	}
}

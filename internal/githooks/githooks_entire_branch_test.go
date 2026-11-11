//go:build !windows

package githooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// commitSettings commits Entire's settings on the current branch.
func commitSettings(t *testing.T, repo string) {
	t.Helper()
	enableEntire(t, repo)
	git(t, repo, "add", ".entire/settings.json")
	git(t, repo, "commit", "-q", "-m", "adopt Entire")
}

func untouched(t *testing.T, hooks string) {
	t.Helper()
	for _, hook := range []string{"commit-msg", "prepare-commit-msg", "post-commit", "post-rewrite", "pre-push"} {
		if read(t, filepath.Join(hooks, hook)) != legacyScript(hook) {
			t.Errorf("%s was changed", hook)
		}
	}
}

// The settings are a tracked file: worktrees on an older branch do not have
// them, and the repository still uses Entire.
func TestEntireOnTheDefaultBranchCountsWhateverIsCheckedOut(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "before Entire")
	git(t, repo, "branch", "old")
	commitSettings(t, repo)
	git(t, repo, "checkout", "-q", "old")
	tree := filepath.Join(t.TempDir(), "other")
	git(t, repo, "worktree", "add", "-q", "-b", "older", tree, "old")
	for _, root := range []string{repo, tree} {
		if _, err := os.Stat(filepath.Join(root, ".entire", "settings.json")); err == nil {
			t.Fatalf("%s has the settings checked out", root)
		}
		report := ensure(t, root)
		if report.Changed() {
			t.Fatalf("from %s: %+v", root, report)
		}
	}
	untouched(t, hooks)
}

// A branch with the settings checked out in some worktree counts too, even when
// the default branch does not have them.
func TestEntireCheckedOutInAnyWorktreeCounts(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	tree := filepath.Join(t.TempDir(), "adopting")
	git(t, repo, "worktree", "add", "-q", "-b", "adopt", tree)
	enableEntire(t, tree)
	if report := ensure(t, repo); report.Changed() {
		t.Fatalf("report = %+v", report)
	}
	untouched(t, hooks)
}

func TestEntireAbsentEverywhereIsNotEnabled(t *testing.T) {
	repo, _ := legacyClone(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	if entireEnabled(t.Context(), repo, filepath.Join(repo, ".git", "hooks"), Options{Env: isolated}) {
		t.Fatal("Entire was found")
	}
	if report := ensure(t, repo); !report.Changed() {
		t.Fatalf("nothing was repaired: %+v", report)
	}
}

// When git cannot answer, nothing is changed on a guess.
func TestEntireIsAssumedWhenGitFails(t *testing.T) {
	repo, hooks := legacyClone(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !entireEnabled(ctx, repo, filepath.Join(repo, ".git", "hooks"), Options{Env: isolated}) {
		t.Fatal("a failed check counted as not enabled")
	}
	if !entireEnabled(t.Context(), repo, filepath.Join(repo, ".git", "hooks"), Options{Env: append(isolated, "GIT_DIR="+filepath.Join(repo, "missing"))}) {
		t.Fatal("a git error counted as not enabled")
	}
	untouched(t, hooks)
}

// A dispatcher moved aside is reported when the hook itself is missing too.
func TestOptOutReportsADispatcherWhenTheHookIsMissing(t *testing.T) {
	repo := initRepo(t)
	hooks := filepath.Join(repo, ".git", "hooks")
	writeExecutable(t, filepath.Join(hooks, "post-commit.legacy"), Dispatcher("post-commit", ""))
	git(t, repo, "config", "baum.hooks", "false")
	if report := ensure(t, repo); outcomes(report)["post-commit"] != Unclaimed {
		t.Fatalf("report = %+v", report)
	}
}

// A local repository whose default branch is neither main nor master, with only
// an older branch checked out, is not read as Entire-free.
func TestEntireOnAnUnnamedDefaultBranchIsNotAssumedAbsent(t *testing.T) {
	repo, hooks := legacyClone(t)
	git(t, repo, "checkout", "-q", "-b", "develop")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "before Entire")
	git(t, repo, "branch", "old")
	commitSettings(t, repo)
	git(t, repo, "checkout", "-q", "old")
	tree := filepath.Join(t.TempDir(), "other")
	git(t, repo, "worktree", "add", "-q", "-b", "older", tree, "old")
	for _, root := range []string{repo, tree} {
		if report := ensure(t, root); report.Changed() {
			t.Fatalf("from %s: %+v", root, report)
		}
	}
	untouched(t, hooks)
}

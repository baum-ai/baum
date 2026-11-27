package gitlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pull request's head branch is checked out on a new branch of its own name
// that tracks it, and the clone's other worktree is left as it was.
func TestAddPullWorktreeOnTheHeadBranch(t *testing.T) {
	f := newFixFixture(t)
	git(t, f.remote, "update-ref", "refs/pull/7/head", f.head)
	before := git(t, f.clone, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "sessions", "one", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "feature" || !added.NewBranch || added.Dir != dir {
		t.Fatalf("added = %+v", added)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("worktree HEAD = %s, want %s", got, f.head)
	}
	if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature" {
		t.Fatalf("worktree branch = %s", got)
	}
	if got := git(t, f.clone, "config", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Fatalf("upstream = %s", got)
	}
	if got := git(t, f.clone, "config", "branch.feature.remote"); got != "origin" {
		t.Fatalf("upstream remote = %s", got)
	}
	if got := git(t, f.clone, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the clone's HEAD moved from %s to %s", before, got)
	}
	if got := git(t, f.clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the clone's branch is %s", got)
	}

	// A second worktree for the same pull request cannot take the branch the
	// first has checked out, and gets baum's name for it.
	second := filepath.Join(t.TempDir(), "two", "repo")
	added, err = open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: second, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "baum/pr-7" || !added.NewBranch {
		t.Fatalf("second = %+v", added)
	}
	if got := git(t, second, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("second HEAD = %s", got)
	}
	if got := git(t, f.clone, "config", "branch.baum/pr-7.merge"); got != "refs/heads/feature" {
		t.Fatalf("second upstream = %s", got)
	}

	// The directory must be new and outside the clone.
	_, err = open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: second, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an existing directory: %v", err)
	}
	_, err = open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: filepath.Join(f.clone, "inside"), Number: 7, HeadRef: "feature", Suffix: "abc"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a directory in the clone: %v", err)
	}
}

// A fork's head is fetched from refs/pull/<N>/head onto baum's branch name,
// which tracks that ref, whatever the fork's branch is called.
func TestAddPullWorktreeForAFork(t *testing.T) {
	f := newFixFixture(t)
	git(t, f.remote, "update-ref", "refs/pull/9/head", f.head)
	dir := filepath.Join(t.TempDir(), "fork", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 9, HeadRef: "main", Fork: true, Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "baum/pr-9" || !added.NewBranch {
		t.Fatalf("added = %+v", added)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("worktree HEAD = %s", got)
	}
	if got := git(t, f.clone, "config", "branch.baum/pr-9.merge"); got != "refs/pull/9/head" {
		t.Fatalf("upstream = %s", got)
	}
	if got := git(t, f.clone, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the clone's branch is %s", got)
	}
}

// A branch of the head's name that no worktree has checked out is checked out
// when it points at the head. One at another commit is never moved and never
// used, so the agent does not work on stale code: the next name is.
func TestAddPullWorktreeUsesAnExistingBranchOnlyAtTheHead(t *testing.T) {
	f := newFixFixture(t)
	initial := git(t, f.clone, "rev-parse", "HEAD")
	git(t, f.clone, "branch", "feature", initial)
	dir := filepath.Join(t.TempDir(), "stale", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "baum/pr-7" || !added.NewBranch {
		t.Fatalf("added = %+v", added)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("the worktree is at %s, want the head", got)
	}
	if got := git(t, f.clone, "rev-parse", "refs/heads/feature"); got != initial {
		t.Fatalf("the person's branch moved to %s", got)
	}

	// At the head, the person's branch is used as it is.
	other := newFixFixture(t)
	git(t, other.clone, "fetch", "-q", "origin", "feature:feature")
	dir = filepath.Join(t.TempDir(), "kept", "repo")
	added, err = open(t, other.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "feature" || added.NewBranch {
		t.Fatalf("added = %+v", added)
	}
}

// With the head branch checked out and baum/pr-N at another commit, the
// suffixed name is the one left.
func TestAddPullWorktreeTakesTheSuffixedName(t *testing.T) {
	f := newFixFixture(t)
	initial := git(t, f.clone, "rev-parse", "HEAD")
	git(t, f.clone, "fetch", "-q", "origin", "feature:feature")
	git(t, f.clone, "worktree", "add", "-q", filepath.Join(t.TempDir(), "mine"), "feature")
	git(t, f.clone, "branch", "baum/pr-7", initial)
	dir := filepath.Join(t.TempDir(), "third", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "baum/pr-7-abc" || !added.NewBranch {
		t.Fatalf("added = %+v", added)
	}
	if got := git(t, f.clone, "rev-parse", "refs/heads/baum/pr-7"); got != initial {
		t.Fatalf("baum/pr-7 moved to %s", got)
	}
}

// A worktree in the middle of a rebase lists as detached, but git still
// refuses its branch: the refusal counts as taken and the next name is used.
func TestAddPullWorktreeTreatsARebasingWorktreesBranchAsTaken(t *testing.T) {
	f := newFixFixture(t)
	git(t, f.clone, "fetch", "-q", "origin", "feature:feature")
	rebasing := filepath.Join(t.TempDir(), "rebasing")
	git(t, f.clone, "worktree", "add", "-q", rebasing, "feature")
	git(t, rebasing, "checkout", "-q", "--detach")
	admin := git(t, rebasing, "rev-parse", "--absolute-git-dir")
	write(t, filepath.Join(admin, "rebase-merge"), "head-name", "refs/heads/feature\n", 0o644)
	dir := filepath.Join(t.TempDir(), "next", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "baum/pr-7" {
		t.Fatalf("added = %+v", added)
	}
}

// Neither the clone's post-checkout nor its reference-transaction hook runs.
func TestAddPullWorktreeRunsNoHooks(t *testing.T) {
	f := newFixFixture(t)
	marker := filepath.Join(t.TempDir(), "hooks-ran")
	hooks := git(t, f.clone, "rev-parse", "--git-path", "hooks")
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(f.clone, hooks)
	}
	for _, hook := range []string{"post-checkout", "reference-transaction"} {
		write(t, hooks, hook, "#!/bin/sh\necho "+hook+" >> '"+marker+"'\n", 0o755)
	}
	// The hooks run for an ordinary command, so their silence below means
	// something.
	git(t, f.clone, "branch", "probe")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the reference-transaction hook does not run at all: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "hooks", "repo")
	if _, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(marker); err == nil {
		t.Fatalf("hooks ran: %s", body)
	}
}

// A head that cannot be fetched leaves no directory, ref, branch or worktree.
func TestAddPullWorktreeFetchFailureLeavesNothing(t *testing.T) {
	f := newFixFixture(t)
	parent := filepath.Join(t.TempDir(), "conversation")
	dir := filepath.Join(parent, "repo")
	_, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "gone", Suffix: "abc"})
	if err == nil {
		t.Fatal("a missing head branch was checked out")
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the directory was made: %v", err)
	}
	if refs := git(t, f.clone, "for-each-ref", "refs/baum", "refs/heads/gone", "refs/heads/baum"); refs != "" {
		t.Fatalf("refs left behind: %s", refs)
	}
	if list := git(t, f.clone, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("worktrees left behind: %s", list)
	}
}

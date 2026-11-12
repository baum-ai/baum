package gitlocal

import (
	"context"
	"errors"
	"path/filepath"
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

// A branch of the head's name that exists but no worktree has checked out is
// checked out as it is: it may hold the person's own commits.
func TestAddPullWorktreeKeepsAnExistingBranch(t *testing.T) {
	f := newFixFixture(t)
	initial := git(t, f.clone, "rev-parse", "HEAD")
	git(t, f.clone, "branch", "feature", initial)
	dir := filepath.Join(t.TempDir(), "kept", "repo")
	added, err := open(t, f.clone, Options{}).AddPullWorktree(context.Background(), PullWorktreeOptions{Dir: dir, Number: 7, HeadRef: "feature", Suffix: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Branch != "feature" || added.NewBranch {
		t.Fatalf("added = %+v", added)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != initial {
		t.Fatalf("the existing branch moved to %s", got)
	}
}

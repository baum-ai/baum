//go:build unix

package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// A worktree baum adds on the person's own branch, already at the head,
// records that the branch is not baum's, so no cleanup may delete it.
func TestDirectoryRecordsWhoseBranchItIs(t *testing.T) {
	r := newRepository(t)
	git(t, r.clone, "fetch", "-q", "origin", "feature:feature")
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
	if err != nil {
		t.Fatal(err)
	}
	if !c.CreatedWorktree || c.CreatedBranch || c.Branch != "feature" {
		t.Fatalf("created %+v", c)
	}
	got, _ := f.service.Get(context.Background(), c.ID)
	if !got.CreatedWorktree || got.CreatedBranch {
		t.Fatalf("stored %+v", got)
	}
}

// A head branch that is gone is checked out from the pull request's own ref.
func TestDirectoryFallsBackToThePullRequestRef(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(10)})
	if err != nil {
		t.Fatal(err)
	}
	if !c.CreatedWorktree || c.Branch != "baum/pr-10" || c.DirectoryNote != "" {
		t.Fatalf("created %+v", c)
	}
	if got := git(t, c.Directory, "rev-parse", "HEAD"); got != r.head {
		t.Fatalf("the worktree is at %s", got)
	}
	if got := git(t, r.clone, "config", "branch.baum/pr-10.merge"); got != "refs/pull/10/head" {
		t.Fatalf("upstream %s", got)
	}
}

// A pull request that cannot be checked out at all gets an empty directory
// and a note saying why, and nothing of the attempt is left.
func TestDirectoryNotesAFailedCheckout(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	c, err := f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(11)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Directory != filepath.Join(f.opts.Root, "sessions", c.ID) || c.CreatedWorktree || c.Repository != "" || !strings.Contains(c.DirectoryNote, "could not be checked out") {
		t.Fatalf("created %+v", c)
	}
	if _, err := os.Lstat(filepath.Join(f.opts.Root, "worktrees", c.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the worktree's parent was left: %v", err)
	}
	if got, _ := f.service.Get(context.Background(), c.ID); got.DirectoryNote != c.DirectoryNote {
		t.Fatalf("the note was not stored: %+v", got)
	}
	if list := git(t, r.clone, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("a worktree was left: %s", list)
	}
	// A repository with no clone says so too.
	none := pullFixture(t, r)
	none.opts.Pull = func(context.Context, string, int) (Pull, error) { return Pull{HeadRef: "feature"}, nil }
	none.service.opts.Pull = none.opts.Pull
	c, err = none.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode,
		Page: &agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/other", PullRequest: 7}})
	if err != nil || !strings.Contains(c.DirectoryNote, "No clone of owner/other") {
		t.Fatalf("no clone: %+v %v", c, err)
	}
}

// Two conversations opened at once on one pull request do not race: the
// first adds the worktree and the second finds it.
func TestDirectoryPlacementIsSerializedPerRepository(t *testing.T) {
	r := newRepository(t)
	f := pullFixture(t, r)
	var wg sync.WaitGroup
	results := make([]Conversation, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = f.service.Create(context.Background(), CreateRequest{Agent: AgentClaudeCode, Page: pullPage(7)})
		})
	}
	wg.Wait()
	created := 0
	for i, c := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if c.Directory != results[0].Directory {
			t.Fatalf("conversations got different checkouts: %+v", results)
		}
		if c.CreatedWorktree {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d worktrees were added, want 1: %+v", created, results)
	}
}

package reviewrefs

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
)

// A symbolic ref at a retained name, pointing at a user branch, is neither
// deleted when its version is gone nor moved when its version names another
// commit, and the branch stays as it was.
func TestSymbolicRefsAtRetainedNamesAreLeftAlone(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	orphan := gitlocal.RetainedName{Repository: key.Repository, Number: key.Number, Version: 9, Role: "head"}.Ref()
	moved := gitlocal.RetainedName{Repository: key.Repository, Number: key.Number, Version: 1, Role: "head"}.Ref()
	c.git(t, "symbolic-ref", orphan, "refs/heads/main")
	c.git(t, "symbolic-ref", moved, "refs/heads/main")
	branches := c.refs(t, "refs/heads", "refs/tags")
	// main is at c.Base; version 1 records c.Head as its head.
	m := put(t, s, key, c.Head, c.Base, c.Base)
	if err := Keep(ctx, repo, s, key, m); err == nil || !strings.Contains(err.Error(), "symbolic") {
		t.Fatalf("Keep = %v, want the symbolic ref reported", err)
	}
	result, err := Reconcile(ctx, repo, DefaultLimits)
	if err != nil || result.Symbolic != 2 || result.Written != 0 || result.Deleted != 0 || len(result.Failed) != 0 {
		t.Fatalf("pass = %+v, %v", result, err)
	}
	if got := c.refs(t, "refs/heads", "refs/tags"); got != branches {
		t.Fatalf("branches changed:\n%s\nwant:\n%s", got, branches)
	}
	for _, name := range []string{orphan, moved} {
		if target := c.git(t, "symbolic-ref", name); target != "refs/heads/main" {
			t.Fatalf("%s now names %q", name, target)
		}
	}
}

// Commits old versions lost sort after a new version's, and lookups go on
// past the first batch up to their bound, which asks for another pass only
// when it cut off refs after writing some.
func TestReconcileLooksUpNewestFirstInBoundedBatches(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	missing := strings.Repeat("ef", 20)
	put(t, s, key, c.Head, c.Base, c.Base)       // version 1: 3 refs
	put(t, s, key, missing, missing, missing)    // version 2: none
	put(t, s, key, c.Commits[0], c.Base, c.Base) // version 3: 3 refs
	limits := Limits{Check: 2, Write: 100, Delete: 100}
	// Version 3's three, then version 2's missing three, then two of version
	// 1's: eight lookups, the bound.
	result, err := Reconcile(ctx, repo, limits)
	if err != nil || result.Written != 5 || result.Missing != 3 || !result.More {
		t.Fatalf("first pass = %+v, %v", result, err)
	}
	if got := c.refs(t, gitlocal.RetainPrefix); !strings.Contains(got, "/7/3/merge-base ") || strings.Contains(got, "/7/1/merge-base ") {
		t.Fatalf("not newest first:\n%s", got)
	}
	result, err = Reconcile(ctx, repo, limits)
	if err != nil || result.Written != 1 || result.Missing != 3 || result.More {
		t.Fatalf("second pass = %+v, %v", result, err)
	}
	// Lookups cut off with nothing written do not ask for another pass: the
	// next would look up the same missing commits.
	put(t, s, key, missing, missing, missing)
	put(t, s, key, missing, missing, missing)
	put(t, s, key, missing, missing, missing)
	put(t, s, store.Key{Repository: key.Repository, Number: 1}, c.Head, c.Base, c.Base)
	result, err = Reconcile(ctx, repo, Limits{Check: 2, Write: 100, Delete: 100})
	if err != nil || result.Written != 0 || result.More {
		t.Fatalf("starved pass = %+v, %v", result, err)
	}
}

// A ref that cannot be written (its name clashes with another ref) fails
// alone: the rest of the pass is written.
func TestReconcileWritesAroundARefThatCannotChange(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	put(t, s, key, c.Head, c.Base, c.Base)
	put(t, s, key, c.Commits[0], c.Base, c.Base)
	blocker := strings.TrimSuffix(gitlocal.RetainVersionPrefix(key.Repository, key.Number, 2), "/") + "/head/x"
	c.git(t, "update-ref", blocker, c.Head)
	result, err := Reconcile(ctx, repo, DefaultLimits)
	head2 := gitlocal.RetainedName{Repository: key.Repository, Number: key.Number, Version: 2, Role: "head"}.Ref()
	if err != nil || result.Written != 5 || !slices.Equal(result.Failed, []string{head2}) {
		t.Fatalf("pass = %+v, %v", result, err)
	}
	got := c.refs(t, gitlocal.RetainPrefix)
	for _, role := range []string{"1/head", "1/base", "1/merge-base", "2/base", "2/merge-base"} {
		if !strings.Contains(got, "/7/"+role+" ") {
			t.Fatalf("%s not written:\n%s", role, got)
		}
	}
	if !strings.Contains(got, blocker+" ") {
		t.Fatalf("the clashing ref was touched:\n%s", got)
	}
}

// Another writer that writes the version's refs first is no failure.
func TestKeepAcceptsRefsAnotherWriterWroteFirst(t *testing.T) {
	c := newClone(t)
	repo, s := c.open(t), c.store(t)
	m := put(t, s, key, c.Head, c.Base, c.Base)
	beforeKeepWrite = func() {
		for name, oid := range refs(key, m.Version, m.Head, m.Base, m.MergeBase) {
			c.git(t, "update-ref", name, oid)
		}
	}
	t.Cleanup(func() { beforeKeepWrite = nil })
	if err := Keep(context.Background(), repo, s, key, m); err != nil {
		t.Fatalf("Keep = %v", err)
	}
	// One that wrote something else still fails it.
	m = put(t, s, key, c.Commits[0], c.Base, c.Base)
	beforeKeepWrite = func() {
		c.git(t, "update-ref", gitlocal.RetainedName{Repository: key.Repository, Number: key.Number, Version: m.Version, Role: "head"}.Ref(), c.Base)
	}
	if err := Keep(context.Background(), repo, s, key, m); err == nil {
		t.Fatal("Keep reported success with a ref at another commit")
	}
}

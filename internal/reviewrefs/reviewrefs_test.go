package reviewrefs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/store"
)

var key = store.Key{Repository: linktest.Repository, Number: 7}

type clone struct {
	linktest.Fixture
	common string
}

func newClone(t *testing.T) clone {
	t.Helper()
	f := linktest.New(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return clone{Fixture: f, common: filepath.Join(f.Repo, ".git")}
}

func (c clone) git(t *testing.T, args ...string) string {
	t.Helper()
	return linktest.Git(t, c.Repo, args...)
}

// branch commits file on a new branch from start and returns the commit.
func (c clone) branch(t *testing.T, name, start, file string) string {
	t.Helper()
	c.git(t, "checkout", "-q", "-b", name, start)
	if err := os.WriteFile(filepath.Join(c.Repo, file), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.git(t, "add", "-A")
	c.git(t, "commit", "-q", "-m", name)
	oid := c.git(t, "rev-parse", "HEAD")
	c.git(t, "checkout", "-q", "main")
	return oid
}

func (c clone) open(t *testing.T) *gitlocal.Repo {
	t.Helper()
	repo, err := gitlocal.Open(context.Background(), c.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func (c clone) store(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(c.common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// put stores the next version of k with the commits given.
func put(t *testing.T, s *store.Store, k store.Key, head, base, mergeBase string) store.Metadata {
	t.Helper()
	ctx := context.Background()
	previous, err := s.Latest(ctx, k)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	m := store.Metadata{Version: previous.Metadata.Version + 1, Head: head, Base: base, MergeBase: mergeBase}
	// A document names owner/name, without an Enterprise host.
	parts := strings.Split(k.Repository, "/")
	repository := strings.Join(parts[len(parts)-2:], "/")
	document := fmt.Sprintf(`{"review":{"repository":%q,"pull_request":%d,"version":%d}}`, repository, k.Number, m.Version)
	if err := s.Put(ctx, k, previous, store.Version{Metadata: m, Document: []byte(document)}); err != nil {
		t.Fatal(err)
	}
	return m
}

func (c clone) refs(t *testing.T, patterns ...string) string {
	t.Helper()
	return c.git(t, append([]string{"for-each-ref", "--format=%(refname) %(objectname)"}, patterns...)...)
}

// outside lists every ref but the retained ones.
func (c clone) outside(t *testing.T) string {
	t.Helper()
	var kept []string
	for _, line := range strings.Split(c.refs(t), "\n") {
		if !strings.HasPrefix(line, gitlocal.RetainPrefix) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func (c clone) has(t *testing.T, oid string) bool {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "-e", oid+"^{commit}")
	cmd.Dir = c.Repo
	return cmd.Run() == nil
}

func (c clone) collect(t *testing.T) {
	t.Helper()
	c.git(t, "reflog", "expire", "--expire=now", "--all")
	c.git(t, "gc", "-q", "--prune=now")
}

// An old version's head, base and merge base survive a force push, deleted
// branches and git gc --prune=now, and its lazy patch still reads.
func TestKeptVersionSurvivesForcePushAndGC(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	// Version 1 compares a topic branch with an upstream that moved on; both
	// branches are deleted afterwards.
	mergeBase1 := c.branch(t, "upstream", "main", "upstream.txt")
	base1 := c.branch(t, "upstream2", mergeBase1, "upstream2.txt")
	head1 := c.branch(t, "topic1", mergeBase1, "one.txt")
	head2 := c.branch(t, "topic2", "main", "two.txt")
	unkept := c.branch(t, "scratch", "main", "scratch.txt")
	c.git(t, "update-ref", "refs/baum/pull/7/head", head1)
	repo, s := c.open(t), c.store(t)
	first := put(t, s, key, head1, base1, mergeBase1)
	if err := Keep(ctx, repo, s, key, first); err != nil {
		t.Fatal(err)
	}
	c.git(t, "update-ref", "refs/baum/pull/7/head", head2)
	second := put(t, s, key, head2, c.Base, c.Base)
	if err := Keep(ctx, repo, s, key, second); err != nil {
		t.Fatal(err)
	}
	// Keeping a version twice changes nothing.
	if err := Keep(ctx, repo, s, key, first); err != nil {
		t.Fatal(err)
	}
	c.git(t, "branch", "-q", "-D", "upstream", "upstream2", "topic1", "topic2", "scratch")
	c.collect(t)
	if c.has(t, unkept) {
		t.Fatal("gc kept an unreferenced commit; the test proves nothing")
	}
	for _, oid := range []string{head1, base1, mergeBase1, head2} {
		if !c.has(t, oid) {
			t.Fatalf("commit %s was pruned", oid)
		}
	}
	want := strings.Join([]string{
		"refs/baum/reviews/acme%2Fwidgets/7/1/base " + base1,
		"refs/baum/reviews/acme%2Fwidgets/7/1/head " + head1,
		"refs/baum/reviews/acme%2Fwidgets/7/1/merge-base " + mergeBase1,
		"refs/baum/reviews/acme%2Fwidgets/7/2/base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/2/head " + head2,
		"refs/baum/reviews/acme%2Fwidgets/7/2/merge-base " + c.Base,
	}, "\n")
	if got := c.refs(t, gitlocal.RetainPrefix); got != want {
		t.Fatalf("refs:\n%s\nwant:\n%s", got, want)
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := locallink.Patch(readCtx, locallink.PatchRequest{Dir: c.Repo, Repository: linktest.Repository, Head: head1, Base: base1, MergeBase: mergeBase1, Path: "one.txt"})
	if err != nil || !result.Available || result.Patch != "@@ -0,0 +1 @@\n+topic1\n" {
		t.Fatalf("old version's patch = %+v, %v", result, err)
	}
}

func TestKeepSkipsMissingCommitsAndStoresElsewhere(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	missing := strings.Repeat("ab", 20)
	m := put(t, s, key, missing, c.Base, c.Base)
	if err := Keep(ctx, repo, s, key, m); err != nil {
		t.Fatal(err)
	}
	if got := c.refs(t, gitlocal.RetainPrefix); strings.Contains(got, "/head ") || !strings.Contains(got, "/1/base "+c.Base) {
		t.Fatalf("refs = %q", got)
	}
	elsewhere, err := store.Open(c.common, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	other := store.Key{Repository: "acme/other", Number: 1}
	m = put(t, elsewhere, other, c.Head, c.Base, c.Base)
	if err := Keep(ctx, repo, elsewhere, other, m); err != nil {
		t.Fatal(err)
	}
	if got := c.refs(t, gitlocal.RetainPrefix); strings.Contains(got, "acme%2Fother") {
		t.Fatalf("a version in an explicit store got refs: %q", got)
	}
}

func TestKeepReportsAFailedWrite(t *testing.T) {
	c := newClone(t)
	repo, s := c.open(t), c.store(t)
	// A ref where the version's directory would go makes the write fail.
	c.git(t, "update-ref", "refs/baum/reviews/acme%2Fwidgets/7", c.Head)
	m := put(t, s, key, c.Head, c.Base, c.Base)
	if err := Keep(context.Background(), repo, s, key, m); err == nil {
		t.Fatal("Keep reported success for refs it could not write")
	}
}

func TestReconcileCreatesMissingRefsForExistingVersions(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	missing := strings.Repeat("cd", 20)
	put(t, s, key, c.Head, c.Base, c.Base)
	put(t, s, key, missing, c.Base, c.Base)
	put(t, s, store.Key{Repository: "ghe.example.com/team/app", Number: 3}, c.Commits[0], c.Base, "")
	result, err := Reconcile(ctx, repo, Limits{Check: 100, Write: 4, Delete: 100})
	if err != nil || result.Written != 4 || result.Missing != 1 || !result.More {
		t.Fatalf("first pass = %+v, %v", result, err)
	}
	result, err = Reconcile(ctx, repo, Limits{Check: 100, Write: 4, Delete: 100})
	if err != nil || result.Written != 3 || result.Missing != 1 || result.More {
		t.Fatalf("second pass = %+v, %v", result, err)
	}
	want := strings.Join([]string{
		"refs/baum/reviews/acme%2Fwidgets/7/1/base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/1/head " + c.Head,
		"refs/baum/reviews/acme%2Fwidgets/7/1/merge-base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/2/base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/2/merge-base " + c.Base,
		"refs/baum/reviews/ghe%2Eexample%2Ecom%2Fteam%2Fapp/3/1/base " + c.Base,
		"refs/baum/reviews/ghe%2Eexample%2Ecom%2Fteam%2Fapp/3/1/head " + c.Commits[0],
	}, "\n")
	if got := c.refs(t, gitlocal.RetainPrefix); got != want {
		t.Fatalf("refs:\n%s\nwant:\n%s", got, want)
	}
	if result, err := Reconcile(ctx, repo, DefaultLimits); err != nil || result != (Result{Missing: 1}) {
		t.Fatalf("settled pass = %+v, %v", result, err)
	}
}

func TestReconcileDeletesOnlyOrphanedRetainedRefsWithinBounds(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo, s := c.open(t), c.store(t)
	put(t, s, key, c.Head, c.Base, c.Base)
	c.git(t, "tag", "v1", c.Head)
	c.git(t, "branch", "kept", c.Head)
	for _, ref := range []string{"refs/baum/pull/7/head", "refs/baum/pull/7/base", "refs/baum/fix/x/head", "refs/baum/default",
		"refs/baum/reviewsx/acme%2Fwidgets/7/9/head", "refs/baum/reviews/junk", "refs/baum/reviews/acme%2Fwidgets/7/9/tip"} {
		c.git(t, "update-ref", ref, c.Head)
	}
	orphans := []string{
		"refs/baum/reviews/acme%2Fwidgets/7/9/head", "refs/baum/reviews/acme%2Fwidgets/8/1/base",
		"refs/baum/reviews/acme%2Fother/7/1/head",
	}
	for _, ref := range orphans {
		c.git(t, "update-ref", ref, c.Base)
	}
	others := c.outside(t)
	if !strings.Contains(others, "refs/heads/kept") || !strings.Contains(others, "refs/tags/v1") {
		t.Fatalf("setup refs: %q", others)
	}
	limits := Limits{Check: 100, Write: 100, Delete: 2}
	result, err := Reconcile(ctx, repo, limits)
	if err != nil || result.Written != 3 || result.Deleted != 2 || !result.More {
		t.Fatalf("first pass = %+v, %v", result, err)
	}
	result, err = Reconcile(ctx, repo, limits)
	if err != nil || result.Deleted != 1 || result.More {
		t.Fatalf("second pass = %+v, %v", result, err)
	}
	if got := c.outside(t); got != others {
		t.Fatalf("refs outside the namespace changed:\n%s\nwant:\n%s", got, others)
	}
	want := strings.Join([]string{
		"refs/baum/reviews/acme%2Fwidgets/7/1/base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/1/head " + c.Head,
		"refs/baum/reviews/acme%2Fwidgets/7/1/merge-base " + c.Base,
		"refs/baum/reviews/acme%2Fwidgets/7/9/tip " + c.Head,
		"refs/baum/reviews/junk " + c.Head,
	}, "\n")
	if got := c.refs(t, gitlocal.RetainPrefix); got != want {
		t.Fatalf("retained refs:\n%s\nwant:\n%s", got, want)
	}
}

func TestReconcileDeletesNothingWithoutAReadableStore(t *testing.T) {
	c := newClone(t)
	ctx := context.Background()
	repo := c.open(t)
	orphan := "refs/baum/reviews/acme%2Fwidgets/7/1/head"
	c.git(t, "update-ref", orphan, c.Head)
	// No store at all: a store gone missing is not an empty one.
	if result, err := Reconcile(ctx, repo, DefaultLimits); err != nil || result != (Result{}) {
		t.Fatalf("pass without a store = %+v, %v", result, err)
	}
	roots, err := store.Roots(c.common)
	if err != nil {
		t.Fatal(err)
	}
	// A fallback store that cannot be read fails the pass, though the
	// clone-local one reads.
	c.store(t)
	if err := os.MkdirAll(roots[1], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roots[1], "versions.sqlite"), []byte("not a database, but long enough to look like one at first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, repo, DefaultLimits); err == nil {
		t.Fatal("a pass over an unreadable store succeeded")
	}
	if got := c.refs(t, gitlocal.RetainPrefix); got != orphan+" "+c.Head {
		t.Fatalf("refs = %q", got)
	}
	// Once only the readable store is left, the orphan goes.
	if err := os.Remove(filepath.Join(roots[1], "versions.sqlite")); err != nil {
		t.Fatal(err)
	}
	if result, err := Reconcile(ctx, repo, DefaultLimits); err != nil || result.Deleted != 1 {
		t.Fatalf("pass = %+v, %v", result, err)
	}
}

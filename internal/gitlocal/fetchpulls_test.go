package gitlocal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// countFetches records the refspecs of each fetch until the test ends.
func countFetches(t *testing.T) *[][]string {
	t.Helper()
	var fetches [][]string
	onFetch = func(refspecs []string) { fetches = append(fetches, refspecs) }
	t.Cleanup(func() { onFetch = nil })
	return &fetches
}

// FetchPulls fetches every missing head, and a missing base by its id, in
// one fetch, and nothing when the clone has them all.
func TestFetchPulls(t *testing.T) {
	f := newRemoteFixture(t)
	git(t, f.remote, "checkout", "-q", "-b", "other")
	write(t, f.remote, "c.txt", "other\n", 0o644)
	other := commit(t, f.remote, "other change")
	git(t, f.remote, "update-ref", "refs/pull/9/head", other)
	git(t, f.remote, "checkout", "-q", "main")
	// f.base, on the remote's main, is not in the clone.
	prs := []PullRequest{{Number: 7, HeadSHA: f.head, BaseSHA: f.base}, {Number: 9, HeadSHA: other, BaseSHA: f.base}}

	r := open(t, f.clone, Options{})
	fetches := countFetches(t)
	if err := r.FetchPulls(context.Background(), "", prs); err != nil {
		t.Fatal(err)
	}
	if len(*fetches) != 1 || len((*fetches)[0]) != 3 {
		t.Fatalf("fetches = %v, want one of three refspecs", *fetches)
	}
	refs := git(t, f.clone, "for-each-ref", "--format=%(refname) %(objectname)", "refs/baum")
	want := strings.Join([]string{"refs/baum/pull/7/base " + f.base, "refs/baum/pull/7/head " + f.head, "refs/baum/pull/9/head " + other}, "\n")
	if refs != want {
		t.Fatalf("refs = %q, want %q", refs, want)
	}
	// Everything present: nothing is fetched from a remote that is gone, and
	// a pull request without valid ids is skipped.
	git(t, f.clone, "remote", "set-url", "origin", "/nonexistent")
	if err := r.FetchPulls(context.Background(), "origin", append(prs, PullRequest{Number: 8, HeadSHA: "main", BaseSHA: f.base})); err != nil || len(*fetches) != 1 {
		t.Fatalf("fetches = %v, %v", *fetches, err)
	}
	if err := r.FetchPulls(context.Background(), "origin", []PullRequest{{Number: 10, HeadSHA: strings.Repeat("e", 40), BaseSHA: f.base}}); err == nil {
		t.Fatal("a missing head from a remote that is gone must fail")
	}
}

// A pull request ref the remote lacks does not keep the others' heads out.
func TestFetchPullsPastAMissingRef(t *testing.T) {
	f := newRemoteFixture(t)
	git(t, f.clone, "fetch", "-q", "origin", "main")
	prs := []PullRequest{{Number: 7, HeadSHA: f.head, BaseSHA: f.base}, {Number: 8, HeadSHA: strings.Repeat("e", 40), BaseSHA: f.base}}
	r := open(t, f.clone, Options{})
	fetches := countFetches(t)
	if err := r.FetchPulls(context.Background(), "", prs); err != nil {
		t.Fatal(err)
	}
	// The pair, then each alone.
	if len(*fetches) != 3 || len((*fetches)[0]) != 2 {
		t.Fatalf("fetches = %v", *fetches)
	}
	if refs := git(t, f.clone, "for-each-ref", "--format=%(refname)", "refs/baum"); refs != "refs/baum/pull/7/head" {
		t.Fatalf("refs = %q", refs)
	}
	// Only a missing ref, from a remote that answers: one fetch, not
	// repeated, and no error.
	*fetches = nil
	if err := r.FetchPulls(context.Background(), "", prs[1:]); err != nil || len(*fetches) != 1 {
		t.Fatalf("refs the remote lacks: %v, fetches %v", err, *fetches)
	}
	// A remote out of reach fails at once, nothing fetched one at a time.
	write(t, f.remote, "d.txt", "d\n", 0o644)
	git(t, f.remote, "update-ref", "refs/pull/9/head", commit(t, f.remote, "nine"))
	nine := git(t, f.remote, "rev-parse", "refs/pull/9/head")
	git(t, f.clone, "remote", "set-url", "origin", "/nonexistent")
	*fetches = nil
	if err := r.FetchPulls(context.Background(), "", append(prs[1:], PullRequest{Number: 9, HeadSHA: nine, BaseSHA: f.base})); err == nil || len(*fetches) != 1 {
		t.Fatalf("a remote out of reach: %v, fetches %v", err, *fetches)
	}
}

// More heads than one fetch takes all arrive.
func TestFetchPullsBatches(t *testing.T) {
	f := newRemoteFixture(t)
	git(t, f.clone, "fetch", "-q", "origin", "main")
	var prs []PullRequest
	for n := 1; n <= maxFetchRefspecs+1; n++ {
		head := git(t, f.remote, "commit-tree", "-p", f.base, "-m", fmt.Sprintf("pr %d", n), f.base+"^{tree}")
		git(t, f.remote, "update-ref", fmt.Sprintf("refs/pull/%d/head", n), head)
		prs = append(prs, PullRequest{Number: n, HeadSHA: head, BaseSHA: f.base})
	}
	r := open(t, f.clone, Options{})
	fetches := countFetches(t)
	if err := r.FetchPulls(context.Background(), "", prs); err != nil {
		t.Fatal(err)
	}
	if len(*fetches) != 2 || len((*fetches)[0]) != maxFetchRefspecs || len((*fetches)[1]) != 1 {
		t.Fatalf("%d fetches", len(*fetches))
	}
	if refs := strings.Fields(git(t, f.clone, "for-each-ref", "--format=%(objectname)", "refs/baum/pull")); len(refs) != len(prs) {
		t.Fatalf("%d heads fetched, want %d", len(refs), len(prs))
	}
}

// FetchBranch fetches the remote's branch into DefaultRef and returns its
// head; a bad branch name is refused before anything runs.
func TestFetchBranch(t *testing.T) {
	f := newRemoteFixture(t)
	write(t, f.remote, "d.txt", "newer\n", 0o644)
	newer := commit(t, f.remote, "newer change")
	r := open(t, f.clone, Options{})
	got, err := r.FetchBranch(context.Background(), "", "main")
	if err != nil || got != newer || git(t, f.clone, "rev-parse", DefaultRef) != newer {
		t.Fatalf("FetchBranch = %s, %v; want %s", got, err, newer)
	}
	if _, err := r.FetchBranch(context.Background(), "", "a:b"); err != ErrInvalidInput {
		t.Fatalf("a bad branch gives %v", err)
	}
}

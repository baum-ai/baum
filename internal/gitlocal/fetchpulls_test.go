package gitlocal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// FetchPullHeads fetches every missing head in one fetch, and none when the
// clone has them all.
func TestFetchPullHeads(t *testing.T) {
	f := newRemoteFixture(t)
	git(t, f.remote, "checkout", "-q", "-b", "other")
	write(t, f.remote, "c.txt", "other\n", 0o644)
	other := commit(t, f.remote, "other change")
	git(t, f.remote, "update-ref", "refs/pull/9/head", other)
	git(t, f.remote, "checkout", "-q", "main")
	prs := []PullRequest{{Number: 7, HeadSHA: f.head}, {Number: 9, HeadSHA: other}}

	r := open(t, f.clone, Options{})
	if err := r.FetchPullHeads(context.Background(), "", prs); err != nil {
		t.Fatal(err)
	}
	refs := git(t, f.clone, "for-each-ref", "--format=%(refname) %(objectname)", "refs/baum")
	if want := "refs/baum/pull/7/head " + f.head + "\nrefs/baum/pull/9/head " + other; refs != want {
		t.Fatalf("refs = %q, want %q", refs, want)
	}
	// Both heads present: nothing is fetched from a remote that is gone.
	git(t, f.clone, "remote", "set-url", "origin", "/nonexistent")
	if err := r.FetchPullHeads(context.Background(), "origin", prs); err != nil {
		t.Fatal(err)
	}
	if err := r.FetchPullHeads(context.Background(), "origin", []PullRequest{{Number: 7, HeadSHA: "main"}}); err != ErrInvalidInput {
		t.Fatalf("err = %v, want invalid input", err)
	}
	if err := r.FetchPullHeads(context.Background(), "origin", []PullRequest{{Number: 10, HeadSHA: strings.Repeat("e", 40)}}); err == nil {
		t.Fatal("a missing head from a remote that is gone must fail")
	}
}

// More heads than one fetch takes all arrive.
func TestFetchPullHeadsBatches(t *testing.T) {
	f := newRemoteFixture(t)
	var prs []PullRequest
	for n := 1; n <= maxFetchRefspecs+1; n++ {
		head := git(t, f.remote, "commit-tree", "-p", f.base, "-m", fmt.Sprintf("pr %d", n), f.base+"^{tree}")
		git(t, f.remote, "update-ref", fmt.Sprintf("refs/pull/%d/head", n), head)
		prs = append(prs, PullRequest{Number: n, HeadSHA: head})
	}
	r := open(t, f.clone, Options{})
	if err := r.FetchPullHeads(context.Background(), "", prs); err != nil {
		t.Fatal(err)
	}
	if refs := strings.Fields(git(t, f.clone, "for-each-ref", "--format=%(objectname)", "refs/baum/pull")); len(refs) != len(prs) {
		t.Fatalf("%d heads fetched, want %d", len(refs), len(prs))
	}
}

package gitlocal

import (
	"context"
	"testing"
)

func TestValidationCommitReadsCanonicalObjectAndTree(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	git(t, f.dir, "branch", f.head[:7], f.base)
	for _, id := range []string{f.head, f.head[:7], f.head[:12]} {
		got, err := r.ValidationCommit(ctx, id)
		if err != nil || got.SHA != f.head || got.TreeSHA != git(t, f.dir, "show", "-s", "--format=%T", f.head) {
			t.Fatalf("%s: %+v %v", id, got, err)
		}
	}
	for _, id := range []string{"HEAD", "--all", "refs/heads/main", "abc", "abcdef0123456789"} {
		if _, err := r.ValidationCommit(ctx, id); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	// An amend which changes only the message has another SHA and the same tree.
	git(t, f.dir, "checkout", "-q", "feature")
	git(t, f.dir, "commit", "--amend", "-m", "amended")
	amended := git(t, f.dir, "rev-parse", "HEAD")
	before, err := r.ValidationCommit(ctx, f.head)
	if err != nil {
		t.Fatal(err)
	}
	after, err := r.ValidationCommit(ctx, amended)
	if err != nil {
		t.Fatal(err)
	}
	if before.SHA == after.SHA || before.TreeSHA != after.TreeSHA {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

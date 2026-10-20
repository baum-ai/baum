package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveCommit(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	for _, id := range []string{f.head, f.head[:7], f.head[:12]} {
		if got, err := r.ResolveCommit(ctx, id); err != nil || got != f.head {
			t.Errorf("ResolveCommit(%q) = %q, %v", id, got, err)
		}
	}
	if _, err := r.ResolveCommit(ctx, "abcdef0123456789"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
	for _, id := range []string{"main", "HEAD~1", "abc", "ABCDEF1", ""} {
		if _, err := r.ResolveCommit(ctx, id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ResolveCommit(%q) err = %v, want ErrInvalidInput", id, err)
		}
	}
}

// TestResolveCommitAmbiguous builds enough commits with fast-import that two
// share a seven-digit prefix, then asks for that prefix.
func TestResolveCommitAmbiguous(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 120000 commits")
	}
	requireGit(t)
	dir := initRepo(t)
	var stream strings.Builder
	for i := range 120000 {
		fmt.Fprintf(&stream, "commit refs/heads/many\ncommitter T <t@example.com> %d +0000\ndata 2\nx\n\n", 1_700_000_000+i)
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stream.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	prefixes := map[string]bool{}
	var shared string
	for _, oid := range strings.Fields(git(t, dir, "rev-list", "refs/heads/many")) {
		if prefixes[oid[:7]] {
			shared = oid[:7]
			break
		}
		prefixes[oid[:7]] = true
	}
	if shared == "" {
		t.Skip("no two commits share a seven-digit prefix")
	}
	r := open(t, dir, Options{})
	if _, err := r.ResolveCommit(context.Background(), shared); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ResolveCommit(%q) err = %v, want ErrAmbiguous", shared, err)
	}
	if _, err := r.ValidationCommit(context.Background(), shared); err == nil {
		t.Errorf("ValidationCommit(%q) accepted an ambiguous object prefix", shared)
	}
}

func TestCommitsInAPartialCloneNeedNoBlobs(t *testing.T) {
	requireGit(t)
	src := initRepo(t)
	var content strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&content, "%d\n", i)
	}
	write(t, src, "a.txt", content.String(), 0o644)
	base := commit(t, src, "base")
	git(t, src, "checkout", "-q", "-b", "feature")
	git(t, src, "mv", "a.txt", "moved.txt")
	write(t, src, "moved.txt", content.String()+"extra\n", 0o644)
	head := commit(t, src, "rename with an edit")
	git(t, src, "checkout", "-q", "main")
	git(t, src, "config", "uploadpack.allowFilter", "true")

	partial := filepath.Join(t.TempDir(), "partial")
	git(t, src, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+filepath.ToSlash(src), partial)
	git(t, partial, "fetch", "-q", "origin", "feature")
	if err := os.Rename(src, src+"-gone"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(src+"-gone", src) })
	before := tree(t, filepath.Join(partial, ".git", "objects"))

	r := open(t, partial, Options{})
	commits, err := r.Commits(context.Background(), base, head)
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if len(commits) != 1 || !slices.Equal(commits[0].Files, []string{"a.txt", "moved.txt"}) {
		t.Fatalf("commits = %+v", commits)
	}
	if tree(t, filepath.Join(partial, ".git", "objects")) != before {
		t.Fatal("objects were fetched")
	}
}

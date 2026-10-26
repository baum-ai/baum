package gitlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixFixture is a bare remote with main and a feature branch that conflict
// in a.txt, and a clone of it made before either moved on.
type fixFixture struct {
	remote, clone, seed string
	head, base          string
}

func newFixFixture(t *testing.T) fixFixture {
	t.Helper()
	requireGit(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, t.TempDir(), "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-b", "main")
	write(t, seed, "a.txt", "one\n", 0o644)
	commit(t, seed, "initial")
	git(t, seed, "push", "-q", "origin", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, t.TempDir(), "clone", "-q", remote, clone)
	git(t, seed, "checkout", "-q", "-b", "feature")
	write(t, seed, "a.txt", "feature\n", 0o644)
	head := commit(t, seed, "feature change")
	git(t, seed, "push", "-q", "origin", "feature")
	git(t, seed, "checkout", "-q", "main")
	write(t, seed, "a.txt", "main\n", 0o644)
	base := commit(t, seed, "main change")
	git(t, seed, "push", "-q", "origin", "main")
	return fixFixture{remote: remote, clone: clone, seed: seed, head: head, base: base}
}

func startFix(t *testing.T, f fixFixture, opts FixOptions) (*Fix, error) {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = filepath.Join(t.TempDir(), "fix")
	}
	if opts.ID == "" {
		opts.ID = "job1"
	}
	if opts.HeadRef == "" {
		opts.HeadRef = "feature"
	}
	if opts.HeadSHA == "" {
		opts.HeadSHA = f.head
	}
	return open(t, f.clone, Options{}).StartFix(context.Background(), opts)
}

func TestFixMergesCommitsAndPushesWithoutForce(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	fix, err := startFix(t, f, FixOptions{BaseRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if fix.Base != f.base || readFile(t, filepath.Join(fix.Dir(), "a.txt")) != "feature\n" {
		t.Fatalf("checkout at the wrong commit: base %s", fix.Base)
	}
	// A hook in the clone must never run.
	write(t, f.clone, ".git/hooks/pre-commit", "#!/bin/sh\ntouch \"$GIT_DIR/../../hook-ran\"\nexit 1\n", 0o755)
	write(t, f.clone, ".git/hooks/pre-push", "#!/bin/sh\nexit 1\n", 0o755)
	conflicts, err := fix.Merge(ctx, "Merge main into feature")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "a.txt" {
		t.Fatalf("conflicts %q, %v", conflicts, err)
	}
	if !strings.Contains(readFile(t, filepath.Join(fix.Dir(), "a.txt")), "<<<<<<<") {
		t.Fatal("the conflict markers are not in the checkout")
	}
	write(t, fix.Dir(), "a.txt", "feature and main\n", 0o644)
	// A .git file the agent rewrote is ignored.
	write(t, fix.Dir(), ".git", "gitdir: /nowhere\n", 0o644)
	merged, err := fix.Commit(ctx, "Merge main into feature")
	if err != nil {
		t.Fatal(err)
	}
	if parents := git(t, f.clone, "rev-list", "--parents", "-n", "1", merged); parents != merged+" "+f.head+" "+f.base {
		t.Fatalf("not a merge of head and base: %s", parents)
	}
	if ok, err := fix.Descends(ctx, merged); !ok || err != nil {
		t.Fatalf("descends %v %v", ok, err)
	}
	if err := fix.Push(ctx, merged); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.remote, "rev-parse", "refs/heads/feature"); got != merged {
		t.Fatalf("remote feature at %s, want %s", got, merged)
	}
	if err := fix.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fix.dir); !os.IsNotExist(err) {
		t.Fatalf("the fix directory remains: %v", err)
	}
	if list := git(t, f.clone, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("a worktree remains registered:\n%s", list)
	}
	if refs := git(t, f.clone, "for-each-ref", "refs/baum/"); refs != "" {
		t.Fatalf("temporary refs remain: %s", refs)
	}
}

func TestFixRefusesAMovedBranchAndARejectedPush(t *testing.T) {
	f := newFixFixture(t)
	ctx := context.Background()
	stale := git(t, f.seed, "rev-parse", "main~1")
	dir := filepath.Join(t.TempDir(), "fix")
	if _, err := startFix(t, f, FixOptions{Dir: dir, HeadSHA: stale}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err %v, want ErrHeadMoved", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a refused fix left its directory")
	}

	fix, err := startFix(t, f, FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fix.Close() }()
	if head, err := fix.Commit(ctx, "nothing"); err != nil || head != f.head {
		t.Fatalf("an empty commit was made: %s %v", head, err)
	}
	write(t, fix.Dir(), "b.txt", "fixed\n", 0o644)
	fixed, err := fix.Commit(ctx, "Fix the build")
	if err != nil || fixed == f.head {
		t.Fatalf("commit %s %v", fixed, err)
	}
	// The branch moves on while the fix runs: the push is refused, not forced.
	git(t, f.seed, "checkout", "-q", "feature")
	write(t, f.seed, "c.txt", "someone else\n", 0o644)
	moved := commit(t, f.seed, "someone else")
	git(t, f.seed, "push", "-q", "origin", "feature")
	if err := fix.Push(ctx, fixed); !errors.Is(err, ErrPushRejected) {
		t.Fatalf("err %v, want ErrPushRejected", err)
	}
	if got := git(t, f.remote, "rev-parse", "refs/heads/feature"); got != moved {
		t.Fatal("the push replaced someone else's commit")
	}
	// A commit that drops the head, such as its parent, is not a descendant.
	if ok, err := fix.Descends(ctx, stale); ok || err != nil {
		t.Fatalf("the head's parent descends from it: %v %v", ok, err)
	}
}

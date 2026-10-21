package gitlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommitsListsTheRangeOldestFirst(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "checkout", "-q", "feature")
	write(t, f.dir, "second.txt", "two\n", 0o644)
	git(t, f.dir, "add", "-A")
	git(t, f.dir, "commit", "-q", "-m", "Add second\n\nbody text\n\nCo-Authored-By: Claude <noreply@anthropic.com>\nReviewed-by: Ada <ada@example.com>")
	second := git(t, f.dir, "rev-parse", "HEAD")
	git(t, f.dir, "checkout", "-q", "main")

	r := open(t, f.dir, Options{})
	commits, err := r.Commits(context.Background(), f.base, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].OID != f.head || commits[1].OID != second {
		t.Fatalf("commits = %+v", commits)
	}
	first := commits[0]
	if first.Subject != "head" || first.AuthorName != "Test" || first.AuthorEmail != "test@example.com" || first.CommittedAt == "" {
		t.Errorf("first = %+v", first)
	}
	if len(first.Parents) != 1 || first.Parents[0] != f.base || len(first.Trailers) != 0 {
		t.Errorf("parents or trailers = %+v", first)
	}
	// Both sides of a rename are files the commit touched.
	for _, want := range []string{"mod.txt", "rename_only.txt", "dir/renamed_only.txt", "del.txt", "new.txt"} {
		if !slices.Contains(first.Files, want) {
			t.Errorf("files %v lack %s", first.Files, want)
		}
	}
	if !slices.Equal(commits[1].Files, []string{"second.txt"}) {
		t.Errorf("second files = %v", commits[1].Files)
	}
	wantTrailers := []string{"Co-Authored-By: Claude <noreply@anthropic.com>", "Reviewed-by: Ada <ada@example.com>"}
	if !slices.Equal(commits[1].Trailers, wantTrailers) {
		t.Errorf("trailers = %q", commits[1].Trailers)
	}
	wantMessage := "Add second\n\nbody text\n\nCo-Authored-By: Claude <noreply@anthropic.com>\nReviewed-by: Ada <ada@example.com>"
	if commits[1].Message != wantMessage {
		t.Errorf("message = %q, want %q", commits[1].Message, wantMessage)
	}
	if empty, err := r.Commits(context.Background(), f.head, f.head); err != nil || len(empty) != 0 {
		t.Errorf("empty range = %v, %v", empty, err)
	}
}

func TestCommitsMissingObjects(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	absent := strings.Repeat("a", 40)
	for _, args := range [][2]string{{f.base, absent}, {absent, f.head}} {
		if _, err := r.Commits(ctx, args[0], args[1]); !errors.Is(err, ErrObjectNotFound) {
			t.Errorf("Commits(%v) err = %v, want ErrObjectNotFound", args, err)
		}
	}
	if _, err := r.Commits(ctx, "main", f.head); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a ref name err = %v, want ErrInvalidInput", err)
	}
}

func TestCommitsNeverFetchFromAPartialClone(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	// tree:0 leaves every tree behind, so listing files would need a fetch.
	git(t, f.dir, "clone", "-q", "--filter=tree:0", "--no-checkout", "file://"+filepath.ToSlash(f.dir), partial)
	git(t, partial, "fetch", "-q", "--filter=tree:0", "origin", "feature")
	// Any fetch from here on fails.
	if err := os.Rename(f.dir, f.dir+"-gone"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(f.dir+"-gone", f.dir) })
	before := tree(t, filepath.Join(partial, ".git", "objects"))

	r := open(t, partial, Options{})
	_, err := r.Commits(context.Background(), f.base, f.head)
	if !errors.Is(err, ErrObjectNotFound) && !errors.Is(err, ErrPartialClone) {
		t.Fatalf("err = %v, want a missing-object error", err)
	}
	if tree(t, filepath.Join(partial, ".git", "objects")) != before {
		t.Fatal("objects were fetched")
	}
}

func TestReadsForbidLazyFetching(t *testing.T) {
	env := mirrorEnv(t.TempDir())
	if !slices.Contains(env, "GIT_NO_LAZY_FETCH=1") {
		t.Errorf("mirror environment lacks GIT_NO_LAZY_FETCH=1")
	}
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	config, err := os.ReadFile(filepath.Join(r.mirror, "config"))
	if err != nil || strings.Contains(string(config), "remote") || strings.Contains(string(config), "promisor") {
		t.Errorf("private repository config = %q, %v", config, err)
	}
}

func TestRemoteURL(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	if got, err := r.RemoteURL(context.Background()); err != nil || got != "" {
		t.Fatalf("no origin: %q, %v", got, err)
	}
	git(t, f.dir, "remote", "add", "origin", "git@github.com:acme/widgets.git")
	if got, err := r.RemoteURL(context.Background()); err != nil || got != "git@github.com:acme/widgets.git" {
		t.Fatalf("origin: %q, %v", got, err)
	}
}

package gitlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainRefNamesRoundTripAndRefuseOthers(t *testing.T) {
	for _, repository := range []string{"acme/widgets", "acme/.github", "ghe.example.com:8443/team/app.v2", "a/b%2e"} {
		name := RetainedName{Repository: repository, Number: 7, Version: 3, Role: "merge-base"}
		ref := name.Ref()
		if !validRefName(ref) || strings.Count(ref, "/") != 6 {
			t.Fatalf("%q: ref %q is not one component per field", repository, ref)
		}
		parsed, ok := ParseRetainRef(ref)
		if !ok || parsed != name {
			t.Fatalf("%q: parsed %q as %+v %v", repository, ref, parsed, ok)
		}
	}
	if got := (RetainedName{Repository: "Acme/Widgets", Number: 1, Version: 1, Role: "head"}).Ref(); got != "refs/baum/reviews/acme%2Fwidgets/1/1/head" {
		t.Fatalf("ref = %q", got)
	}
	for _, name := range []string{
		"refs/baum/pull/7/head", "refs/heads/main", "refs/baum/reviews/acme%2Fwidgets/7/1",
		"refs/baum/reviews/acme%2Fwidgets/7/1/tip", "refs/baum/reviews/acme%2Fwidgets/07/1/head",
		"refs/baum/reviews/acme%2Fwidgets/7/0/head", "refs/baum/reviews/Acme%2Fwidgets/7/1/head",
		"refs/baum/reviews/acme%2fwidgets/7/1/head", "refs/baum/reviews/acme.widgets/7/1/head",
		"refs/baum/reviews/acme%2Fwidgets/7/1/head/x", "refs/baum/reviews/junk",
	} {
		if parsed, ok := ParseRetainRef(name); ok {
			t.Fatalf("%q parsed as %+v", name, parsed)
		}
	}
}

func TestUpdateRetainedWritesOnlyItsNamespaceWithoutHooks(t *testing.T) {
	f := newFixture(t)
	// A reference-transaction hook, in the default directory and in one the
	// clone's configuration names, would record any ref change it saw.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\necho ran >> '" + marker + "'\n"
	write(t, f.dir, ".git/hooks/reference-transaction", hook, 0o755)
	write(t, f.dir, "custom-hooks/reference-transaction", hook, 0o755)
	git(t, f.dir, "config", "core.hooksPath", filepath.Join(f.dir, "custom-hooks"))
	git(t, f.dir, "config", "core.logAllRefUpdates", "always")
	repo, err := Open(context.Background(), f.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	ctx := context.Background()
	for _, u := range []RefUpdate{
		{Name: "refs/heads/main", New: f.head}, {Name: "refs/baum/pull/7/head", New: f.head},
		{Name: RetainPrefix + "junk", New: f.head}, {Name: RetainedName{"acme/widgets", 7, 1, "head"}.Ref()},
		{Name: RetainedName{"acme/widgets", 7, 1, "head"}.Ref(), New: "HEAD"},
	} {
		if err := repo.UpdateRetained(ctx, []RefUpdate{u}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%+v: err = %v", u, err)
		}
	}
	head := RetainedName{"acme/widgets", 7, 1, "head"}.Ref()
	base := RetainedName{"acme/widgets", 7, 1, "base"}.Ref()
	if err := repo.UpdateRetained(ctx, []RefUpdate{{Name: head, New: f.head}, {Name: base, New: f.base}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RetainedRefs(ctx, RetainPrefix)
	if err != nil || len(got.Refs) != 2 || got.Refs[head] != f.head || got.Refs[base] != f.base {
		t.Fatalf("refs = %v, %v", got, err)
	}
	// create refuses an existing ref, and the whole transaction with it.
	other := RetainedName{"acme/widgets", 7, 2, "head"}.Ref()
	if err := repo.UpdateRetained(ctx, []RefUpdate{{Name: other, New: f.head}, {Name: head, New: f.base}}); err == nil {
		t.Fatal("creating an existing ref succeeded")
	}
	// A delete whose old value is stale changes nothing.
	if err := repo.UpdateRetained(ctx, []RefUpdate{{Name: head, Old: f.base}}); err == nil {
		t.Fatal("deleting with a stale old value succeeded")
	}
	if got, _ := repo.RetainedRefs(ctx, RetainPrefix); len(got.Refs) != 2 {
		t.Fatalf("a failed transaction changed refs: %v", got)
	}
	if err := repo.UpdateRetained(ctx, []RefUpdate{{Name: head, Old: f.head}, {Name: base, Old: f.base, New: f.head}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.RetainedRefs(ctx, RetainPrefix); len(got.Refs) != 1 || got.Refs[base] != f.head {
		t.Fatalf("refs = %v", got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reference-transaction hook ran")
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".git", "logs", "refs", "baum")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reflog was written for a retained ref")
	}
	if _, err := repo.RetainedRefs(ctx, "refs/baum/"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("listing outside the namespace: %v", err)
	}
}

func TestHasCommitsReportsOnlyCommitsTheCloneHolds(t *testing.T) {
	f := newFixture(t)
	repo, err := Open(context.Background(), f.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	tree := git(t, f.dir, "rev-parse", f.head+"^{tree}")
	absent := strings.Repeat("ab", 20)
	got, err := repo.HasCommits(context.Background(), []string{f.head, f.base, f.head, tree, absent, "not-an-id"})
	if err != nil || len(got) != 2 || !got[f.head] || !got[f.base] {
		t.Fatalf("present = %v, %v", got, err)
	}
}

// A symbolic ref planted at a retained name is listed as such, and an update
// or delete of that name never reaches the branch it points at.
func TestUpdateRetainedNeverFollowsASymbolicRef(t *testing.T) {
	f := newFixture(t)
	name := RetainedName{"acme/widgets", 7, 1, "head"}.Ref()
	git(t, f.dir, "symbolic-ref", name, "refs/heads/main")
	repo, err := Open(context.Background(), f.dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	ctx := context.Background()
	got, err := repo.RetainedRefs(ctx, RetainPrefix)
	if err != nil || len(got.Refs) != 0 || !got.Symbolic[name] {
		t.Fatalf("listed %+v, %v", got, err)
	}
	refs := func() string { return git(t, f.dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") }
	before := refs()
	_ = repo.UpdateRetained(ctx, []RefUpdate{{Name: name, Old: f.base, New: f.head}})
	_ = repo.UpdateRetained(ctx, []RefUpdate{{Name: name, Old: f.base}})
	if after := refs(); after != before {
		t.Fatalf("branches changed:\n%s\nwant:\n%s", after, before)
	}
}

package repos

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestAttachFoundNeverMovesARegisteredClone(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	fork := clone(t, "https://github.com/me/app.git")
	git(t, fork, "remote", "add", "upstream", "https://github.com/acme/app.git")
	registered, e := r.Add(ctx, fork, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Pick(ctx, "acme/app", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = r.AttachFound(ctx, fork, "acme/app"); !errors.Is(e, ErrClonedElsewhere) {
		t.Fatalf("error = %v", e)
	}
	// A linked worktree of the registered repository directory is refused too.
	work := t.TempDir() + "/linked"
	git(t, fork, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	git(t, fork, "worktree", "add", "-q", "-b", "w", work)
	if _, e = r.AttachFound(ctx, work, "acme/app"); !errors.Is(e, ErrClonedElsewhere) {
		t.Fatalf("worktree error = %v", e)
	}
	clones, e := r.List(ctx)
	if e != nil || len(clones) != 1 || clones[0].ID != registered.ID || clones[0].Repository != "me/app" {
		t.Fatalf("clones = %+v, %v", clones, e)
	}
	// AddFor keeps its documented behavior and does move it.
	moved, e := r.AddFor(ctx, fork, "acme/app", nil)
	if e != nil || moved.Repository != "acme/app" {
		t.Fatalf("AddFor = %+v, %v", moved, e)
	}
}

func TestAttachFoundRequiresThePickInTheSameTransaction(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/acme/app.git")
	if _, e := r.AttachFound(ctx, p, "acme/app"); !errors.Is(e, ErrNotPicked) {
		t.Fatalf("error = %v", e)
	}
	if list, _ := r.Repositories(ctx); len(list) != 0 {
		t.Fatalf("an attach picked %+v", list)
	}
	if _, e := r.Pick(ctx, "acme/app", nil); e != nil {
		t.Fatal(e)
	}
	c, e := r.AttachFound(ctx, p, "acme/app")
	if e != nil || c.Repository != "acme/app" {
		t.Fatalf("attach = %+v, %v", c, e)
	}
	// The same clone again is not "elsewhere".
	if _, e = r.AttachFound(ctx, p, "acme/app"); e != nil {
		t.Fatal(e)
	}
	if e = r.Unpick(ctx, "acme/app"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.AttachFound(ctx, p, "acme/app"); !errors.Is(e, ErrNotPicked) {
		t.Fatalf("after unpick: %v", e)
	}
	if list, _ := r.Repositories(ctx); len(list) != 0 {
		t.Fatalf("an attach picked again: %+v", list)
	}
}

func TestPickedWithoutClonesAndCloneRowsRunNoGit(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/acme/app.git")
	if _, e := r.Pick(ctx, "acme/none", nil); e != nil {
		t.Fatal(e)
	}
	c, e := r.Add(ctx, p, nil)
	if e != nil {
		t.Fatal(e)
	}
	names, e := r.PickedWithoutClones(ctx)
	if e != nil || len(names) != 1 || names[0] != "acme/none" {
		t.Fatalf("names = %v, %v", names, e)
	}
	// The rows come back even when the working tree is gone.
	if e = os.RemoveAll(p); e != nil {
		t.Fatal(e)
	}
	rows, e := r.CloneRows(ctx)
	if e != nil || len(rows) != 1 || rows[0].Path != c.Path || rows[0].CommonDir != c.CommonDir {
		t.Fatalf("rows = %+v, %v", rows, e)
	}
}

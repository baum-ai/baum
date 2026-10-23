package repos

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPickReturnsOnlyItsOwnClones(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Add(ctx, clone(t, "https://github.com/owner/a"), nil); e != nil {
		t.Fatal(e)
	}
	other, e := r.Pick(ctx, "owner/b", nil)
	if e != nil || len(other.Clones) != 0 {
		t.Fatalf("%#v %v", other, e)
	}
	same, e := r.Pick(ctx, "owner/a", nil)
	if e != nil || len(same.Clones) != 1 || same.Clones[0].Repository != "owner/a" || !same.Clones[0].Available {
		t.Fatalf("%#v %v", same, e)
	}
}

func TestOpenReadOnlyPickedTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	w, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.Pick(ctx, "owner/repo", nil); e != nil {
		t.Fatal(e)
	}
	_ = w.Close()

	r, e := OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 || list[0].Repository != "owner/repo" {
		t.Fatalf("%v %v", list, e)
	}
	if _, ok, e := r.RepositorySettings(ctx, "owner/repo"); e != nil || !ok {
		t.Fatalf("%v %v", ok, e)
	}
	_ = r.Close()

	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("DROP TABLE picked")
	_ = db.Close()
	if e != nil {
		t.Fatal(e)
	}
	r, e = OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	if list, e = r.Repositories(ctx); e != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, e)
	}
	if _, ok, e := r.RepositorySettings(ctx, "owner/repo"); e != nil || ok {
		t.Fatalf("%v %v", ok, e)
	}
}

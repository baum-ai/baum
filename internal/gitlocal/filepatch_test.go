package gitlocal

import (
	"context"
	"testing"
)

func TestChangedFilesListsRenamesDeletionsAndSubmodules(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "checkout", "-q", "feature")
	git(t, f.dir, "update-index", "--add", "--cacheinfo", "160000,"+f.base+",vendor/sub")
	git(t, f.dir, "commit", "-q", "-m", "Add a submodule")
	head := git(t, f.dir, "rev-parse", "HEAD")
	git(t, f.dir, "checkout", "-q", "main")

	r := open(t, f.dir, Options{})
	files, err := r.ChangedFiles(context.Background(), f.base, head)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ChangedFile{
		"mod.txt":              {Path: "mod.txt"},
		"dir/renamed_only.txt": {Path: "dir/renamed_only.txt", OldPath: "rename_only.txt"},
		"renamed_edit.txt":     {Path: "renamed_edit.txt", OldPath: "rename_edit.txt"},
		"del.txt":              {Path: "del.txt"},
		"new.txt":              {Path: "new.txt"},
		"bin.dat":              {Path: "bin.dat"},
		"mode.sh":              {Path: "mode.sh"},
		"vendor/sub":           {Path: "vendor/sub", Submodule: true},
	}
	if len(files) != len(want) {
		t.Errorf("files = %+v", files)
	}
	for path, change := range want {
		if got := files[path]; got != change {
			t.Errorf("%s = %+v, want %+v", path, got, change)
		}
	}
	// A path a rename left behind, and one nothing touched, are not listed.
	for _, path := range []string{"rename_only.txt", "keep.txt"} {
		if _, listed := files[path]; listed {
			t.Errorf("%s is listed", path)
		}
	}
}

func TestChangedFilesRejectsWhatIsNotACommitId(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	if _, err := r.ChangedFiles(context.Background(), "main", f.head); err == nil {
		t.Error("a ref name was accepted")
	}
}

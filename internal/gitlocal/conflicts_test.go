package gitlocal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A pull request that merges cleanly lists no files, and the base is the
// branch tip the remote has now, not the one the clone saw.
func TestConflictsCleanMerge(t *testing.T) {
	f := newRemoteFixture(t)
	r := open(t, f.clone, Options{})
	base, paths, err := r.Conflicts(context.Background(), "", 7, f.head, "main")
	if err != nil || len(paths) != 0 || base != f.base {
		t.Fatalf("Conflicts = %s, %v, %v; want %s and no files", base, paths, err, f.base)
	}
	// The head was missing from the clone and has been fetched.
	if got := git(t, f.clone, "rev-parse", "refs/baum/pull/7/head"); got != f.head {
		t.Fatalf("head ref = %s, want %s", got, f.head)
	}
}

// A base that changed the same lines lists the file once, and the base moving
// on is seen by the next call.
func TestConflictsListsConflictedFiles(t *testing.T) {
	f := newRemoteFixture(t)
	// main edits the line the pull request appended after.
	write(t, f.remote, "a.txt", lines("a", 5)+"main's line\n", 0o644)
	write(t, f.remote, "dir/other.txt", "unrelated\n", 0o644)
	tip := commit(t, f.remote, "main edits a.txt")
	r := open(t, f.clone, Options{})
	base, paths, err := r.Conflicts(context.Background(), "", 7, f.head, "refs/heads/main")
	if err != nil || base != tip || !reflect.DeepEqual(paths, []string{"a.txt"}) {
		t.Fatalf("Conflicts = %s, %v, %v; want %s and a.txt", base, paths, err, tip)
	}
	// The conflict is gone once main has what the pull request has.
	git(t, f.remote, "update-ref", "refs/heads/main", f.head)
	base, paths, err = r.Conflicts(context.Background(), "", 7, f.head, "main")
	if err != nil || len(paths) != 0 || base != f.head {
		t.Fatalf("Conflicts = %s, %v, %v; want %s and no files", base, paths, err, f.head)
	}
}

func TestConflictsRefusesBadInput(t *testing.T) {
	f := newRemoteFixture(t)
	r := open(t, f.clone, Options{})
	for name, call := range map[string]func() error{
		"number": func() error { _, _, err := r.Conflicts(context.Background(), "", 0, f.head, "main"); return err },
		"head":   func() error { _, _, err := r.Conflicts(context.Background(), "", 7, "main", "main"); return err },
		"base":   func() error { _, _, err := r.Conflicts(context.Background(), "", 7, f.head, "a:b"); return err },
		"remote": func() error { _, _, err := r.Conflicts(context.Background(), "-x", 7, f.head, "main"); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestConflictPaths(t *testing.T) {
	out := []byte(strings.Repeat("a", 40) + "\x00a.txt\x00dir/b.txt\x00a.txt\x00")
	if got, want := conflictPaths(out), []string{"a.txt", "dir/b.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("conflictPaths = %v, want %v", got, want)
	}
}

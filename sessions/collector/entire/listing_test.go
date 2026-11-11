package entire

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
)

// counted runs f and returns how far each workstats counter moved.
func counted(t *testing.T, f func()) map[string]int64 {
	t.Helper()
	before := workstats.Take()
	f()
	moved := map[string]int64{}
	for _, d := range workstats.Take().Since(before) {
		moved[d.Name] = d.Count
	}
	return moved
}

// answers is everything a Listing answers about one store.
type answers struct {
	List     []Checkpoint
	Touched  map[string]time.Time
	Sessions map[string][]Snapshot
	Located  map[string]bool
}

var askedIDs = []string{migrated, ulidID, branchID, "01ARZ3NDEKTSV4RRFFQ69G5FAX", "0000000000aa"}

func ask(t *testing.T, l *Listing, private string, store Store) answers {
	t.Helper()
	ctx := context.Background()
	var a answers
	var err error
	if a.List, err = l.List(ctx, private, store); err != nil {
		t.Fatal(err)
	}
	if a.Touched, err = l.Touched(ctx, private, store); err != nil {
		t.Fatal(err)
	}
	if a.Sessions, err = l.Sessions(ctx, private, store, askedIDs); err != nil {
		t.Fatal(err)
	}
	if a.Located, err = l.Located(ctx, private, store, askedIDs); err != nil {
		t.Fatal(err)
	}
	return a
}

// fresh is what List, Touched, Sessions and Located, which remember
// nothing, answer.
func fresh(t *testing.T, private string, store Store) answers {
	t.Helper()
	ctx := context.Background()
	var a answers
	var err error
	if a.List, err = List(ctx, private, store); err != nil {
		t.Fatal(err)
	}
	if a.Touched, err = Touched(ctx, private, store); err != nil {
		t.Fatal(err)
	}
	if a.Sessions, err = Sessions(ctx, private, store, askedIDs); err != nil {
		t.Fatal(err)
	}
	if a.Located, err = Located(ctx, private, store, askedIDs); err != nil {
		t.Fatal(err)
	}
	return a
}

func resolved(t *testing.T, dir string) Store {
	t.Helper()
	store, _, err := Resolve(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// A second look at stores that did not move runs no git, and answers as a
// first look does.
func TestListingRunsNoGitForStoresThatDidNotMove(t *testing.T) {
	dir, _ := migratedRepo(t)
	private := privateRepo(t, dir)
	store := resolved(t, dir)
	l := &Listing{}

	var first answers
	if moved := counted(t, func() { first = ask(t, l, private, store) }); moved["git.processes"] == 0 {
		t.Fatal("the first look ran no git")
	}
	if want := fresh(t, private, store); !reflect.DeepEqual(first, want) {
		t.Fatalf("listing answered\n%+v\nwant\n%+v", first, want)
	}
	if len(first.Sessions) != 2 || !first.Located[branchID] || first.Touched[ulidID].IsZero() {
		t.Fatalf("the fixture is not what the test expects: %+v", first)
	}

	var second answers
	if moved := counted(t, func() { second = ask(t, l, private, store) }); moved["git.processes"] != 0 {
		t.Errorf("the second look ran git: %v", moved)
	}
	if !reflect.DeepEqual(second, first) {
		t.Errorf("second look\n%+v\nwant\n%+v", second, first)
	}
}

// When the branch only moved forward, its dates come from the new commits
// alone, merged into those already read.
func TestListingDatesOnlyTheBranchsNewCommits(t *testing.T) {
	dir := checkpointRepo(t)
	private := privateRepo(t, dir)
	l := &Listing{}
	ask(t, l, private, resolved(t, dir))

	const later = "2030-01-01T12:00:00Z"
	gitIn(t, dir, later, "checkout", "-q", CheckpointBranch)
	for name, content := range map[string]string{
		"ff/0123456789/metadata.json": `{"checkpoint_id":"ff0123456789","again":true}`,
		"aa/0123456789/metadata.json": `{"checkpoint_id":"aa0123456789"}`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, later, "add", ".")
	gitIn(t, dir, later, "commit", "-q", "-m", "later")
	gitIn(t, dir, later, "checkout", "-q", "main")

	store := resolved(t, dir)
	var got answers
	moved := counted(t, func() { got = ask(t, l, private, store) })
	if moved["entire.listing.dates_extended"] != 1 || moved["git.merge-base"] != 1 || moved["git.log"] != 1 {
		t.Errorf("the branch's dates were not read from its new commits alone: %v", moved)
	}
	if want := fresh(t, private, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("listing answered\n%+v\nwant\n%+v", got, want)
	}
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	if !got.Touched["ff0123456789"].Equal(at) || !got.Touched["aa0123456789"].Equal(at) || !got.Touched["abcdef012345"].Before(at) {
		t.Errorf("touched = %v", got.Touched)
	}
}

// A branch rewritten so its old commit is no longer an ancestor is read
// whole: what only the old history named is gone.
func TestListingReadsARewrittenBranchWhole(t *testing.T) {
	dir := checkpointRepo(t)
	private := privateRepo(t, dir)
	l := &Listing{}
	ask(t, l, private, resolved(t, dir))

	rewritten := orphanCommit(t, dir, "rewritten", map[string]string{
		"aa/0123456789/metadata.json": `{"checkpoint_id":"aa0123456789"}`,
	})
	gitIn(t, dir, refDate, "update-ref", "refs/heads/"+CheckpointBranch, rewritten)

	store := resolved(t, dir)
	var got answers
	moved := counted(t, func() { got = ask(t, l, private, store) })
	if moved["entire.listing.dates_extended"] != 0 {
		t.Errorf("a rewritten branch was read as an extension: %v", moved)
	}
	if want := fresh(t, private, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("listing answered\n%+v\nwant\n%+v", got, want)
	}
	if len(got.Touched) != 1 || len(got.List) != 1 || got.List[0].ID != "aa0123456789" {
		t.Errorf("rewritten branch: list %v, touched %v", got.List, got.Touched)
	}
}

// A ref that is added, moved or removed changes the answers, and only the
// refs that changed are asked about.
func TestListingFollowsRefChanges(t *testing.T) {
	dir, _ := migratedRepo(t)
	private := privateRepo(t, dir)
	l := &Listing{}
	ask(t, l, private, resolved(t, dir))

	const added = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	addRef(t, dir, refPrefix+"AX/"+added, map[string]string{
		"metadata.json":   `{"checkpoint_id":"` + added + `","sessions":[{"metadata":"/0/metadata.json"}]}`,
		"0/metadata.json": `{"session_id":"from-added","created_at":"2026-07-03T10:00:00Z"}`,
	})
	store := resolved(t, dir)
	var got answers
	moved := counted(t, func() { got = ask(t, l, private, store) })
	if moved["git.ls-tree"] != 0 {
		t.Errorf("an added ref listed the unchanged branch again: %v", moved)
	}
	if want := fresh(t, private, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("after adding a ref, listing answered\n%+v\nwant\n%+v", got, want)
	}
	if len(got.Sessions[added]) != 1 || got.Sessions[added][0].SessionID != "from-added" || !got.Located[added] || got.Touched[added].IsZero() {
		t.Fatalf("the added ref is missing: %+v", got)
	}

	// The migrated checkpoint's ref moves to a commit naming another session.
	gitIn(t, dir, refDate, "update-ref", refPrefix+"45/"+migrated, orphanCommit(t, dir, "moved", map[string]string{
		"metadata.json":   `{"checkpoint_id":"abcdef012345","sessions":[{"metadata":"/0/metadata.json"}]}`,
		"0/metadata.json": `{"session_id":"from-moved-ref","created_at":"2026-07-04T10:00:00Z"}`,
	}))
	store = resolved(t, dir)
	got = ask(t, l, private, store)
	if want := fresh(t, private, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("after moving a ref, listing answered\n%+v\nwant\n%+v", got, want)
	}
	if s := got.Sessions[migrated]; len(s) != 1 || s[0].SessionID != "from-moved-ref" {
		t.Fatalf("the moved ref's sessions = %+v", s)
	}

	gitIn(t, dir, refDate, "update-ref", "-d", refPrefix+"AX/"+added)
	store = resolved(t, dir)
	moved = counted(t, func() { got = ask(t, l, private, store) })
	if moved["git.processes"] != 0 {
		t.Errorf("a removed ref ran git: %v", moved)
	}
	if want := fresh(t, private, store); !reflect.DeepEqual(got, want) {
		t.Fatalf("after removing a ref, listing answered\n%+v\nwant\n%+v", got, want)
	}
	if _, ok := got.Sessions[added]; ok || got.Located[added] || !got.Touched[added].IsZero() {
		t.Fatalf("the removed ref is still answered for: %+v", got)
	}
	for _, checkpoint := range got.List {
		if strings.EqualFold(checkpoint.ID, added) {
			t.Fatalf("the removed ref is still listed: %+v", got.List)
		}
	}
}

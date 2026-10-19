package entire

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	ulidID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	migrated  = "abcdef012345" // in the branch and, migrated, in a ref
	branchID  = "ff0123456789" // only in the branch
	refDate   = "2031-02-03T04:05:06Z"
	refPrefix = "refs/entire/checkpoints/"
)

// gitIn runs git in dir with a fixed identity and no user configuration.
func gitIn(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// addRef commits files as the root tree of a new orphan commit and points ref
// at it, leaving main checked out.
func addRef(t *testing.T, dir, ref string, files map[string]string) string {
	t.Helper()
	gitIn(t, dir, refDate, "checkout", "-q", "--orphan", "scratch")
	gitIn(t, dir, refDate, "rm", "-rqf", "--ignore-unmatch", ".")
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, refDate, "add", "-A")
	gitIn(t, dir, refDate, "commit", "-q", "-m", ref)
	commit := gitIn(t, dir, refDate, "rev-parse", "HEAD")
	gitIn(t, dir, refDate, "update-ref", ref, commit)
	gitIn(t, dir, refDate, "checkout", "-q", "main")
	gitIn(t, dir, refDate, "branch", "-qD", "scratch")
	return commit
}

// migratedRepo is a clone with both stores: the branch holds migrated and
// branchID, and refs hold the migrated checkpoint again (its metadata
// re-serialised, so its tree differs and it names another session) and a
// ULID checkpoint. Refs that are not checkpoints are planted beside them.
func migratedRepo(t *testing.T) (dir string, refCommits map[string]string) {
	t.Helper()
	dir = checkpointRepo(t)
	refCommits = map[string]string{}
	refCommits[migrated] = addRef(t, dir, refPrefix+"45/"+migrated, map[string]string{
		"metadata.json":   `{"checkpoint_id":"abcdef012345","sessions":[{"metadata":"/0/metadata.json"}]}`,
		"0/metadata.json": `{"session_id":"from-ref","created_at":"2026-07-01T10:00:00Z"}`,
	})
	refCommits[ulidID] = addRef(t, dir, refPrefix+"AV/"+ulidID, map[string]string{
		"metadata.json":   `{"checkpoint_id":"` + ulidID + `","sessions":[{"metadata":"/0/metadata.json"}]}`,
		"0/metadata.json": `{"session_id":"from-ulid","created_at":"2026-07-02T10:00:00Z"}`,
	})
	junk := map[string]string{"metadata.json": `{}`}
	addRef(t, dir, refPrefix+"zz/"+ulidID, junk)                    // shard is not the id's last two characters
	addRef(t, dir, refPrefix+"AV/extra/"+ulidID, junk)              // extra path segment
	addRef(t, dir, refPrefix+"ef/not-a-checkpoint", junk)           // not an id
	addRef(t, dir, refPrefix+"45/ABCDEF012345X", junk)              // too long
	addRef(t, dir, refPrefix+"AV/81ARZ3NDEKTSV4RRFFQ69G5FAV", junk) // ULID overflowing its timestamp
	return dir, refCommits
}

func TestResolveReadsPerCheckpointRefs(t *testing.T) {
	dir, refCommits := migratedRepo(t)
	store, ok, err := Resolve(context.Background(), dir)
	if err != nil || !ok {
		t.Fatalf("Resolve = %+v, %v, %v", store, ok, err)
	}
	if store.Branch == "" {
		t.Error("the branch was not found beside the refs")
	}
	if len(store.Refs) != 2 || store.Refs[migrated] != refCommits[migrated] || store.Refs[ulidID] != refCommits[ulidID] {
		t.Errorf("refs = %v, want only the two well-named ones", store.Refs)
	}
}

func TestResolveWithRefsOnly(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, refDate, "init", "-q", "-b", "main")
	gitIn(t, dir, refDate, "commit", "-q", "--allow-empty", "-m", "main")
	commit := addRef(t, dir, refPrefix+"AV/"+ulidID, map[string]string{"metadata.json": `{}`})
	store, ok, err := Resolve(context.Background(), dir)
	if err != nil || !ok || store.Branch != "" || store.Refs[ulidID] != commit {
		t.Fatalf("Resolve = %+v, %v, %v", store, ok, err)
	}
}

func TestListMergesBothStoresWithTheRefWinning(t *testing.T) {
	ctx := context.Background()
	dir, refCommits := migratedRepo(t)
	store, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := List(ctx, privateRepo(t, dir), store)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	trees := map[string]string{}
	for _, checkpoint := range checkpoints {
		ids = append(ids, checkpoint.ID)
		trees[checkpoint.ID] = checkpoint.TreeOID
	}
	if want := []string{ulidID, migrated, branchID}; !slices.Equal(ids, want) {
		t.Fatalf("List ids = %v, want %v", ids, want)
	}
	if want := gitIn(t, dir, refDate, "rev-parse", refCommits[migrated]+"^{tree}"); trees[migrated] != want {
		t.Errorf("the migrated checkpoint's tree = %s, want its ref's %s", trees[migrated], want)
	}
	if branchTree := gitIn(t, dir, refDate, "rev-parse", CheckpointBranch+":ab/cdef012345"); trees[migrated] == branchTree {
		t.Error("the migrated checkpoint was listed from the branch")
	}
	if trees[ulidID] == "" || trees[branchID] == "" {
		t.Errorf("trees = %v", trees)
	}
}

func TestReadFromRefsAndBranch(t *testing.T) {
	ctx := context.Background()
	dir, _ := migratedRepo(t)
	store, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	objects := privateRepo(t, dir)

	for _, tc := range []struct{ id, file, want string }{
		{migrated, "0/metadata.json", "from-ref"},
		{strings.ToUpper(migrated), "0/metadata.json", "from-ref"},
		{ulidID, "0/metadata.json", "from-ulid"},
		{strings.ToLower(ulidID), "0/metadata.json", "from-ulid"},
		{branchID, "metadata.json", branchID},
	} {
		fsys, err := Read(ctx, objects, store, tc.id)
		if err != nil {
			t.Errorf("Read(%s): %v", tc.id, err)
			continue
		}
		data, err := fs.ReadFile(fsys, tc.file)
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Errorf("Read(%s) %s = %s, %v; want it to hold %q", tc.id, tc.file, data, err, tc.want)
		}
	}

	_, err = Read(ctx, objects, store, "01ARZ3NDEKTSV4RRFFQ69G5FAW")
	if err == nil || !strings.Contains(err.Error(), FetchHint) {
		t.Errorf("Read of a ULID checkpoint with no ref = %v, want an error naming %q", err, FetchHint)
	}
	// A ULID is never in the branch, whatever the branch holds.
	if _, err := Read(ctx, objects, Store{Branch: store.Branch}, ulidID); err == nil {
		t.Error("a ULID was read from the branch")
	}
}

func TestSessionsFromRefsAndBranch(t *testing.T) {
	ctx := context.Background()
	dir, _ := migratedRepo(t)
	store, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Sessions(ctx, privateRepo(t, dir), store, []string{strings.ToUpper(migrated), strings.ToLower(ulidID), branchID, "0000000000aa", "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Sessions = %+v, want the two checkpoints that record sessions", got)
	}
	if s := got[migrated]; len(s) != 1 || s[0].SessionID != "from-ref" {
		t.Errorf("migrated checkpoint = %+v, want the ref's session, not the branch's", s)
	}
	if s := got[ulidID]; len(s) != 1 || s[0].SessionID != "from-ulid" {
		t.Errorf("ULID checkpoint = %+v", s)
	}
}

func TestTouchedDatesRefsByTheirCommit(t *testing.T) {
	ctx := context.Background()
	dir, _ := migratedRepo(t)
	store, _, err := Resolve(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	touched, err := Touched(ctx, privateRepo(t, dir), store)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	if !touched[migrated].Equal(want) || !touched[ulidID].Equal(want) {
		t.Errorf("touched = %v, want the refs dated %v", touched, want)
	}
	if first := touched[branchID]; first.IsZero() || !first.Before(want) {
		t.Errorf("the branch-only checkpoint = %v, want its own earlier date", first)
	}
}

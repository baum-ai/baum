package sessions

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/collector/entire"
)

const refULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// The sessions addRefCheckpoint gives a checkpoint when it is told to rename
// them.
const (
	refFirstSession  = "bbbbbbbb-1111-4000-8000-000000000001"
	refSecondSession = "bbbbbbbb-1111-4000-8000-000000000002"
)

// addRefCheckpoint adds the fixture checkpoint to the git-refs store as
// refs/entire/checkpoints/<last two of id>/<id>: a commit whose root tree is
// the checkpoint, its session paths without a shard prefix. renamed gives the
// two sessions new ids so a test can tell the copies apart. The clone is left
// on main.
func addRefCheckpoint(t *testing.T, fx fixture, id string, renamed bool) {
	t.Helper()
	src := filepath.Join("entirecheckpoint", "testdata", "two_claude")
	git(t, fx.repo, "checkout", "-q", "--orphan", "scratch")
	git(t, fx.repo, "rm", "-rqf", "--ignore-unmatch", ".")
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "/00/04a7a444a0/", "/")
		text = strings.ReplaceAll(text, "0004a7a444a0", id)
		if renamed {
			text = strings.ReplaceAll(text, firstSession, refFirstSession)
			text = strings.ReplaceAll(text, secondSession, refSecondSession)
		}
		write(t, filepath.Join(fx.repo, rel), text)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, fx.repo, "add", ".")
	git(t, fx.repo, "commit", "-q", "-m", "checkpoint "+id)
	git(t, fx.repo, "update-ref", entire.RefPrefix+id[len(id)-2:]+"/"+id, "HEAD")
	git(t, fx.repo, "checkout", "-q", "main")
	git(t, fx.repo, "branch", "-qD", "scratch")
}

func sessionIDs(sessions []Session) []string {
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

// A clone whose only checkpoints are per-checkpoint refs, with a ULID id the
// branch cannot hold, lists the sessions they record.
func TestLoadListsEntireSessionsFromRefs(t *testing.T) {
	fx := newFixture(t)
	git(t, fx.repo, "branch", "-qD", entire.CheckpointBranch)
	addRefCheckpoint(t, fx, refULID, false)

	sessions, report := loadEntireOnly(t, fx)
	if len(report.Failures) != 0 {
		t.Errorf("failures = %+v", report.Failures)
	}
	if want := []string{firstSession, secondSession}; !slices.Equal(sessionIDs(sessions), want) {
		t.Fatalf("sessions = %v, want %v", sessionIDs(sessions), want)
	}
	for _, s := range sessions {
		if s.Path != refULID || !HasTranscript(s.Graph) {
			t.Errorf("%s read from %s (transcript %v), want the ref's checkpoint with its transcript", s.ID, s.Path, HasTranscript(s.Graph))
		}
	}
	if report.EntireCommit != "" {
		t.Errorf("EntireCommit = %q; the clone has no checkpoint branch", report.EntireCommit)
	}
}

// A migrated clone holds the same 12-hex checkpoint in the branch and in a
// ref, and the ref is the one read; a ULID checkpoint beside them is read too.
func TestLoadReadsTheRefOfAMigratedCheckpoint(t *testing.T) {
	fx := newFixture(t)
	addRefCheckpoint(t, fx, "0004a7a444a0", true)

	sessions, report := loadEntireOnly(t, fx)
	if len(report.Failures) != 0 {
		t.Errorf("failures = %+v", report.Failures)
	}
	if want := []string{refFirstSession, refSecondSession}; !slices.Equal(sessionIDs(sessions), want) {
		t.Fatalf("sessions = %v, want the ref's %v, not the branch copy's", sessionIDs(sessions), want)
	}

	// Both stores, side by side, distinct checkpoints.
	both := newFixture(t)
	addRefCheckpoint(t, both, refULID, true)
	sessions, report = loadEntireOnly(t, both)
	if len(report.Failures) != 0 {
		t.Errorf("failures = %+v", report.Failures)
	}
	want := []string{firstSession, secondSession, refFirstSession, refSecondSession}
	slices.Sort(want)
	if !slices.Equal(sessionIDs(sessions), want) {
		t.Errorf("sessions = %v, want %v", sessionIDs(sessions), want)
	}
	if report.EntireCommit == "" {
		t.Error("EntireCommit is empty although the clone has the branch")
	}
}

// OpenCheckpoints answers for ULIDs, in any case, from refs, and for 12-hex
// ids from the branch, and reports nothing for a checkpoint in neither.
func TestOpenCheckpointsFindsRefAndBranchCheckpoints(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	addRefCheckpoint(t, fx, refULID, true)

	checkpoints, err := OpenCheckpoints(ctx, fx.repo)
	if err != nil || checkpoints == nil {
		t.Fatalf("OpenCheckpoints = %v, %v", checkpoints, err)
	}
	defer checkpoints.Close()
	got, err := checkpoints.Sessions(ctx, []string{strings.ToLower(refULID), "0004A7A444A0", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "ffffffffffff"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !slices.Equal(got[refULID], []string{refFirstSession, refSecondSession}) ||
		!slices.Equal(got["0004a7a444a0"], []string{firstSession, secondSession}) {
		t.Errorf("Sessions = %v", got)
	}
	loaded, failures, err := checkpoints.Load(ctx, strings.ToLower(refULID))
	if err != nil || len(failures) != 0 || !slices.Equal(sessionIDs(loaded), []string{refFirstSession, refSecondSession}) {
		t.Errorf("Load = %v, %v, %v", sessionIDs(loaded), failures, err)
	}
	if _, _, err := checkpoints.Load(ctx, "not-an-id"); err == nil {
		t.Error("Load accepted a malformed id")
	}
}

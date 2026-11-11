package reviewruns

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/store"
)

// codexSessionOn writes a rollout in the dated folder of day.
func (h homes) codexSessionOn(t *testing.T, cwd, id string, day time.Time) string {
	line := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}`+"\n", day.Format(time.RFC3339), id, cwd)
	name := "rollout-" + day.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
	return write(t, filepath.Join(h.codex, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"), name), line)
}

func TestRemoveWithACompleteRecordDeletesByIDWithoutScanning(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	recorded := h.codexSessionOn(t, run, "a1", now)
	recordedYesterday := h.codexSessionOn(t, run, "a2", now.AddDate(0, 0, -1))
	// Not recorded: a complete record means no scan, so this one stays. It
	// stands for a session that is not the run's.
	unrecorded := h.codexSessionOn(t, run, "a3", now)
	// Recorded id but another cwd: never removed.
	foreign := h.codexSessionOn(t, "/work/elsewhere", "a4", now)
	// Older than the run: never even listed.
	older := h.codexSessionOn(t, run, "a5", now.AddDate(0, 0, -30))
	if err := Record(root, run, true, "a1", "a2", "a4", "a5"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{recorded: false, recordedYesterday: false, unrecorded: true, foreign: true, older: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
	if exists(manifestPath(root, run)) || exists(run) {
		t.Error("the run directory or its record stayed")
	}
}

func TestRemoveWithAnIncompleteRecordAlsoScansNewestFirst(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	recorded := h.codexSessionOn(t, run, "b1", now)
	fork := h.codexSessionOn(t, run, "b2", now)
	other := h.codexSessionOn(t, "/work/elsewhere", "b3", now)
	if err := Record(root, run, false, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{recorded: false, fork: false, other: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
}

func TestMarkIncompleteLowersACompleteRecord(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	fork := h.codexSessionOn(t, run, "c2", time.Now())
	if err := Record(root, run, true, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIncomplete(root, run); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	if exists(fork) {
		t.Error("the unrecorded fork stayed after the record was marked incomplete")
	}
}

func TestRecordRejectsBadIDsAndForeignDirectories(t *testing.T) {
	fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(root, run, true, "../x"); err == nil {
		t.Error("an invalid id was accepted")
	}
	elsewhere := t.TempDir()
	if err := Record(root, filepath.Join(elsewhere, agent.RunDirPrefix+"1"), true, "ok"); err != nil || exists(manifestPath(root, filepath.Join(elsewhere, agent.RunDirPrefix+"1"))) {
		t.Errorf("a directory outside the root was recorded: %v", err)
	}
}

func TestPruneSkipsRunsAlreadyPruned(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	if !pruned(root, run) {
		t.Fatal("a removed run with no record is not seen as pruned")
	}
	// A session that shows up for the gone directory is not scanned for.
	late := h.codexSessionOn(t, run, "d1", time.Now())
	if err := PruneVersions(root, []store.Metadata{session("/x"), session("/y"), session(run)}); err != nil {
		t.Fatal(err)
	}
	if !exists(late) {
		t.Error("an already pruned run was scanned again")
	}
}

func TestExpireRemovesSessionsOfARecordWhoseDirectoryIsGone(t *testing.T) {
	h := fakeHomes(t)
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	path := h.codexSessionOn(t, run, "e1", time.Now())
	if err := Record(root, run, true, "e1"); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(run); err != nil {
		t.Fatal(err)
	}
	if err := Expire(root, time.Now().Add(2*MaxAge), MaxAge); err != nil {
		t.Fatal(err)
	}
	if exists(path) || exists(manifestPath(root, run)) {
		t.Error("the orphaned record's session or the record stayed")
	}
}

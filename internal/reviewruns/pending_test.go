package reviewruns

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/store"
)

func newRun(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runs")
	run, err := Create(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, run
}

func TestAChatTurnThatLearnsItsSessionKeepsTheRecordComplete(t *testing.T) {
	h := fakeHomes(t)
	root, run := newRun(t)
	review := h.codexSessionOn(t, run, "f1", time.Now())
	stranger := h.codexSessionOn(t, run, "f9", time.Now())
	if err := Record(root, run, true, "f1"); err != nil {
		t.Fatal(err)
	}
	// A chat turn: pending while it runs, recorded once its id is known.
	if err := MarkIncomplete(root, run, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := readManifest(root, run); !m.Pending {
		t.Fatal("the record is not pending during the call")
	}
	fork := h.codexSessionOn(t, run, "f2", time.Now())
	if err := Record(root, run, false, "f2"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{review: false, fork: false, stranger: true} {
		if exists(path) != want {
			t.Errorf("%s exists = %v, want %v", path, !want, want)
		}
	}
}

func TestAPendingRecordFallsBackToAScan(t *testing.T) {
	h := fakeHomes(t)
	root, run := newRun(t)
	orphan := h.codexSessionOn(t, run, "g2", time.Now())
	if err := Record(root, run, true, "g1"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIncomplete(root, run, false); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	if exists(orphan) {
		t.Error("a session of a call that never reported its id stayed")
	}
}

func TestPruneListsASharedDirectoryOnce(t *testing.T) {
	h := fakeHomes(t)
	root, run := newRun(t)
	recorded := h.codexSessionOn(t, run, "h1", time.Now())
	stranger := h.codexSessionOn(t, run, "h2", time.Now())
	if err := Record(root, run, true, "h1"); err != nil {
		t.Fatal(err)
	}
	versions := []store.Metadata{session("/x"), session("/y"), session(run), session(run)}
	if err := PruneVersions(root, versions); err != nil {
		t.Fatal(err)
	}
	// A second removal would find no record and scan.
	if exists(recorded) || !exists(stranger) {
		t.Errorf("recorded stayed %v, unrecorded stayed %v", exists(recorded), exists(stranger))
	}
}

func TestOnlyFilesNamedByIDAreMatchedPastTheScanCap(t *testing.T) {
	h := fakeHomes(t)
	root, run := newRun(t)
	now := time.Now()
	byID := h.codexSessionOn(t, run, "i1", now)
	scanned := h.codexSessionOn(t, run, "i2", now.Add(time.Hour))
	if err := Record(root, run, false, "i1"); err != nil {
		t.Fatal(err)
	}
	old := maxRollouts
	maxRollouts = 0
	t.Cleanup(func() { maxRollouts = old })
	if err := Remove(root, run); err != nil {
		t.Fatal(err)
	}
	if exists(byID) || !exists(scanned) {
		t.Errorf("by id stayed %v, scanned stayed %v", exists(byID), exists(scanned))
	}
}

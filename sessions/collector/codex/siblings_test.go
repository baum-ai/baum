package codex

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Codex may delete the plain rollout after the walk saw it beside its
// compressed copy: the copy is read instead, and the thread is not lost.
func TestScanReadsTheCompressedCopyOfARolloutDeletedAfterTheWalk(t *testing.T) {
	s := newStore(t)
	day := filepath.Join(s.dir, "2026", "08", "01")
	plain := filepath.Join(day, "rollout-root.jsonl")
	packed := plain + ".zst"
	writeFile(t, packed, zstdOf(t, s.root("root")))

	paths, siblings := chooseRollouts([]string{plain, packed})
	if !slices.Equal(paths, []string{plain}) || siblings[plain] != packed {
		t.Fatalf("paths %v, siblings %v; want the plain rollout with its copy", paths, siblings)
	}
	if err := os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	scan := (*Cache)(nil).beginScan(filepath.Dir(s.dir))
	entry, err := scan.readOrSibling(plain, siblings)
	if err != nil || entry.Path != packed || entry.Meta.ID != "root" {
		t.Fatalf("entry %+v, %v; want the compressed copy", entry, err)
	}
	if _, err := scan.readOrSibling(plain, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("without a copy: %v", err)
	}
}

// The partial index reports a corrupt compressed rollout as the scan does.
func TestPartialIndexReportsCorruptCompressedRollouts(t *testing.T) {
	s := newStore(t)
	day := filepath.Join(s.dir, "2026", "08", "01")
	writeFile(t, filepath.Join(day, "rollout-dup.jsonl"), s.root("root"))
	bad := filepath.Join(day, "rollout-bad.jsonl.zst")
	writeFile(t, bad, "garbage")
	_, report, err := FindSessionsCached(s.dir, []string{s.repo}, &Cache{})
	if err != nil {
		t.Fatal(err)
	}
	for _, skipped := range report.Skipped {
		if skipped.Path == bad && skipped.Reason == unreadableReason {
			return
		}
	}
	t.Fatalf("report %+v, want %s skipped as %q", report, bad, unreadableReason)
}

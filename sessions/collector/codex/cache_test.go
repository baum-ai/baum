package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type store struct {
	repo, dir string
}

func newStore(t *testing.T) store {
	t.Helper()
	base := t.TempDir()
	s := store{repo: filepath.Join(base, "repo"), dir: filepath.Join(base, ".codex", "sessions")}
	day := filepath.Join(s.dir, "2026", "08", "01")
	writeFile(t, filepath.Join(day, "rollout-root.jsonl"), s.root("root"))
	writeFile(t, filepath.Join(day, "rollout-child.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-08-01T12:01:00Z","type":"session_meta","payload":{"id":"child","session_id":"root","cwd":%q,"parent_thread_id":"root","source":{"subagent":{"thread_spawn":{"parent_thread_id":"root","depth":1,"agent_path":"worker"}}}}}
`, s.repo))
	writeFile(t, filepath.Join(day, "rollout-other.jsonl"), rollout("other", filepath.Join(filepath.Dir(s.repo), "elsewhere")))
	return s
}

func (s store) root(id string) string {
	return fmt.Sprintf(`{"timestamp":"2026-08-01T12:00:00Z","type":"session_meta","payload":{"id":%q,"session_id":%q,"cwd":%q,"source":"cli"}}
`, id, id, s.repo)
}

// sameSessions fails unless the cached and uncached discovery agree.
func (s store) sameSessions(t *testing.T, cache *Cache) {
	t.Helper()
	want, wantReport, err := FindSessions(s.dir, []string{s.repo})
	if err != nil {
		t.Fatal(err)
	}
	got, gotReport, err := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotReport, wantReport) {
		t.Fatalf("cached discovery differs:\ngot  %+v %+v\nwant %+v %+v", got, gotReport, want, wantReport)
	}
}

func TestCacheMatchesUncachedAcrossChanges(t *testing.T) {
	s := newStore(t)
	cache := &Cache{}
	day := filepath.Join(s.dir, "2026", "08", "01")
	s.sameSessions(t, cache)
	s.sameSessions(t, cache)

	// A rollout grows: it is read again, and its new time is reported.
	path := filepath.Join(day, "rollout-root.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"timestamp":"2026-08-01T12:05:00Z","type":"event_msg","payload":{"type":"user_message","message":"more"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	s.sameSessions(t, cache)

	// A rollout whose first record changes at the same size is told apart by
	// its time.
	later := time.Now().Add(time.Hour)
	writeFile(t, path, s.root("rooX"))
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	s.sameSessions(t, cache)

	// A rollout is added.
	writeFile(t, filepath.Join(day, "rollout-new.jsonl"), s.root("new"))
	s.sameSessions(t, cache)
	got, _, _ := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if len(got) != 2 {
		t.Fatalf("sessions = %d, want 2", len(got))
	}

	// A rollout is deleted, and the cache lets go of it.
	if err := os.Remove(filepath.Join(day, "rollout-new.jsonl")); err != nil {
		t.Fatal(err)
	}
	s.sameSessions(t, cache)
	if cache.Len() != 3 {
		t.Fatalf("cache holds %d rollouts, want the 3 left", cache.Len())
	}
	got, _, _ = FindSessionsCached(s.dir, []string{s.repo}, cache)
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1", len(got))
	}
}

func TestCacheOpensUnchangedRolloutsOnce(t *testing.T) {
	s := newStore(t)
	cache := &Cache{}
	for range 3 {
		if _, _, err := FindSessionsCached(s.dir, []string{s.repo}, cache); err != nil {
			t.Fatal(err)
		}
	}
	if got := cache.Reads(); got != 3 {
		t.Fatalf("reads = %d, want one per rollout", got)
	}
}

func TestPassScansTheStoreOnce(t *testing.T) {
	s := newStore(t)
	cache := &Cache{}
	cache.BeginPass()
	if _, _, err := FindSessionsCached(s.dir, []string{s.repo}, cache); err != nil {
		t.Fatal(err)
	}
	// A rollout added during the pass is not seen by it: the store is scanned
	// once. Another pass, or a call outside one, sees it.
	writeFile(t, filepath.Join(s.dir, "2026", "08", "01", "rollout-new.jsonl"), s.root("new"))
	during, _, _ := FindSessionsCached(s.dir, []string{s.repo}, cache)
	cache.EndPass()
	if len(during) != 1 {
		t.Fatalf("sessions during the pass = %d, want the one scanned", len(during))
	}
	after, _, _ := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if len(after) != 2 {
		t.Fatalf("sessions after the pass = %d, want 2", len(after))
	}
}

// Codex compresses old rollouts to .jsonl.zst and removes the plain file. The
// scan leaves those out and indexes the rest of the store, instead of failing
// and falling back to the partial index.
func TestScanLeavesOutCompressedRollouts(t *testing.T) {
	s := newStore(t)
	day := filepath.Join(s.dir, "2026", "08", "01")
	compressed := filepath.Join(day, "rollout-old.jsonl.zst")
	writeFile(t, compressed, "\x28\xb5\x2f\xfd")
	// A compressed rollout whose plain copy is still there is read as plain.
	writeFile(t, filepath.Join(day, "rollout-root.jsonl.zst"), "\x28\xb5\x2f\xfd")

	entries, report, err := ScanStore(filepath.Dir(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want the 3 plain rollouts", len(entries))
	}
	if !reflect.DeepEqual(report.CompressedPaths, []string{compressed}) {
		t.Fatalf("compressed = %v, want %v", report.CompressedPaths, compressed)
	}
	sessions, scanReport, err := FindSessionsCached(s.dir, []string{s.repo}, &Cache{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Warnings) != 0 || len(scanReport.Skipped) != 0 {
		t.Fatalf("sessions = %+v, report = %+v; want the root session without warnings", sessions, scanReport)
	}
}

// When the full scan fails, the partial index still reads first records
// through the cache: an unchanged rollout is not opened again.
func TestPartialIndexReadsThroughTheCache(t *testing.T) {
	s := newStore(t)
	day := filepath.Join(s.dir, "2026", "08", "01")
	// Two rollouts claiming one thread id fail the full scan.
	writeFile(t, filepath.Join(day, "rollout-dup.jsonl"), s.root("root"))
	cache := &Cache{}
	first, firstReport, err := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !strings.Contains(strings.Join(first[0].Warnings, "\n"), "partial index") {
		t.Fatalf("sessions = %+v, want the partial index warning", first)
	}

	// Garble a rollout without changing its size or time. Only a read from
	// disk would notice.
	path := filepath.Join(day, "rollout-other.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Repeat("x", int(info.Size())))
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	second, secondReport, err := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, first) || !reflect.DeepEqual(secondReport, firstReport) {
		t.Fatalf("second scan differs:\ngot  %+v %+v\nwant %+v %+v", second, secondReport, first, firstReport)
	}
}

func TestTreeFindMatchesFind(t *testing.T) {
	s := newStore(t)
	index, _, err := ScanStore(filepath.Dir(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	tree := NewTree(index)
	for _, e := range index {
		want, wantErr := Find(index, e)
		got, gotErr := tree.Find(e)
		if !reflect.DeepEqual(got, want) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
			t.Errorf("%s: tree = %v %v, find = %v %v", e.Path, got, gotErr, want, wantErr)
		}
	}
}

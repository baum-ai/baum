package sessions

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/entire"
	"github.com/baum-ai/baum/sessions/vendors"
)

func counter(name string) int64 { return workstats.Take().Counters[name] }

// readWork counts transcripts decoded and checkpoints read whole.
func readWork() int64 {
	s := workstats.Take().Counters
	var n int64
	for name, count := range s {
		if strings.HasPrefix(name, "sessions.map.decodes.") {
			n += count
		}
	}
	return n + s["git.archive"]
}

// withoutCaches turns the mapping and checkpoint caches off for the test, so
// that every transcript mapped is decoded and every checkpoint read is read.
func withoutCaches(t *testing.T) {
	t.Helper()
	SetMapCacheLimit(0)
	SetCheckpointCacheLimit(0)
	t.Cleanup(func() {
		SetMapCacheLimit(DefaultMapCacheBytes)
		SetCheckpointCacheLimit(DefaultCheckpointCacheBytes)
	})
}

// settle dates every session file of the fixture an hour back, past
// racyWindow, as a store nothing has written to lately is.
func settle(t *testing.T, fx fixture) {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	for _, dir := range []string{fx.claudeDir, fx.codexDir} {
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			return os.Chtimes(path, at, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// withoutGraph is s as LoadReusing returns it unmapped.
func withoutGraph(s Session) Session {
	s.Graph, s.Unmapped, s.Transcript = nil, true, HasTranscript(s.Graph)
	return s
}

func discover(t *testing.T, fx fixture) *Discovery {
	t.Helper()
	d, err := Discover(context.Background(), fx.repo, fx.options())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A session whose Key the caller accepts is neither decoded nor read, and
// comes back as Load returns it, less its graph.
func TestLoadReusingSkipsAcceptedSessions(t *testing.T) {
	withoutCaches(t)
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	settle(t, fx)
	ctx := context.Background()
	full, report, err := discover(t, fx).Load(ctx)
	if err != nil || len(report.Failures) != 0 {
		t.Fatalf("Load: %v %+v", err, report.Failures)
	}
	transcripts := map[string]bool{}
	for _, s := range full {
		if s.Key == "" {
			t.Errorf("%s %s has no key", s.Source, s.ID)
		}
		transcripts[s.Key] = HasTranscript(s.Graph)
	}
	if !slices.ContainsFunc(full, func(s Session) bool { return s.Source == Entire }) {
		t.Fatal("the fixture has no Entire session")
	}

	before := readWork()
	reused, report, err := discover(t, fx).LoadReusing(ctx, func(key string) (bool, bool) {
		transcript, ok := transcripts[key]
		return transcript, ok
	})
	if err != nil || len(report.Failures) != 0 {
		t.Fatalf("LoadReusing: %v %+v", err, report.Failures)
	}
	if n := readWork() - before; n != 0 {
		t.Errorf("%d transcripts decoded or checkpoints read, want 0", n)
	}
	if len(reused) != len(full) {
		t.Fatalf("sessions = %d, want %d", len(reused), len(full))
	}
	for i := range full {
		want := withoutGraph(full[i])
		// An unmapped session keeps its discovery warnings, not the
		// mapper's notes.
		got := reused[i]
		got.Notes, want.Notes = nil, nil
		if !sameSession(got, want) {
			t.Errorf("session %d = %+v, want %+v", i, got, want)
		}
	}

	// Refused, each is mapped again; a checkpoint is read only for the
	// snapshot chosen from it.
	before = readWork()
	again, _, err := discover(t, fx).LoadReusing(ctx, func(string) (bool, bool) { return false, false })
	if err != nil {
		t.Fatal(err)
	}
	for i := range again {
		if again[i].Unmapped || again[i].Graph == nil || again[i].Key != full[i].Key {
			t.Errorf("session %d = %+v, want it mapped as before", i, again[i])
		}
	}
	native := 0
	for _, s := range full {
		if s.Source != Entire {
			native++
		}
	}
	// Both Entire sessions are chosen from the newer checkpoint.
	if n := readWork() - before; n != int64(native)+1 {
		t.Errorf("%d transcripts decoded or checkpoints read, want %d", n, native+1)
	}
}

func sameSession(a, b Session) bool {
	return a.Source == b.Source && a.Vendor == b.Vendor && a.ID == b.ID && a.Path == b.Path &&
		a.RepoRoot == b.RepoRoot && a.WorktreeRoot == b.WorktreeRoot && a.ModifiedAt.Equal(b.ModifiedAt) &&
		a.Graph == b.Graph && slices.Equal(a.Notes, b.Notes) && a.Key == b.Key &&
		a.Unmapped == b.Unmapped && a.Transcript == b.Transcript
}

// A session file written within racyWindow could be rewritten at the same
// size and time unnoticed, so its session has no Key until it is older.
func TestRecentlyWrittenSessionsHaveNoKey(t *testing.T) {
	fx := newFixture(t)
	opts := fx.options()
	opts.Sources = []vendors.Slug{vendors.ClaudeCode, vendors.Codex}
	load := func() []Session {
		t.Helper()
		found, _, err := Load(context.Background(), fx.repo, opts)
		if err != nil || len(found) == 0 {
			t.Fatalf("Load: %v, %d sessions", err, len(found))
		}
		return found
	}
	for _, s := range load() {
		if s.Key != "" {
			t.Errorf("%s %s, written just now, has key %q", s.Source, s.ID, s.Key)
		}
	}
	settle(t, fx)
	for _, s := range load() {
		if s.Key == "" {
			t.Errorf("%s %s, written an hour ago, has no key", s.Source, s.ID)
		}
	}
	// A modification time in the future is no older.
	at := time.Now().Add(time.Hour)
	if err := filepath.WalkDir(fx.codexDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chtimes(path, at, at)
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range load() {
		if s.Source == vendors.Codex && s.Key != "" {
			t.Errorf("%s %s, dated in the future, has key %q", s.Source, s.ID, s.Key)
		}
	}
}

// Remembered checkpoint facts choose the same snapshot a read would: the
// older snapshot with a transcript over a newer one without, read once.
func TestRememberedCheckpointsChooseAlike(t *testing.T) {
	withoutCaches(t)
	fx := newFixture(t)
	addCheckpoint(t, fx, "222222222222", func(rel, text string) (string, bool) {
		switch rel {
		case "0/full.jsonl", "0/transcript.jsonl", "0/content_hash.txt":
			return "", false
		case "metadata.json":
			text = strings.ReplaceAll(text, `"transcript": "/22/2222222222/0/full.jsonl",`, "")
			text = strings.ReplaceAll(text, `"compact_transcript": "/22/2222222222/0/transcript.jsonl",`, "")
			text = strings.ReplaceAll(text, `"content_hash": "/22/2222222222/0/content_hash.txt",`, "")
		}
		return text, true
	})
	first, _ := loadEntireOnly(t, fx)
	archives := counter("git.archive")
	second, _ := loadEntireOnly(t, fx)
	// Only the checkpoint each chosen snapshot comes from is read: the
	// newer one, for the second session, and the older one, for the first.
	if n := counter("git.archive") - archives; n != 2 {
		t.Errorf("%d checkpoints read, want 2", n)
	}
	if len(first) != len(second) {
		t.Fatalf("sessions = %+v, want %+v", second, first)
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Path != second[i].Path || first[i].HasTranscript() != second[i].HasTranscript() {
			t.Errorf("session %d = %s from %s, want %s from %s", i, second[i].ID, second[i].Path, first[i].ID, first[i].Path)
		}
	}
	if found := byID(second); found[firstSession][0].Path != "0004a7a444a0" {
		t.Errorf("%s chosen from %s, want the older checkpoint with its transcript", firstSession, found[firstSession][0].Path)
	}
}

// listCheckpoints lists the fixture's checkpoints and names its clone root.
func listCheckpoints(t *testing.T, fx fixture) (string, []entire.Checkpoint) {
	t.Helper()
	root, err := collector.RepoRoot(fx.repo)
	if err != nil {
		t.Fatal(err)
	}
	store, ok, err := openCheckpoints(context.Background(), root)
	if err != nil || !ok {
		t.Fatalf("openCheckpoints: %v %v", ok, err)
	}
	defer store.Close()
	checkpoints, err := store.list(context.Background())
	if err != nil || len(checkpoints) == 0 {
		t.Fatalf("checkpoints = %+v, %v", checkpoints, err)
	}
	return root, checkpoints
}

// A checkpoint that can no longer be read is chosen around, as a load that
// remembered nothing would.
func TestRememberedCheckpointThatNoLongerReads(t *testing.T) {
	withoutCaches(t)
	fx := newFixture(t)
	if _, report := loadEntireOnly(t, fx); len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	root, checkpoints := listCheckpoints(t, fx)
	tree := checkpoints[0].TreeOID
	if _, remembered := snapshotFactsCache.get(root, tree); !remembered {
		t.Fatal("the checkpoint's facts were not remembered")
	}
	// The facts are remembered; the store now cannot read the checkpoint:
	// the chosen snapshot must be read, fails, and is reported.
	broken, err := newCheckpointStore(context.Background(), root, entire.Store{Branch: strings.Repeat("0", 40)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	opts := fx.options()
	opts.Sources = []vendors.Slug{Entire}
	plan := snapshotPlan{
		root: root, opts: opts, native: map[string]bool{},
		trees: map[string]string{"0004a7a444a0": tree},
		first: []string{"0004a7a444a0"}, sessionIDs: []string{firstSession},
		picks: map[string][]pick{firstSession: {{checkpoint: "0004a7a444a0"}}},
	}
	got, failures, err := plan.load(context.Background(), broken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(failures) != 1 || failures[0].Path != "0004a7a444a0" {
		t.Errorf("sessions = %+v, failures = %+v; want none and the checkpoint reported", got, failures)
	}
}

// Two clones holding the same checkpoint share neither its facts nor its
// sessions' keys: one clone's reads never answer for another's.
func TestCheckpointFactsBelongToTheirClone(t *testing.T) {
	withoutCaches(t)
	one, other := newFixture(t), newFixture(t)
	rootOne, listedOne := listCheckpoints(t, one)
	rootOther, listedOther := listCheckpoints(t, other)
	if listedOne[0].TreeOID != listedOther[0].TreeOID {
		t.Fatalf("the fixtures' checkpoints differ: %s, %s", listedOne[0].TreeOID, listedOther[0].TreeOID)
	}
	fromOne, _ := loadEntireOnly(t, one)
	if _, remembered := snapshotFactsCache.get(rootOther, listedOther[0].TreeOID); remembered {
		t.Error("one clone's read answered for another")
	}
	keys := map[string]bool{}
	for _, s := range fromOne {
		keys[s.Key] = true
	}
	archives := counter("git.archive")
	fromOther, _, err := discover(t, other).LoadReusing(context.Background(), func(key string) (bool, bool) {
		return true, keys[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter("git.archive") == archives {
		t.Error("the other clone's checkpoint was not read")
	}
	for _, s := range fromOther {
		if s.Source == Entire && (s.Unmapped || keys[s.Key]) {
			t.Errorf("%s from the other clone was reused from the first: key %q", s.ID, s.Key)
		}
	}
	if rootOne == rootOther {
		t.Fatal("the fixtures share a clone")
	}
}

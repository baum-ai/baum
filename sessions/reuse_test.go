package sessions

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/workstats"
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
	SetMapCacheLimit(0)
	t.Cleanup(func() { SetMapCacheLimit(DefaultMapCacheBytes) })
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
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
	var asked []string
	reused, report, err := discover(t, fx).LoadReusing(ctx, func(key string) (bool, bool) {
		asked = append(asked, key)
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

// Remembered checkpoint facts choose the same snapshot a read would: the
// older snapshot with a transcript over a newer one without, read once.
func TestRememberedCheckpointsChooseAlike(t *testing.T) {
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

// A checkpoint that can no longer be read is chosen around, as a load that
// remembered nothing would.
func TestRememberedCheckpointThatNoLongerReads(t *testing.T) {
	fx := newFixture(t)
	opts := fx.options()
	opts.Sources = []vendors.Slug{Entire}
	d, err := Discover(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Facts remembered for the checkpoint's tree, then a store that cannot
	// read it: the chosen snapshot must be read, fails, and is reported.
	store, ok, err := openCheckpoints(context.Background(), fx.repo)
	if err != nil || !ok {
		t.Fatalf("openCheckpoints: %v %v", ok, err)
	}
	defer store.Close()
	checkpoints, err := entire.List(context.Background(), store.objects, store.store)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("checkpoints = %+v, %v", checkpoints, err)
	}
	if _, remembered := snapshotFactsCache.get(checkpoints[0].TreeOID); !remembered {
		t.Fatal("the checkpoint's facts were not remembered")
	}
	broken := *store
	broken.store = entire.Store{Branch: strings.Repeat("0", 40)}
	plan := snapshotPlan{
		root: fx.repo, opts: opts, native: map[string]bool{},
		trees: map[string]string{"0004a7a444a0": checkpoints[0].TreeOID},
		first: []string{"0004a7a444a0"}, sessionIDs: []string{firstSession},
		picks: map[string][]pick{firstSession: {{checkpoint: "0004a7a444a0"}}},
	}
	got, failures, err := plan.load(context.Background(), &broken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(failures) != 1 || failures[0].Path != "0004a7a444a0" {
		t.Errorf("sessions = %+v, failures = %+v; want none and the checkpoint reported", got, failures)
	}
}

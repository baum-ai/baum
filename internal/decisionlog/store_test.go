package decisionlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
)

var t0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func openStore(t *testing.T, c *clock) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "decisions", "decisions.sqlite"), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.now = c.Now
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func decision(id, summary string, turns ...int) decisions.Decision {
	return decisions.Decision{ID: id, Summary: summary, Category: decisions.CategoryImplementation, Who: decisions.WhoHuman, Turns: turns, Files: []string{"b.go", "a.go"}}
}

func TestSaveKeepsIDsMarksObsoleteAndDates(t *testing.T) {
	ctx := context.Background()
	c := &clock{t0}
	s := openStore(t, c)
	meta := Meta{Agent: "claude-code", ID: "s1", Repository: "o/r", Fingerprint: "f1"}
	turn1 := t0.Add(-time.Hour)
	if err := s.Save(ctx, Extraction{Meta: meta, ContentHash: "h1", Decisions: []decisions.Decision{decision("d1", "first", 1), decision("d2", "second", 2)},
		TurnTimes: map[int]time.Time{1: turn1}}); err != nil {
		t.Fatal(err)
	}
	c.now = t0.Add(time.Hour)
	meta.Fingerprint = "f2"
	// d1 is kept with new text, d2 is gone, d3 is new and has no turn time.
	if err := s.Save(ctx, Extraction{Meta: meta, ContentHash: "h2", Decisions: []decisions.Decision{decision("d1", "first, revised", 1), decision("d3", "third", 3)}}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Current(ctx, SessionKey{"claude-code", "s1"})
	if err != nil || len(current) != 2 {
		t.Fatalf("%+v %v", current, err)
	}
	if current[0].ID != "d1" || current[0].Summary != "first, revised" || !current[0].DecidedAt.Equal(turn1) || len(current[0].Files) != 2 || current[0].Files[0] != "a.go" {
		t.Fatalf("d1 = %+v", current[0])
	}
	if current[1].ID != "d3" || !current[1].DecidedAt.Equal(c.now) {
		t.Fatalf("d3 = %+v", current[1])
	}
	var obsolete int64
	if err := s.db.QueryRow(`SELECT obsolete_at FROM decision WHERE decision_id='d2'`).Scan(&obsolete); err != nil || obsolete != c.now.UnixMilli() {
		t.Fatalf("d2 obsolete_at = %d, %v", obsolete, err)
	}
	// Carried by ID when the turn has no time.
	c.now = t0.Add(2 * time.Hour)
	if err := s.Save(ctx, Extraction{Meta: meta, ContentHash: "h3", Decisions: []decisions.Decision{decision("d3", "third", 3)}}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Current(ctx, SessionKey{"claude-code", "s1"})
	if len(current) != 1 || !current[0].DecidedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", current)
	}
	row, found, err := s.Session(ctx, "claude-code", "s1")
	if err != nil || !found || row.ContentHash != "h3" || row.Coverage != CoverageExtracted || len(row.Decisions) != 1 {
		t.Fatalf("%+v %v", row, err)
	}
}

func TestSkipKeepsTheHeadAndCoverageReads(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	meta := Meta{Agent: "codex", ID: "s2", Fingerprint: "f1"}
	if err := s.Save(ctx, Extraction{Meta: meta, ContentHash: "h1", Decisions: []decisions.Decision{decision("d1", "x", 1)}}); err != nil {
		t.Fatal(err)
	}
	meta.Fingerprint = "f2"
	if err := s.Skip(ctx, meta, ReasonOverTokenCap); err != nil {
		t.Fatal(err)
	}
	row, _, _ := s.Session(ctx, "codex", "s2")
	if row.Coverage != CoverageSkipped || row.SkipReason != ReasonOverTokenCap || row.ContentHash != "h1" || len(row.Decisions) != 1 || row.Fingerprint != "f2" {
		t.Fatalf("%+v", row)
	}
	coverage, err := s.Coverage(ctx, SessionKey{"codex", "s2"}, SessionKey{"codex", "none"})
	if err != nil || len(coverage) != 1 || coverage[SessionKey{"codex", "s2"}] != [2]string{CoverageSkipped, ReasonOverTokenCap} {
		t.Fatalf("%v %v", coverage, err)
	}
	for range 3 {
		if _, err := s.Failed(ctx, meta); err != nil {
			t.Fatal(err)
		}
	}
	if row, _, _ := s.Session(ctx, "codex", "s2"); row.Failures != 3 {
		t.Fatalf("failures = %d", row.Failures)
	}
	if err := s.Skip(ctx, meta, ReasonFailed); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := s.Session(ctx, "codex", "s2"); row.Failures != 0 || row.SkipReason != ReasonFailed {
		t.Fatalf("%+v", row)
	}
}

func TestLedgerSpendAbandonedAttemptsAndPrune(t *testing.T) {
	ctx := context.Background()
	c := &clock{t0}
	path := filepath.Join(t.TempDir(), "decisions", "decisions.sqlite")
	s, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.now = c.Now
	known := 0.01
	a, _ := s.Begin(ctx, Attempt{Agent: "claude-code", ID: "s1", Backend: "claude", Model: "m", Mode: "full", Reserved: 0.05})
	if err := s.Settle(ctx, a, AttemptSucceeded, "", &known, decisions.Usage{InputTokens: 10}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Begin(ctx, Attempt{Agent: "claude-code", ID: "s1", Backend: "claude", Model: "m", Mode: "extend", Reserved: 0.02})
	if err := s.Settle(ctx, b, AttemptFailed, "call_failed", nil, decisions.Usage{}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Begin(ctx, Attempt{Agent: "codex", ID: "s9", Backend: "codex", Model: "m", Mode: "full", Reserved: 0.03})
	_ = s.Close()
	// Reopening settles the pending attempt as abandoned; its reservation counts.
	if s, err = Open(path, OpenOptions{AbandonPending: true}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	s.now = c.Now
	var state string
	if err := s.db.QueryRow(`SELECT state FROM capture_attempt WHERE agent='codex'`).Scan(&state); err != nil || state != AttemptAbandoned {
		t.Fatalf("state = %q %v", state, err)
	}
	st, err := s.Stats(ctx, "claude-code", "s1", startOfDay(t0))
	if err != nil || st.Count != 2 || !near(st.Spent, 0.03) || !near(st.AllSpent, 0.06) || !st.LastFail.Equal(t0) || !st.LastStart.Equal(t0) {
		t.Fatalf("%+v %v", st, err)
	}
	if err := s.Save(ctx, Extraction{Meta: Meta{Agent: "codex", ID: "s9"}, Decisions: []decisions.Decision{decision("old", "x", 1)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, Extraction{Meta: Meta{Agent: "codex", ID: "s9"}, Decisions: []decisions.Decision{}}); err != nil {
		t.Fatal(err)
	}
	c.now = t0.Add(Retention + time.Hour)
	if err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts, rows, files int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM capture_attempt`).Scan(&attempts)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM decision`).Scan(&rows)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM decision_file`).Scan(&files)
	if attempts != 0 || rows != 0 || files != 0 {
		t.Fatalf("after prune: %d attempts, %d decisions, %d files", attempts, rows, files)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestOpenOptionsAndEmptyHead(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "decisions")
	path := filepath.Join(dir, "decisions.sqlite")
	// A dry run on a machine without a store writes nothing.
	s, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a read-only open created %s: %v", dir, err)
	}
	s, err = Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, Attempt{Agent: "a", ID: "s", Backend: "claude", Model: "m", Mode: "full", Reserved: 0.01}); err != nil {
		t.Fatal(err)
	}
	// A session with no turns has an empty head that a later capture hits.
	if err := s.Save(ctx, Extraction{Meta: Meta{Agent: "a", ID: "empty"}, ContentHash: "h0"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	// Only the daemon's open abandons a pending attempt.
	if s, err = Open(path, OpenOptions{}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var state string
	_ = s.db.QueryRow(`SELECT state FROM capture_attempt`).Scan(&state)
	row, _, _ := s.Session(ctx, "a", "empty")
	if state != AttemptPending || row.ContentHash != "h0" || row.Decisions == nil {
		t.Fatalf("state %q, head %+v", state, row)
	}
}

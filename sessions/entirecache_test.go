package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/vendors"
)

// countedLoad discovers and loads the fixture's Entire sessions in repo's
// private repository, and returns how far each workstats counter moved
// while loading.
func countedLoad(t *testing.T, fx fixture, repo *gitlocal.Repo) ([]Session, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	opts := fx.options()
	opts.Sources = []vendors.Slug{Entire}
	// A window, so the checkpoints are dated before they are read.
	opts.Since = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d, err := Discover(ctx, fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	d.ShareRepo(repo)
	before := workstats.Take()
	sessions, report, err := d.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	moved := map[string]int64{}
	for _, delta := range workstats.Take().Since(before) {
		moved[delta.Name] = delta.Count
	}
	return sessions, moved
}

func openRepo(t *testing.T, dir string) *gitlocal.Repo {
	t.Helper()
	repo, err := gitlocal.Open(context.Background(), dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// Loading a clone whose checkpoints did not move again runs no git: the
// listing, dates and metadata are remembered, and the checkpoints read whole
// come from the checkpoint cache.
func TestLoadRunsNoGitForCheckpointsThatDidNotMove(t *testing.T) {
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	repo := openRepo(t, fx.repo)

	first, moved := countedLoad(t, fx, repo)
	if len(first) != 2 || moved["git.archive"] == 0 {
		t.Fatalf("first load: sessions %d, counters %v", len(first), moved)
	}
	second, moved := countedLoad(t, fx, repo)
	if moved["git.processes"] != 0 || moved["sessions.entire.reads"] != 0 || moved["sessions.entire.reads_cached"] == 0 {
		t.Errorf("second load of unchanged checkpoints: %v", moved)
	}
	if len(second) != len(first) {
		t.Fatalf("second load = %d sessions, want %d", len(second), len(first))
	}
	for i := range first {
		if second[i].ID != first[i].ID || second[i].Path != first[i].Path || second[i].Graph != first[i].Graph {
			t.Errorf("session %d: second load %+v, first %+v", i, second[i], first[i])
		}
	}

	// With the cache off, the checkpoints are exported again, but the
	// listing still is not.
	SetCheckpointCacheLimit(0)
	t.Cleanup(func() { SetCheckpointCacheLimit(DefaultCheckpointCacheBytes) })
	_, moved = countedLoad(t, fx, repo)
	if moved["git.archive"] == 0 || moved["git.ls-tree"] != 0 || moved["git.log"] != 0 {
		t.Errorf("load without the checkpoint cache: %v", moved)
	}
}

// A new checkpoint is read alone: those read before come from the cache.
func TestLoadReadsOnlyANewCheckpoint(t *testing.T) {
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	repo := openRepo(t, fx.repo)
	countedLoad(t, fx, repo)

	// The new checkpoint holds the newest snapshot of both sessions.
	addCheckpoint(t, fx, "222222222222", nil)
	sessions, moved := countedLoad(t, fx, repo)
	if moved["git.archive"] != 1 || moved["git.ls-tree"] != 1 || moved["entire.listing.dates_extended"] != 1 {
		t.Errorf("after one new checkpoint: %v", moved)
	}
	for _, s := range sessions {
		if s.Path != "222222222222" {
			t.Errorf("%s was read from %s, not the new checkpoint", s.ID, s.Path)
		}
	}
}

func TestCheckpointCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	c := newCheckpointCache(100)
	c.put("a", readOutcome{}, 40)
	c.put("b", readOutcome{}, 40)
	if _, ok := c.get("a"); !ok {
		t.Fatal("a was not kept")
	}
	c.put("c", readOutcome{}, 40) // evicts b, the least recently used
	if _, ok := c.get("b"); ok {
		t.Error("b was kept past the limit")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("a, recently used, was evicted")
	}
	c.put("d", readOutcome{}, 101)
	if _, ok := c.get("d"); ok || c.held != 80 {
		t.Errorf("an entry larger than the limit was kept; held %d", c.held)
	}
}

package sessions

import (
	"testing"

	"github.com/baum-ai/baum/sessions/entiresession"
)

// A checkpoint read through a real export is charged for the graphs it
// keeps, not for the files it was read from. (The exported filesystem does
// not list directories, so walking it found no files, and every entry cost
// the floor.)
func TestCheckpointCacheChargesTheGraphsItKeeps(t *testing.T) {
	SetCheckpointCacheLimit(0) // empties the cache
	SetCheckpointCacheLimit(DefaultCheckpointCacheBytes)
	t.Cleanup(func() { SetCheckpointCacheLimit(0); SetCheckpointCacheLimit(DefaultCheckpointCacheBytes) })

	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	sessions, _ := countedLoad(t, fx, openRepo(t, fx.repo))
	if len(sessions) == 0 {
		t.Fatal("no sessions loaded")
	}
	want := int64(checkpointErrorCost)
	for _, s := range sessions {
		want += graphBytes(s.Graph)
	}
	checkpointReads.mu.Lock()
	held, entries := checkpointReads.held, checkpointReads.order.Len()
	checkpointReads.mu.Unlock()
	if entries != 1 {
		t.Fatalf("entries = %d, want 1", entries)
	}
	if held != want || held <= 2*checkpointErrorCost {
		t.Errorf("held %d bytes, want the graphs' %d, well above the %d floor", held, want, checkpointErrorCost)
	}
}

func TestCheckpointCacheEvictsOnceHeldGraphsPassTheLimit(t *testing.T) {
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	sessions, _ := countedLoad(t, fx, openRepo(t, fx.repo))
	if len(sessions) == 0 {
		t.Fatal("no sessions loaded")
	}
	cost := int64(checkpointErrorCost)
	var results []entiresession.Result
	for _, s := range sessions {
		cost += graphBytes(s.Graph)
		results = append(results, entiresession.Result{Graph: s.Graph})
	}
	outcome := readOutcome{results: results}
	if got := outcomeCost(outcome); got != cost {
		t.Fatalf("outcomeCost = %d, want %d", got, cost)
	}
	c := newCheckpointCache(2*cost + cost/2)
	c.put("a", outcome, outcomeCost(outcome))
	c.put("b", outcome, outcomeCost(outcome))
	if _, ok := c.get("a"); !ok { // a is now the most recently used
		t.Fatal("a was not kept")
	}
	c.put("c", outcome, outcomeCost(outcome)) // passes the limit: b goes
	if _, ok := c.get("b"); ok {
		t.Error("b was kept past the limit")
	}
	if _, ok := c.get("a"); !ok || c.held != 2*cost {
		t.Errorf("a evicted or held = %d, want %d", c.held, 2*cost)
	}
}

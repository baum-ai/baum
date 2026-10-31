package planusage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

// scripted answers each read with the next figure, taken now.
type scripted struct {
	now     *time.Time
	figures []float64
	resets  time.Time
}

func (s *scripted) Read(context.Context) Plan {
	used := s.figures[0]
	if len(s.figures) > 1 {
		s.figures = s.figures[1:]
	}
	resets := s.resets
	return Plan{Provider: "claude", Available: true, FetchedAt: *s.now, Windows: []Window{{Name: "5-hour", UsedPercent: &used, ResetsAt: &resets}, {Name: "weekly", UsedPercent: nil}}}
}

func TestHistoryStoresChangesPersistsAndPrunes(t *testing.T) {
	now := clock
	source := &scripted{now: &now, figures: []float64{10, 10, 40, 100}, resets: clock.Add(5 * time.Hour)}
	path := filepath.Join(appdbtest.TempDir(t), "plan-usage.json")
	tick := func(h *History, d time.Duration) {
		t.Helper()
		now = now.Add(d)
		if err := h.Sample(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	h := &History{Path: path, Now: func() time.Time { return now }, Service: &Service{Sources: []Source{source}, Now: func() time.Time { return now }}}
	tick(h, 0)
	tick(h, 15*time.Minute) // unchanged: not stored
	tick(h, 15*time.Minute) // 40
	tick(h, 15*time.Minute) // 100
	got := h.Since(time.Time{})
	if len(got) != 3 || got[0].UsedPercent != 10 || got[1].UsedPercent != 40 || got[2].UsedPercent != 100 {
		t.Fatalf("samples = %+v", got)
	}
	tick(h, 2*time.Hour) // unchanged, but an hour since the last: stored
	if got := h.Since(time.Time{}); len(got) != 4 {
		t.Fatalf("keep-alive sample missing: %+v", got)
	}
	raw, err := appdb.Read(path, "state", "plan-usage-history")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "weekly") {
		t.Errorf("a window without a figure was stored: %s", raw)
	}
	// A new process reads the file; three months on, everything has gone.
	again := &History{Path: path, Now: func() time.Time { return now }, Service: h.Service}
	if got := again.Since(clock.Add(40 * time.Minute)); len(got) != 2 {
		t.Fatalf("reloaded since = %+v", got)
	}
	source.figures = []float64{55}
	tick(again, HistoryRetention+time.Minute)
	if got := again.Since(time.Time{}); len(got) != 1 || got[0].UsedPercent != 55 {
		t.Fatalf("after pruning = %+v", got)
	}
}

func TestHistoryIgnoresAReadingNoNewerThanTheLast(t *testing.T) {
	now := clock
	source := &scripted{now: &now, figures: []float64{10, 30}, resets: clock}
	h := &History{Path: filepath.Join(appdbtest.TempDir(t), "p.json"), Now: func() time.Time { return now }, Service: &Service{Sources: []Source{source}, Now: func() time.Time { return now }}}
	_ = h.Sample(context.Background())
	// The Codex snapshot did not move: the same time is not a new reading.
	h.Service.loaded = time.Time{}
	_ = h.Sample(context.Background())
	if got := h.Since(time.Time{}); len(got) != 1 {
		t.Fatalf("samples = %+v", got)
	}
}

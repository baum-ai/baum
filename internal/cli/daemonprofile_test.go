package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
)

func TestProfileSummary(t *testing.T) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	start := workstats.Snapshot{At: at, Process: workstats.Process{UserSeconds: 10, SystemSeconds: 2, GCCycles: 5},
		Counters: map[string]int64{"git.processes": 3, "sessions.map.calls": 10}}
	end := workstats.Snapshot{At: at.Add(time.Minute), Uptime: 3600, Process: workstats.Process{UserSeconds: 13, SystemSeconds: 3, GCCycles: 9, HeapBytes: 64 << 20, Goroutines: 40},
		Counters: map[string]int64{"git.processes": 15, "sessions.map.calls": 10, "sessions.map.decodes.codex": 2},
		Gauges:   map[string]int64{"sessions.map_cache.entries": 7}}
	summary := profileSummary(start, end)
	for _, want := range []string{
		"cpu:         4.00s (6.7% of one core; user 3.00s, system 1.00s)",
		"heap 64.0 MB, 40 goroutines, 4 GC cycles",
		"git.processes",
		"sessions.map.decodes.codex",
		"sessions.map_cache.entries",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "sessions.map.calls") {
		t.Errorf("a counter that did not move is listed:\n%s", summary)
	}
	if strings.Index(summary, "git.processes") > strings.Index(summary, "sessions.map.decodes.codex") {
		t.Errorf("work is not largest first:\n%s", summary)
	}

	line := watchLine(start, end)
	if !strings.Contains(line, "cpu   6.7%") || !strings.Contains(line, "git.processes=12") {
		t.Errorf("watch line = %q", line)
	}
	if idle := watchLine(end, end); !strings.Contains(idle, "idle") {
		t.Errorf("idle watch line = %q", idle)
	}
}

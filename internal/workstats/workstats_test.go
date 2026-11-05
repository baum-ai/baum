package workstats

import (
	"runtime"
	"testing"
)

func TestCountersMoveBetweenSnapshots(t *testing.T) {
	before := Take()
	Inc("test.a")
	Add("test.b", 5)
	Git("-c", "core.quotepath=false", "log", "--format=%H")
	Git("-C", "/repo", "ls-tree", "HEAD")
	after := Take()

	moved := map[string]int64{}
	for _, d := range after.Since(before) {
		moved[d.Name] = d.Count
	}
	want := map[string]int64{"test.a": 1, "test.b": 5, "git.processes": 2, "git.log": 1, "git.ls-tree": 1}
	for name, count := range want {
		if moved[name] != count {
			t.Errorf("%s moved by %d, want %d (all: %v)", name, moved[name], count, moved)
		}
	}
	if _, ok := moved["git.core.quotepath=false"]; ok {
		t.Error("an option's value was counted as the subcommand")
	}
	deltas := after.Since(before)
	for i := 1; i < len(deltas); i++ {
		if deltas[i-1].Count < deltas[i].Count {
			t.Errorf("deltas not largest first: %v", deltas)
		}
	}
}

func TestGaugesAndProcess(t *testing.T) {
	Gauge("test.gauge", func() int64 { return 42 })
	s := Take()
	if s.Gauges["test.gauge"] != 42 {
		t.Errorf("gauge = %d, want 42", s.Gauges["test.gauge"])
	}
	if s.Process.Goroutines == 0 || s.Process.HeapBytes == 0 {
		t.Errorf("process = %+v", s.Process)
	}
	if runtime.GOOS != "windows" {
		// Burn a little CPU so getrusage has something to report.
		x := 0
		for i := range 20_000_000 {
			x ^= i
		}
		_ = x
		if Take().Process.CPUSeconds() <= 0 {
			t.Error("no CPU time reported")
		}
	}
}

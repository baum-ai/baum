// Package workstats counts the work this process does, such as transcripts
// decoded, git processes run and refresh passes, so a profile of the daemon
// can say what its CPU time went to and how often each kind of work ran.
// Counters only ever grow; a reader takes two snapshots and compares them.
// Counting costs one atomic add, so it is always on.
package workstats

import (
	"maps"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	counters sync.Map // string -> *atomic.Int64
	gaugesMu sync.Mutex
	gauges   = map[string]func() int64{}
	started  = time.Now()
)

// Add adds delta to the counter name, creating it at zero.
func Add(name string, delta int64) {
	value, ok := counters.Load(name)
	if !ok {
		value, _ = counters.LoadOrStore(name, new(atomic.Int64))
	}
	value.(*atomic.Int64).Add(delta)
}

// Inc adds one to the counter name.
func Inc(name string) { Add(name, 1) }

// Git counts one git process, in total and by its subcommand: the first
// argument that is not an option or an option's value.
func Git(args ...string) {
	Inc("git.processes")
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			Inc("git." + arg)
			return
		}
	}
}

// Gauge registers a value read when a snapshot is taken, such as a cache's
// size. A later registration under the same name replaces it.
func Gauge(name string, read func() int64) {
	gaugesMu.Lock()
	defer gaugesMu.Unlock()
	gauges[name] = read
}

// Snapshot is the process's counters and gauges at one moment.
type Snapshot struct {
	At       time.Time        `json:"at"`
	Uptime   float64          `json:"uptime_seconds"`
	Process  Process          `json:"process"`
	Counters map[string]int64 `json:"counters"`
	Gauges   map[string]int64 `json:"gauges"`
}

// Process is the process's resource use since it started.
type Process struct {
	// UserSeconds and SystemSeconds are CPU time; both are zero where the
	// platform does not report them.
	UserSeconds   float64 `json:"cpu_user_seconds"`
	SystemSeconds float64 `json:"cpu_system_seconds"`
	Goroutines    int     `json:"goroutines"`
	HeapBytes     uint64  `json:"heap_bytes"`
	GCCycles      uint32  `json:"gc_cycles"`
}

// CPUSeconds is the process's user and system CPU time together.
func (p Process) CPUSeconds() float64 { return p.UserSeconds + p.SystemSeconds }

// Take reads every counter and gauge.
func Take() Snapshot {
	now := time.Now()
	s := Snapshot{At: now.UTC(), Uptime: now.Sub(started).Seconds(), Counters: map[string]int64{}, Gauges: map[string]int64{}}
	counters.Range(func(key, value any) bool {
		s.Counters[key.(string)] = value.(*atomic.Int64).Load()
		return true
	})
	gaugesMu.Lock()
	reads := maps.Clone(gauges)
	gaugesMu.Unlock()
	for name, read := range reads {
		s.Gauges[name] = read()
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	s.Process = Process{Goroutines: runtime.NumGoroutine(), HeapBytes: mem.HeapAlloc, GCCycles: mem.NumGC}
	s.Process.UserSeconds, s.Process.SystemSeconds = cpuTimes()
	return s
}

// Delta is how a counter moved between two snapshots.
type Delta struct {
	Name  string
	Count int64
}

// Since returns the counters that moved from earlier to s, largest first.
func (s Snapshot) Since(earlier Snapshot) []Delta {
	var moved []Delta
	for name, value := range s.Counters {
		if d := value - earlier.Counters[name]; d != 0 {
			moved = append(moved, Delta{name, d})
		}
	}
	sort.Slice(moved, func(i, j int) bool {
		if moved[i].Count != moved[j].Count {
			return moved[i].Count > moved[j].Count
		}
		return moved[i].Name < moved[j].Name
	})
	return moved
}

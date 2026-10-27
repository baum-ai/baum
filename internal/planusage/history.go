package planusage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	// SampleInterval is how often History reads the plans while the daemon
	// runs.
	SampleInterval = 15 * time.Minute
	// HistoryRetention is how long a sample is kept.
	HistoryRetention = 90 * 24 * time.Hour
	// keepAlive stores an unchanged figure again after this long, so a flat
	// stretch still has points and a gap means the daemon was not running.
	keepAlive = time.Hour
)

// Sample is one reading of one window: when the figure was true (the
// snapshot's time, which for Codex is its last turn), and how much was used.
type Sample struct {
	At          time.Time  `json:"at"`
	Provider    string     `json:"provider"`
	Window      string     `json:"window"`
	UsedPercent float64    `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

// History samples the plans periodically and keeps the readings in one file,
// for the analytics page's plan usage over time. Only figures are kept: no
// token, account or plan name. A reading is stored when its figure changed,
// when its window reset, or an hour after the last stored one; older than
// HistoryRetention it is dropped.
type History struct {
	Path    string
	Service *Service
	Now     func() time.Time
	// Interval defaults to SampleInterval.
	Interval time.Duration

	mu      sync.Mutex
	samples []Sample
	loaded  bool
}

func (h *History) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Run samples at once and then every Interval until ctx ends.
func (h *History) Run(ctx context.Context) {
	interval := h.Interval
	if interval <= 0 {
		interval = SampleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = h.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sample reads the plans (through the service's cache) and records what is new.
func (h *History) Sample(ctx context.Context) error {
	plans := h.Service.Plans(ctx, false)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	changed := h.prune()
	for _, plan := range plans {
		if !plan.Available {
			continue
		}
		for _, w := range plan.Windows {
			if w.UsedPercent == nil {
				continue
			}
			next := Sample{At: plan.FetchedAt.UTC(), Provider: plan.Provider, Window: w.Name, UsedPercent: *w.UsedPercent, ResetsAt: w.ResetsAt}
			if h.keep(next) {
				h.samples = append(h.samples, next)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	slices.SortStableFunc(h.samples, func(a, b Sample) int { return a.At.Compare(b.At) })
	return h.save()
}

// keep reports whether next says something the last stored reading of its
// window does not.
func (h *History) keep(next Sample) bool {
	for i := len(h.samples) - 1; i >= 0; i-- {
		last := h.samples[i]
		if last.Provider != next.Provider || last.Window != next.Window {
			continue
		}
		if !next.At.After(last.At) {
			return false
		}
		reset := (last.ResetsAt == nil) != (next.ResetsAt == nil) || (last.ResetsAt != nil && !last.ResetsAt.Equal(*next.ResetsAt))
		return last.UsedPercent != next.UsedPercent || reset || next.At.Sub(last.At) >= keepAlive
	}
	return true
}

// Since returns the readings taken at or after t, oldest first.
func (h *History) Since(t time.Time) []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	var out []Sample
	for _, s := range h.samples {
		if !s.At.Before(t) {
			out = append(out, s)
		}
	}
	return out
}

func (h *History) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	raw, err := os.ReadFile(h.Path)
	if err != nil {
		return
	}
	// An unreadable file starts a new history rather than stopping sampling.
	var stored []Sample
	if json.Unmarshal(raw, &stored) == nil {
		h.samples = stored
	}
}

func (h *History) prune() bool {
	cutoff := h.now().Add(-HistoryRetention)
	before := len(h.samples)
	h.samples = slices.DeleteFunc(h.samples, func(s Sample) bool { return s.At.Before(cutoff) })
	return len(h.samples) != before
}

func (h *History) save() error {
	raw, err := json.Marshal(h.samples)
	if err != nil {
		return err
	}
	dir := filepath.Dir(h.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".plan-usage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), h.Path)
}

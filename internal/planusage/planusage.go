// Package planusage reports how much of the Claude and Codex plans the person
// has left, for the app's settings. See README.md.
package planusage

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Window is one usage window of a plan.
type Window struct {
	// Name is "5-hour", "weekly", "weekly Opus", ... or the window length for
	// a Codex window of another size.
	Name string `json:"name"`
	// UsedPercent is nil when the window's reset passed after the snapshot,
	// so its old figure no longer applies.
	UsedPercent *float64 `json:"used_percent"`
	// ResetsAt is nil when the source did not say.
	ResetsAt *time.Time `json:"resets_at"`
	// Reset reports that the window reset after the snapshot was taken.
	Reset bool `json:"reset,omitempty"`
}

// Plan is the usage of one provider's plan, or why it is unavailable.
type Plan struct {
	Provider  string `json:"provider"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// NeedsSignIn reports that Reason asks the person to open the agent so it
	// refreshes its own sign-in. The app shows such a plan with its reason
	// instead of leaving it out.
	NeedsSignIn bool      `json:"needs_sign_in,omitempty"`
	Plan        string    `json:"plan,omitempty"`
	Windows     []Window  `json:"windows"`
	FetchedAt   time.Time `json:"fetched_at"`
	// ConfigDir tells a provider's second plan from its first: the Claude
	// Code configuration directory or Codex home whose sign-in it is, with
	// the home directory written as ~, for a plan read from a directory
	// chosen in Settings (Service.Others). The plan of the daemon's own
	// directory has none.
	ConfigDir string `json:"config_dir,omitempty"`
}

// Source reads one provider's usage. A failure is reported as an unavailable
// Plan whose Reason is a fixed sentence, never an error carrying upstream text.
type Source interface {
	Read(ctx context.Context) Plan
}

const (
	// CacheTTL is how long a result serves without asking again.
	CacheTTL = 5 * time.Minute
	// MinRefresh is the shortest time between two reads, even on demand.
	MinRefresh = 60 * time.Second
)

// Service caches the plans of its sources.
type Service struct {
	Sources []Source
	Now     func() time.Time
	// Others, when set, lists the other directories baum runs an agent with
	// (Claude Code configuration directories, Codex homes; absolute paths),
	// whose plans are read too, after Sources', each with its ConfigDir. The
	// daemon gives the directories Settings chose for reviews, fixes and data
	// extraction.
	Others func(ctx context.Context) []Other
	// OtherIn builds the reader of one such directory; nil is NewOtherIn.
	OtherIn func(Other) Source

	mu      sync.Mutex
	plans   []Plan
	loaded  time.Time
	reading chan struct{}
	// readFor are the directories plans was read for; a change of them is a
	// reason to read again, as a change in Settings should show at once.
	readFor []Other
	// readers are the readers of Others' directories, kept so each keeps its
	// last reading and its wait after a rate limit.
	readers map[Other]Source
}

// Other is a directory of a provider's agent other than the daemon's own.
type Other struct {
	// Provider is the Plan.Provider of its plan: "claude" or "codex".
	Provider string
	// Dir is the Claude Code configuration directory or Codex home.
	Dir string
}

// NewOtherIn reads the plan of o: Claude Code's with that configuration
// directory's credential (NewClaudeIn), or Codex's with that home's sign-in
// and session files.
func NewOtherIn(o Other) Source {
	switch o.Provider {
	case "claude":
		return NewClaudeIn(o.Dir)
	case "codex":
		return &Codex{Home: o.Dir}
	}
	return unknownProvider(o.Provider)
}

type unknownProvider string

func (p unknownProvider) Read(context.Context) Plan {
	return Plan{Provider: string(p), Reason: "baum cannot read this provider's usage.", Windows: []Window{}}
}

// NewService reads Claude Code's usage endpoint and Codex's session files.
func NewService() *Service {
	return &Service{Sources: []Source{NewClaude(), NewCodex()}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Plans returns the cached plans, reading again when they are older than
// CacheTTL, or older than MinRefresh when refresh is set.
func (s *Service) Plans(ctx context.Context, refresh bool) []Plan {
	others := s.others(ctx)
	for {
		s.mu.Lock()
		age := s.now().Sub(s.loaded)
		fresh := age >= 0 && (age < CacheTTL && !refresh || age < MinRefresh) && slices.Equal(others, s.readFor)
		if s.plans != nil && fresh {
			plans := s.plans
			s.mu.Unlock()
			return plans
		}
		if ctx.Err() != nil {
			plans := s.plans
			s.mu.Unlock()
			return plans
		}
		if reading := s.reading; reading != nil {
			s.mu.Unlock()
			select {
			case <-reading:
			case <-ctx.Done():
			}
			continue
		}
		s.reading = make(chan struct{})
		s.mu.Unlock()
		return s.read(ctx, others)
	}
}

// others are Others' directories, once each and in order.
func (s *Service) others(ctx context.Context) []Other {
	if s.Others == nil {
		return nil
	}
	var others []Other
	for _, o := range s.Others(ctx) {
		if filepath.IsAbs(o.Dir) && !slices.Contains(others, o) {
			others = append(others, o)
		}
	}
	return others
}

// otherIn is the reader of o, the same one on every read.
func (s *Service) otherIn(o Other) Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	if source, ok := s.readers[o]; ok {
		return source
	}
	build := s.OtherIn
	if build == nil {
		build = NewOtherIn
	}
	if s.readers == nil {
		s.readers = map[Other]Source{}
	}
	s.readers[o] = build(o)
	return s.readers[o]
}

// tildePath is path with the user's home directory written as ~.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}

// read releases the flight even if a provider panics, as an HTTP handler's
// panic must not leave all later refreshes waiting forever.
func (s *Service) read(ctx context.Context, others []Other) []Plan {
	defer func() {
		s.mu.Lock()
		close(s.reading)
		s.reading = nil
		s.mu.Unlock()
	}()
	plans := make([]Plan, 0, len(s.Sources))
	for _, source := range s.Sources {
		plans = append(plans, source.Read(ctx))
	}
	for _, o := range others {
		plan := s.otherIn(o).Read(ctx)
		plan.ConfigDir = tildePath(o.Dir)
		// Beside the provider's other plans, so every list shows them
		// together.
		at := len(plans)
		for i, other := range plans {
			if other.Provider == plan.Provider {
				at = i + 1
			}
		}
		plans = slices.Insert(plans, at, plan)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A disconnected reader must not cache cancellation as an unavailable
	// plan for everyone else. Waiters may retry with their own context.
	if ctx.Err() == nil {
		s.plans, s.loaded, s.readFor = plans, s.now(), others
	}
	return s.plans
}

// Handler serves GET /api/plan-usage; ?refresh=1 asks for a newer reading,
// which the cache still limits to one per MinRefresh.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			Plans []Plan `json:"plans"`
		}{s.Plans(r.Context(), r.URL.Query().Get("refresh") == "1")})
	})
}

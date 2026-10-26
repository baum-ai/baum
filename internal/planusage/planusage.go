// Package planusage reports how much of the Claude and Codex plans the person
// has left, for the app's settings. See README.md.
package planusage

import (
	"context"
	"encoding/json"
	"net/http"
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
	Provider  string    `json:"provider"`
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	Plan      string    `json:"plan,omitempty"`
	Windows   []Window  `json:"windows"`
	FetchedAt time.Time `json:"fetched_at"`
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

	mu     sync.Mutex
	plans  []Plan
	loaded time.Time
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
	s.mu.Lock()
	defer s.mu.Unlock()
	age := s.now().Sub(s.loaded)
	fresh := age >= 0 && (age < CacheTTL && !refresh || age < MinRefresh)
	if s.plans != nil && fresh {
		return s.plans
	}
	plans := make([]Plan, 0, len(s.Sources))
	for _, source := range s.Sources {
		plans = append(plans, source.Read(ctx))
	}
	s.plans, s.loaded = plans, s.now()
	return plans
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

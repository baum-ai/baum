package clonefind

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/repos"
)

// Service timings and bounds.
const (
	// Buffer is how many hook events wait for the service; the record drops
	// the ones a slow service misses and the service then scans again.
	Buffer = 64
	// DefaultInterval is how often the service scans while a picked
	// repository has no clone.
	DefaultInterval = time.Hour
	// minRescan is the shortest time between scans that picking starts.
	minRescan   = time.Minute
	maxSeen     = 4096
	maxAttached = 20
)

// MatchEvent selects the hook events the service reads: those of coding agents.
func MatchEvent(e hookevents.Event) bool { return e.Kind == hookevents.KindAgent }

// Registry is the part of the repository registry the service uses.
type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
	Add(ctx context.Context, path string, settings *repos.Settings) (repos.Clone, error)
}

// Attached is a clone the service registered by itself.
type Attached struct {
	Repository string    `json:"repository"`
	Path       string    `json:"path"`
	At         time.Time `json:"at"`
}

// Service keeps the last Result in memory, scans in the background, and
// registers the clone it finds of a picked repository that has none.
type Service struct {
	Registry Registry
	Options  Options
	// Changed tells the app the registry changed.
	Changed func()
	Log     *slog.Logger
	// Interval overrides DefaultInterval.
	Interval time.Duration

	mu        sync.Mutex
	ctx       context.Context
	closed    bool
	running   bool
	again     bool
	documents bool
	result    Result
	attached  []Attached
	seen      map[string]bool
	wg        sync.WaitGroup
	attachMu  sync.Mutex
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Run scans once at start, then again when asked, when events were missed,
// and every Interval while a picked repository has no clone. It attaches the
// clone of a picked repository an agent event shows. It returns when ctx ends.
func (s *Service) Run(ctx context.Context, events <-chan hookevents.Record, dropped func() uint64) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.wg.Wait()
	}()
	s.start(true, false)
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var missed uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if len(s.needy(ctx)) > 0 {
				s.start(false, false)
			}
		case record, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if dropped != nil {
				if n := dropped(); n != missed {
					missed = n
					s.start(true, false)
				}
			}
			s.handleEvent(ctx, record)
		}
	}
}

// Trigger is called when picked repositories may have changed: it attaches
// what the last scan found and, if a picked repository still has no clone,
// scans again unless it just did.
func (s *Service) Trigger() {
	s.spawn(func(ctx context.Context) {
		s.attachAll(ctx)
		s.mu.Lock()
		recent := !s.result.ScannedAt.IsZero() && time.Since(s.result.ScannedAt) < minRescan
		s.mu.Unlock()
		if !recent && len(s.needy(ctx)) > 0 {
			s.start(true, false)
		}
	})
}

// Refresh starts a scan unless one is running, and reports whether it did.
func (s *Service) Refresh() bool { return s.start(false, false) }

// RefreshWithDocuments is Refresh for one scan that also walks ~/Documents
// where it is otherwise left out (macOS). The choice is not kept.
func (s *Service) RefreshWithDocuments() bool { return s.start(false, true) }

// spawn runs fn in the background while the service runs.
func (s *Service) spawn(fn func(context.Context)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.closed || s.ctx.Err() != nil {
		return false
	}
	ctx := s.ctx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(ctx)
	}()
	return true
}

// start runs a scan in the background. While one runs it starts none; with
// again set it asks the running one to repeat once it ends.
func (s *Service) start(again, documents bool) bool {
	s.mu.Lock()
	if s.running {
		s.again = s.again || again || documents
		s.documents = s.documents || documents
		s.mu.Unlock()
		return false
	}
	if s.ctx == nil || s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	s.running = true
	s.documents = documents
	ctx := s.ctx
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for {
			s.mu.Lock()
			documents := s.documents
			s.documents = false
			s.mu.Unlock()
			s.scan(ctx, documents)
			s.mu.Lock()
			if !s.again || ctx.Err() != nil {
				s.running, s.again = false, false
				s.mu.Unlock()
				return
			}
			s.again = false
			s.mu.Unlock()
		}
	}()
	return true
}

func (s *Service) scan(ctx context.Context, documents bool) {
	options := s.Options
	options.IncludeDocuments = options.IncludeDocuments || documents
	result, err := Find(ctx, options)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.log().Warn("clone search failed", "error", err)
	}
	s.mu.Lock()
	s.result = result
	s.mu.Unlock()
	s.log().Info("clone search finished", "repositories", len(result.Repositories))
	s.attachAll(ctx)
}

// needy lists the picked repositories without an available clone.
func (s *Service) needy(ctx context.Context) map[string]bool {
	list, err := s.Registry.Repositories(ctx)
	if err != nil {
		s.log().Warn("picked repositories not read", "error", err)
		return nil
	}
	needy := map[string]bool{}
	for _, repository := range list {
		available := false
		for _, clone := range repository.Clones {
			available = available || clone.Available
		}
		if !available {
			needy[strings.ToLower(repository.Repository)] = true
		}
	}
	return needy
}

// attachAll registers the best clone found of each picked repository that has
// none.
func (s *Service) attachAll(ctx context.Context) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	attached := false
	for name := range s.needy(ctx) {
		s.mu.Lock()
		candidates := append([]Candidate(nil), s.result.Repositories[name]...)
		s.mu.Unlock()
		for _, candidate := range candidates {
			if ctx.Err() != nil {
				return
			}
			if s.attach(ctx, name, candidate) {
				attached = true
				break
			}
		}
	}
	if attached && s.Changed != nil {
		s.Changed()
	}
}

// attach registers a clone for the picked repository name. A clone the
// registry would file under another repository (a fork whose origin is the
// fork) is left alone: registering it would pick that repository too.
func (s *Service) attach(ctx context.Context, name string, candidate Candidate) bool {
	if candidate.primary != name {
		return false
	}
	clone, err := s.Registry.Add(ctx, candidate.Path, nil)
	if err != nil {
		s.log().Warn("found clone not registered", "repository", name, "error", err)
		return false
	}
	s.log().Info("registered a clone found on this computer", "repository", clone.Repository)
	s.mu.Lock()
	s.attached = append(s.attached, Attached{Repository: clone.Repository, Path: clone.Path, At: time.Now().UTC()})
	if len(s.attached) > maxAttached {
		s.attached = s.attached[len(s.attached)-maxAttached:]
	}
	s.mu.Unlock()
	return true
}

// handleEvent attaches the clone an agent worked in when it is one of a picked
// repository without a clone.
func (s *Service) handleEvent(ctx context.Context, record hookevents.Record) {
	dir, ok := AgentDir(record.Event)
	if !ok {
		return
	}
	s.mu.Lock()
	known := s.seen[dir]
	s.mu.Unlock()
	if known {
		return
	}
	needy := s.needy(ctx)
	if len(needy) == 0 {
		return
	}
	s.mu.Lock()
	if s.seen == nil || len(s.seen) >= maxSeen {
		s.seen = map[string]bool{}
	}
	s.seen[dir] = true
	s.mu.Unlock()
	at := record.Event.Time
	if at.IsZero() {
		at = record.Received
	}
	attached := false
	s.attachMu.Lock()
	for _, match := range Locate(ctx, s.Options, dir, at) {
		s.mu.Lock()
		if s.result.Repositories == nil {
			s.result.Repositories = map[string][]Candidate{}
		}
		add(s.result.Repositories, match.Repository, match.Candidate)
		sortCandidates(s.result.Repositories[match.Repository])
		s.mu.Unlock()
		if needy[match.Repository] && s.attach(ctx, match.Repository, match.Candidate) {
			attached = true
		}
	}
	s.attachMu.Unlock()
	if attached && s.Changed != nil {
		s.Changed()
	}
}

// Found is the JSON of GET /api/clones/found.
type Found struct {
	ScannedAt    *time.Time              `json:"scanned_at"`
	Running      bool                    `json:"running"`
	Repositories map[string][]FoundClone `json:"repositories"`
	Attached     []Attached              `json:"attached"`
}

// FoundClone is one clone in Found.
type FoundClone struct {
	Path         string     `json:"path"`
	CommonDir    string     `json:"common_dir"`
	LastActivity *time.Time `json:"last_activity"`
	Source       string     `json:"source"`
}

// Snapshot is the current state, as the API serves it.
func (s *Service) Snapshot() Found {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := Found{Running: s.running, Repositories: map[string][]FoundClone{}, Attached: append([]Attached{}, s.attached...)}
	if !s.result.ScannedAt.IsZero() {
		at := s.result.ScannedAt
		found.ScannedAt = &at
	}
	for name, candidates := range s.result.Repositories {
		list := make([]FoundClone, 0, len(candidates))
		for _, c := range candidates {
			clone := FoundClone{Path: c.Path, CommonDir: c.CommonDir, Source: c.Source}
			if !c.LastActivity.IsZero() {
				at := c.LastActivity
				clone.LastActivity = &at
			}
			list = append(list, clone)
		}
		found.Repositories[name] = list
	}
	return found
}

// Handler serves /api/clones/found. It has no authentication: the daemon
// mounts it behind its authenticated host and origin checks, because the paths
// it returns are local data for the app alone.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/clones/found", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, s.Snapshot())
	})
	mux.HandleFunc("POST /api/clones/found/refresh", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var body struct {
			IncludeDocuments bool `json:"include_documents"`
		}
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		var started bool
		if body.IncludeDocuments {
			started = s.RefreshWithDocuments()
		} else {
			started = s.Refresh()
		}
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		write(w, http.StatusAccepted, map[string]bool{"started": started, "running": running})
	})
	return mux
}

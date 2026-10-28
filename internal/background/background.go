// Package background keeps local GitHub metadata fresh without a generation
// queue, model backend, checkout export or hosted baum service. It hands
// discovery snapshots to Service.Discovered and nothing else: automatic
// generation is the daemon's separate scheduler.
package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
}
type Discovery interface {
	Poll(context.Context, github.Repository) (discovery.Snapshot, error)
	PollNow(context.Context, github.Repository) (discovery.Snapshot, error)
	Probe(context.Context, []github.Repository) (map[string]bool, error)
	Observe(context.Context, github.Repository, discovery.Observation) error
}
type Problem struct {
	PR     int    `json:"pr"`
	Reason string `json:"reason"`
}
type State struct {
	Problems    []Problem `json:"problems,omitempty"`
	Repository  string    `json:"repository"`
	LastSuccess time.Time `json:"last_success"`
	NextRefresh time.Time `json:"next_refresh"`
	Error       string    `json:"error,omitempty"`
	Failures    int       `json:"failures"`
}
type Service struct {
	Registry  Registry
	Discovery Discovery
	// Refresh must only read GitHub/local evidence and CAS-publish the latest
	// saved review. It must never enqueue or invoke generation.
	Refresh func(context.Context, repos.Clone, int) (discovery.Observation, error)
	Saved   func(context.Context, repos.Clone) ([]int, error)
	// Discovered receives every snapshot a discovery poll returned without
	// error. The daemon passes them to its automatic generation scheduler
	// (internal/automatic), which alone decides whether to queue a job; this
	// service never enqueues or invokes generation itself.
	Discovered  func(context.Context, discovery.Snapshot)
	Changed     func()
	Now         func() time.Time
	Interval    time.Duration
	runMu       sync.Mutex
	mu          sync.Mutex
	states      map[string]State
	hostPause   map[string]time.Time
	reads       map[string]read
	epoch       uint64
	probedEpoch uint64
	probed      time.Time
}

// ResetIdentity drops credential-dependent backoff without waiting for an old
// network pass. Its late state/rate writes are fenced by the captured epoch.
func (s *Service) ResetIdentity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	s.states = nil
	s.hostPause = nil
	s.reads = nil
}
func (s *Service) identityEpoch() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.epoch }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
func (s *Service) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return time.Minute
}
func (s *Service) state(repository string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[repository]
}
func (s *Service) put(state State, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return
	}
	if s.states == nil {
		s.states = map[string]State{}
	}
	s.states[state.Repository] = state
}
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		states := make([]State, 0, len(s.states))
		for _, state := range s.states {
			states = append(states, state)
		}
		s.mu.Unlock()
		sort.Slice(states, func(i, j int) bool { return states[i].Repository < states[j].Repository })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(states)
	})
}

// Run starts immediately in its caller's goroutine. The host runs it separately
// from HTTP startup; a slow/offline GitHub never holds the cached inbox hostage.
// One worker bounds both GitHub and local session reads across all repositories.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(min(s.interval(), 30*time.Second))
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// How long a saved review's evidence goes unread while GitHub reports no
// change to its pull request: a push, edit, review or comment moves the pull
// request's updated time, which discovery reads with every poll, and a pull
// request that leaves the open ones is read once to learn whether it merged
// or closed. Check results do not move it, but discovery reports them for
// open pull requests.
const (
	openReadAge   = 15 * time.Minute
	closedReadAge = time.Hour
	// refreshBudget bounds the pull request reads of one pass, so the next
	// pass's polls never wait long behind them; the oldest due reads go first
	// and the rest wait for the next pass.
	refreshBudget = 30 * time.Second
)

// read is this process's last read of a saved pull request in a clone.
type read struct {
	at time.Time
	// failures counts the reads that failed since the last that did not.
	failures int
}

func readKey(clone repos.Clone, number int) string {
	return fmt.Sprintf("%s\x00%s#%d", clone.CommonDir, strings.ToLower(clone.Repository), number)
}

func (s *Service) lastRead(key string) read {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[key]
}

// recordRead notes a read that GitHub did not rate-limit.
func (s *Service) recordRead(key string, err error, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return
	}
	if s.reads == nil {
		s.reads = map[string]read{}
	}
	r := read{at: s.now()}
	// A conflict means a generation or another refresh published first, with
	// fresh evidence of its own.
	if err != nil && !errors.Is(err, store.ErrConflict) {
		r.failures = s.reads[key].failures + 1
	}
	s.reads[key] = r
}

// due says whether a saved pull request's evidence should be read now, and
// whether first (urgent): GitHub reported a change since the last read.
func (s *Service) due(r read, observation discovery.Observation, observed bool, pr discovery.PullRequest, open bool) (due, urgent bool) {
	last := maxTime(observation.FetchedAt, r.at)
	switch {
	case last.IsZero():
		return true, true
	case open && pr.UpdatedAt.After(last):
		return true, true
	case r.failures > 0:
		return !s.now().Before(r.at.Add(min(s.interval()*time.Duration(1<<min(r.failures-1, 8)), openReadAge))), false
	// Unless read since the observation: a GitHub that still
	// reports the old state would otherwise be read every pass.
	case open && observed && observation.HeadSHA != pr.HeadSHA && r.at.Before(observation.FetchedAt):
		return true, true
	case !open && observed && observation.State == "OPEN" && r.at.Before(observation.FetchedAt):
		return true, true
	}
	age := closedReadAge
	if open {
		age = openReadAge
	}
	return !s.now().Before(last.Add(age)), false
}

// Sync is a single serialized scheduling pass, exposed for deterministic tests.
// Discovery itself owns conditional requests and credential-wide rate budgets.
// It polls every due repository first, so no repository's pull requests wait
// behind another's reads, then reads the saved pull requests that are due,
// those GitHub reported changed first, within refreshBudget.
func (s *Service) Sync(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	epoch := s.identityEpoch()
	if s.probedEpoch != epoch {
		s.probed = time.Time{}
		s.probedEpoch = epoch
	}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return
	}
	// Every picked repository is polled, with or without a clone. Only
	// available clones take part in the per-pull-request refresh below.
	groups := map[string][]repos.Clone{}
	seen := map[string]bool{}
	for _, repository := range picked {
		name := strings.ToLower(repository.Repository)
		if _, ok := groups[name]; !ok {
			groups[name] = nil
		}
		for _, clone := range repository.Clones {
			if !clone.Available {
				continue
			}
			key := name + "\x00" + clone.CommonDir
			if seen[key] {
				continue
			}
			seen[key] = true
			groups[name] = append(groups[name], clone)
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := s.probe(ctx, names)

	type polled struct {
		repo     github.Repository
		state    State
		previous State
		next     time.Time
	}
	type target struct {
		polled *polled
		clone  repos.Clone
		number int
		key    string
		urgent bool
		last   time.Time
	}
	var passed []*polled
	var targets []target
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		repo, err := github.ParseRepository(name)
		if err != nil {
			continue
		}
		previous := s.state(name)
		if s.now().Before(previous.NextRefresh) && !changed[repo.String()] {
			continue
		}
		p := &polled{repo: repo, state: State{Repository: name}, previous: previous}
		passed = append(passed, p)
		poll := s.Discovery.Poll
		if changed[repo.String()] {
			poll = s.Discovery.PollNow
		}
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		snapshot, pollErr := poll(requestCtx, repo)
		cancel()
		if pollErr == nil && s.Discovered != nil {
			s.Discovered(ctx, snapshot)
		}
		p.next = s.now().Add(s.interval())
		if pollErr != nil {
			p.state.Error = "discovery_unavailable"
			p.state.Failures = previous.Failures + 1
			backoff := min(s.interval()*time.Duration(1<<min(p.state.Failures-1, 8)), 15*time.Minute)
			p.next = s.now().Add(backoff)
			if snapshot.NextPoll.After(p.next) {
				p.next = snapshot.NextPoll
			}
			continue
		}
		pause := s.pause(repo.Host)
		exhausted := snapshot.Rate.Remaining != nil && *snapshot.Rate.Remaining == 0
		if exhausted || snapshot.Rate.RetryAt.After(s.now()) || pause.After(s.now()) {
			p.state.Error = "rate_limited"
			p.next = maxTime(p.next, pause)
			if exhausted || snapshot.Rate.RetryAt.After(s.now()) {
				p.next = maxTime(p.next, maxTime(snapshot.Rate.Reset, snapshot.Rate.RetryAt))
			}
			continue
		}
		open := map[int]discovery.PullRequest{}
		for _, pr := range snapshot.PullRequests {
			open[pr.Number] = pr
		}
		observations := map[int]discovery.Observation{}
		for _, observation := range snapshot.Observations {
			observations[observation.Number] = observation
		}
		for cloneIndex, clone := range groups[name] {
			numbers := map[int]bool{}
			if cloneIndex == 0 {
				for number := range open {
					numbers[number] = true
				}
			}
			saved, err := s.Saved(ctx, clone)
			if err != nil {
				p.state.Error = "store_unreadable"
			}
			for _, number := range saved {
				numbers[number] = true
			}
			for number := range numbers {
				key := readKey(clone, number)
				r := s.lastRead(key)
				observation, observed := observations[number]
				pr, isOpen := open[number]
				if due, urgent := s.due(r, observation, observed, pr, isOpen); due {
					targets = append(targets, target{p, clone, number, key, urgent, maxTime(observation.FetchedAt, r.at)})
				}
			}
		}
	}
	if len(passed) > 0 && s.Changed != nil {
		s.Changed()
	}

	// What GitHub reported changed goes first, then the longest unread.
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		switch {
		case a.urgent != b.urgent:
			return a.urgent
		case !a.last.Equal(b.last):
			return a.last.Before(b.last)
		case a.polled.repo.String() != b.polled.repo.String():
			return a.polled.repo.String() < b.polled.repo.String()
		case a.clone.CommonDir != b.clone.CommonDir:
			return a.clone.CommonDir < b.clone.CommonDir
		}
		return a.number < b.number
	})
	started := s.now()
	for _, current := range targets {
		if ctx.Err() != nil {
			return
		}
		p := current.polled
		if pause := s.pause(p.repo.Host); pause.After(s.now()) {
			// A read on this host was rate-limited: the rest wait for its reset.
			p.state.Error = "rate_limited"
			p.next = maxTime(p.next, pause)
			continue
		}
		if s.now().Sub(started) >= refreshBudget {
			break
		}
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		observation, err := s.Refresh(requestCtx, current.clone, current.number)
		cancel()
		if observation.Number > 0 && observation.HeadSHA != "" {
			if observeErr := s.Discovery.Observe(ctx, p.repo, observation); observeErr != nil {
				p.state.Error = "cache_unavailable"
			}
		}
		if limited, ok := errors.AsType[*github.RateLimitedError](err); ok {
			// Unrecorded, the read stays due and goes first after the reset.
			p.state.Error = "rate_limited"
			p.state.Problems = append(p.state.Problems, Problem{current.number, "metadata_unavailable"})
			p.next = maxTime(p.next, limited.ResumeAt)
			s.setPause(p.repo.Host, p.next, epoch)
			continue
		}
		s.recordRead(current.key, err, epoch)
		if err != nil && !errors.Is(err, store.ErrConflict) {
			p.state.Error = "metadata_unavailable"
			p.state.Problems = append(p.state.Problems, Problem{current.number, "metadata_unavailable"})
		}
	}

	for _, p := range passed {
		state, next := p.state, p.next
		state.LastSuccess = p.previous.LastSuccess
		if state.Error == "" {
			state.LastSuccess = s.now()
		} else if state.Failures == 0 {
			state.Failures = p.previous.Failures + 1
			// A failed read backs off on its own; the polls go on.
			if state.Error != "rate_limited" && state.Error != "metadata_unavailable" {
				next = maxTime(next, s.now().Add(min(s.interval()*time.Duration(1<<min(state.Failures-1, 8)), 15*time.Minute)))
			}
		}
		state.NextRefresh = next
		s.put(state, epoch)
	}
	if len(passed) > 0 && s.Changed != nil {
		s.Changed()
	}
}

// probe asks discovery, at most once per interval, which repositories' open
// pull requests changed since their last poll: those poll at once rather than
// after discovery's backoff, so a pull request opened or pushed from anywhere
// shows within about a minute. A failed probe changes nothing; the scheduled
// polls go on.
func (s *Service) probe(ctx context.Context, names []string) map[string]bool {
	if s.now().Before(s.probed.Add(s.interval())) {
		return nil
	}
	s.probed = s.now()
	repositories := make([]github.Repository, 0, len(names))
	for _, name := range names {
		if repo, err := github.ParseRepository(name); err == nil {
			repositories = append(repositories, repo)
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	changed, err := s.Discovery.Probe(requestCtx, repositories)
	if err != nil {
		return nil
	}
	return changed
}

// pause is when the host's rate limit lets refreshes resume.
func (s *Service) pause(host string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostPause[host]
}

func (s *Service) setPause(host string, until time.Time, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return
	}
	if s.hostPause == nil {
		s.hostPause = map[string]time.Time{}
	}
	s.hostPause[host] = maxTime(s.hostPause[host], until)
}

// RefreshNow refreshes some pull requests of one clone at once, as Sync
// refreshes each of them, outside Sync's schedule: a hook event said they
// changed. It honors the host's rate pause, which a rate-limited refresh here
// extends for Sync too, and returns the observations it made. It never
// enqueues or invokes generation.
func (s *Service) RefreshNow(ctx context.Context, clone repos.Clone, numbers []int) ([]discovery.Observation, error) {
	epoch := s.identityEpoch()
	repo, err := github.ParseRepository(clone.Repository)
	if err != nil {
		return nil, err
	}
	if resume := s.pause(repo.Host); resume.After(s.now()) {
		return nil, &github.RateLimitedError{ResumeAt: resume}
	}
	var observations []discovery.Observation
	var errs []error
	for _, number := range numbers {
		if ctx.Err() != nil {
			break
		}
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		observation, err := s.Refresh(requestCtx, clone, number)
		cancel()
		if observation.Number > 0 && observation.HeadSHA != "" {
			observations = append(observations, observation)
			if observeErr := s.Discovery.Observe(ctx, repo, observation); observeErr != nil {
				errs = append(errs, observeErr)
			}
		}
		if limited, ok := errors.AsType[*github.RateLimitedError](err); ok {
			errs = append(errs, err)
			s.setPause(repo.Host, limited.ResumeAt, epoch)
			break
		}
		s.recordRead(readKey(clone, number), err, epoch)
		if err != nil && !errors.Is(err, store.ErrConflict) {
			errs = append(errs, err)
		}
	}
	if len(observations) > 0 && s.Changed != nil {
		s.Changed()
	}
	return observations, errors.Join(errs...)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func Saved(ctx context.Context, clone repos.Clone) ([]int, error) {
	versions, err := store.Open(clone.CommonDir, "", false)
	if err != nil {
		return nil, err
	}
	rows, err := versions.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var numbers []int
	for _, row := range rows {
		if strings.EqualFold(row.Key.Repository, clone.Repository) && row.Latest != nil {
			numbers = append(numbers, row.Key.Number)
		}
	}
	return numbers, nil
}

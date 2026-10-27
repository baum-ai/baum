// Package background keeps local GitHub metadata fresh without a generation
// queue, model backend, checkout export or hosted baum service. It hands
// discovery snapshots to Service.Discovered and nothing else: automatic
// generation is the daemon's separate scheduler.
package background

import (
	"context"
	"encoding/json"
	"errors"
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
	cursors     map[string]int
	nextRepo    string
	epoch       uint64
	cursorEpoch uint64
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

// Sync is a single serialized scheduling pass, exposed for deterministic tests.
// Discovery itself owns conditional requests and credential-wide rate budgets.
func (s *Service) Sync(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	epoch := s.identityEpoch()
	if s.cursorEpoch != epoch {
		s.cursors = nil
		s.nextRepo = ""
		s.probed = time.Time{}
		s.cursorEpoch = epoch
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
	if s.nextRepo != "" {
		index := sort.SearchStrings(names, s.nextRepo)
		if index < len(names) {
			names = append(append([]string{}, names[index:]...), names[:index]...)
		}
	}
	if s.cursors == nil {
		s.cursors = map[string]int{}
	}
	changed := s.probe(ctx, names)
	for repoIndex, name := range names {
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
		state := State{Repository: name}
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
		next := s.now().Add(s.interval())
		if pollErr != nil {
			state.Error = "discovery_unavailable"
			state.Failures = previous.Failures + 1
			backoff := min(s.interval()*time.Duration(1<<min(state.Failures-1, 8)), 15*time.Minute)
			next = s.now().Add(backoff)
			if snapshot.NextPoll.After(next) {
				next = snapshot.NextPoll
			}
		} else {
			pause := s.pause(repo.Host)
			exhausted := snapshot.Rate.Remaining != nil && *snapshot.Rate.Remaining == 0
			if exhausted || snapshot.Rate.RetryAt.After(s.now()) || pause.After(s.now()) {
				state.Error = "rate_limited"
				next = maxTime(next, pause)
				if exhausted || snapshot.Rate.RetryAt.After(s.now()) {
					next = maxTime(next, maxTime(snapshot.Rate.Reset, snapshot.Rate.RetryAt))
				}
			} else {

				type target struct {
					clone  repos.Clone
					number int
				}
				var targets []target
				for cloneIndex, clone := range groups[name] {
					numbers := map[int]bool{}
					if cloneIndex == 0 {
						for _, pr := range snapshot.PullRequests {
							numbers[pr.Number] = true
						}
					}
					saved, err := s.Saved(ctx, clone)
					if err != nil {
						state.Error = "store_unreadable"
					}
					for _, number := range saved {
						numbers[number] = true
					}
					sorted := make([]int, 0, len(numbers))
					for number := range numbers {
						sorted = append(sorted, number)
					}
					sort.Ints(sorted)
					for _, number := range sorted {
						targets = append(targets, target{clone, number})
					}
				}
				cursor := 0
				if len(targets) > 0 {
					cursor = s.cursors[name] % len(targets)
				}
				for i := 0; i < len(targets); i++ {
					if ctx.Err() != nil {
						return
					}
					current := targets[(cursor+i)%len(targets)]
					requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
					observation, err := s.Refresh(requestCtx, current.clone, current.number)
					cancel()
					s.cursors[name] = (cursor + i + 1) % len(targets)
					if observation.Number > 0 && observation.HeadSHA != "" {
						if observeErr := s.Discovery.Observe(ctx, repo, observation); observeErr != nil {
							state.Error = "cache_unavailable"
						}
					}
					// A conflict means a generation or another refresh published first;
					// the next pass refreshes the latest document normally.
					if err != nil && !errors.Is(err, store.ErrConflict) {
						state.Error = "metadata_unavailable"
						state.Problems = append(state.Problems, Problem{current.number, "metadata_unavailable"})
						var limited *github.RateLimitedError
						if errors.As(err, &limited) {
							state.Error = "rate_limited"
							// Retry the uncompleted target with the next budget window. Skipping
							// it can starve every second target when each window funds one PR.
							s.cursors[name] = (cursor + i) % len(targets)
							next = maxTime(next, limited.ResumeAt)
							s.setPause(repo.Host, next, epoch)
							s.nextRepo = names[(repoIndex+1)%len(names)]
							break
						}
					}
				}
			}
		}
		state.LastSuccess = previous.LastSuccess
		if state.Error == "" {
			state.LastSuccess = s.now()
		} else if state.Failures == 0 {
			state.Failures = previous.Failures + 1
			if state.Error != "rate_limited" {
				next = maxTime(next, s.now().Add(min(s.interval()*time.Duration(1<<min(state.Failures-1, 8)), 15*time.Minute)))
			}
		}
		state.NextRefresh = next
		s.put(state, epoch)
		if s.Changed != nil {
			s.Changed()
		}
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
		if err != nil && !errors.Is(err, store.ErrConflict) {
			errs = append(errs, err)
			if limited, ok := errors.AsType[*github.RateLimitedError](err); ok {
				s.setPause(repo.Host, limited.ResumeAt, epoch)
				break
			}
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

// Package background keeps local GitHub metadata fresh without a generation
// queue, model backend, checkout export or hosted baum service. It hands
// discovery snapshots to Service.Discovered and nothing else: automatic
// generation is the daemon's separate scheduler.
package background

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions"
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
	// Saved lists the clone's pull requests with a saved review: only those
	// are read in the background.
	Saved func(context.Context, repos.Clone) ([]SavedReview, error)
	// Budget is the credential's GraphQL points budget as discovery last read
	// it (discovery.Service.GraphQLBudget); nil leaves it unchecked. Reads
	// wait while less than refreshReserve of it is left.
	Budget func(context.Context, github.Repository) (discovery.RateLimit, error)
	// Discovered receives every snapshot a discovery poll returned without
	// error. The daemon passes them to its automatic generation scheduler
	// (internal/automatic), which alone decides whether to queue a job; this
	// service never enqueues or invokes generation itself.
	Discovered func(context.Context, discovery.Snapshot)
	// Retain reconciles a clone's retained review refs with its stores and
	// reports whether a bound left work for another pass (Retention.Retain);
	// nil skips it. Sync calls it per common git directory, see retainDue.
	Retain      func(context.Context, repos.Clone) (bool, error)
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
	// probeFailed says the latest probe failed, as every request does while
	// the machine is offline, and failedSince when the probes began to fail.
	probeFailed bool
	failedSince time.Time
	retained    map[string]retention
	// started is this process's first pass, or the first under the current
	// identity, from which the reads not yet made are spread (restartSpread).
	started time.Time
	// asked is when the person last refreshed each repository (Asked), by
	// lower-case name.
	asked map[string]time.Time
	// spent is the GraphQL requests the reads made in the last hour, oldest
	// first, which refreshRequestsPerHour bounds.
	spent []spend
}

// spend is the GraphQL requests one read made.
type spend struct {
	at       time.Time
	requests int
}

// SavedReview is a pull request with a saved review in a clone's store, as
// its latest version records it.
type SavedReview struct {
	Number int
	// State is the saved document's pr_state: open, draft, closed or merged.
	State string
	// Head is the head the latest version reviewed.
	Head string
	// Generated is when the latest version was generated, and Started when
	// its generation began to read the evidence; zero when unknown.
	Generated, Started time.Time
}

// final says the saved document records that its pull request merged or
// closed: a read of it while it is not open has nothing left to learn.
func (r SavedReview) final() bool {
	return strings.EqualFold(r.State, "merged") || strings.EqualFold(r.State, "closed")
}

// closedDraft says the saved document is of a draft that a read after its
// generation observed closed: a document records a closed draft as a draft,
// so its state never says the final read was published.
func (r SavedReview) closedDraft(observation discovery.Observation) bool {
	return strings.EqualFold(r.State, "draft") && (observation.State == "CLOSED" || observation.State == "MERGED") && observation.FetchedAt.After(r.Generated)
}

// retention is this process's last Retain of a common git directory.
type retention struct {
	at   time.Time
	more bool
	err  bool
}

// A clone's retained refs are reconciled on the first pass that sees it,
// then once a day, an hour after a failure, and on the next pass while a
// bound left work.
const (
	retainInterval = 24 * time.Hour
	retainRetry    = time.Hour
)

func (s *Service) retainDue(r retention) bool {
	switch {
	case r.at.IsZero() || r.more:
		return true
	case r.err:
		return s.now().Sub(r.at) >= retainRetry
	}
	return s.now().Sub(r.at) >= retainInterval
}

// retain runs Retain for each common git directory among the clones that is
// due, in path order.
func (s *Service) retain(ctx context.Context, groups map[string][]repos.Clone) {
	if s.Retain == nil {
		return
	}
	clones := map[string]repos.Clone{}
	for _, group := range groups {
		for _, clone := range group {
			if _, ok := clones[clone.CommonDir]; !ok {
				clones[clone.CommonDir] = clone
			}
		}
	}
	dirs := make([]string, 0, len(clones))
	for dir := range clones {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		if !s.retainDue(s.retained[dir]) {
			continue
		}
		more, err := s.Retain(ctx, clones[dir])
		if s.retained == nil {
			s.retained = map[string]retention{}
		}
		s.retained[dir] = retention{at: s.now(), more: more && err == nil, err: err != nil}
	}
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
	s.asked = nil
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
// One pass runs at a time: its polls run pollConcurrency at once, its reads
// one at a time.
func (s *Service) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.Sync(ctx)
		timer := time.NewTimer(s.wait())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// wait is how long Run sleeps between passes: at most 30 seconds, less when a
// repository is due sooner, as one settling after a new head is. A deadline
// past by more than the interval is a repository no longer picked.
func (s *Service) wait() time.Duration {
	wait := min(s.interval(), 30*time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.states {
		if until := state.NextRefresh.Sub(s.now()); until < wait && until > -s.interval() {
			wait = max(until, time.Second)
		}
	}
	return wait
}

// Only pull requests with a saved review are read; discovery covers the
// others. An open one is read when discovery reports a change: a push, edit,
// review or comment moves its updated time, a push its head, a check
// finishing its check rollup and a resolved thread its unresolved count; or
// when the person refreshes its repository (Asked). Otherwise it is read once
// its evidence is openReadAge old. One that leaves the open ones is read once more, to record
// that it merged or closed, and never again while it stays closed: a saved
// document that records the final state is retired.
const (
	openReadAge = 2 * time.Hour
	// restartSpread spreads the reads that were already due, or fall due by
	// age, before this process read them, so a restart does not read every
	// saved review at once. A change GitHub reported is read at once.
	restartSpread = 30 * time.Minute
	// maxReadFailures is how many reads of a pull request fail in a row
	// before it is read again only when its updated time or head moves (or
	// after stoppedRetry while open): a huge one that never fits its time
	// limit stops spending the budget.
	maxReadFailures = 3
	stoppedRetry    = 24 * time.Hour
	// refreshBudget bounds the pull request reads a pass starts, so the next
	// pass's polls seldom wait long behind them; the oldest due reads go first
	// and the rest wait for the next pass. A read under way is not cut short,
	// so a huge pull request may hold the next pass for up to readLimit.
	refreshBudget = 30 * time.Second
	// refreshRequestsPerHour bounds the GraphQL requests the reads make in
	// any hour, a fifth of a token's usual 5,000 points, so they never drain
	// the budget discovery and generation share. RefreshNow's reads count
	// toward it but are never held by it.
	refreshRequestsPerHour = 1000
	// refreshReserve is the part of the token's GraphQL points the reads
	// leave to discovery and generation: while discovery last read fewer
	// remaining in the current window, reads wait for its reset.
	refreshReserve = 1000
	// readRequestTimeout bounds each GitHub request of a read, so a huge pull
	// request can take as many requests as it needs while a stalled one fails.
	// readLimit only stops a read that never ends.
	readRequestTimeout = 30 * time.Second
	readLimit          = 10 * time.Minute
	// pollConcurrency is how many repositories poll at once. They share the
	// credential's GitHub budget, which discovery checks before every request.
	pollConcurrency = 3
	// pollTimeout is how long a repository's poll waits for GitHub to answer
	// one of its requests (discovery.WithIdleTimeout); discovery keeps what it
	// read when this runs out. It bounds a whole probe.
	pollTimeout = 45 * time.Second
)

// read is this process's last read of a saved pull request in a clone.
type read struct {
	at time.Time
	// failures counts the reads that failed since the last that did not.
	failures int
	// again asks for a failed read at the next pass: GitHub answers again
	// after it did not, or the person refreshed the repository.
	again bool
	// head, open, updated, checks and unresolved are what discovery showed of
	// the pull request when its read was due: a change from them is what makes
	// it due again, so a GitHub that still reports the old state is not read
	// every pass. updated is GitHub's own time, so a change made while the
	// read ran is read again, whatever this computer's clock says.
	head       string
	open       bool
	updated    time.Time
	checks     string
	unresolved *int
}

func readKey(clone repos.Clone, number int) string {
	return fmt.Sprintf("%s\x00%s#%d", clone.CommonDir, strings.ToLower(clone.Repository), number)
}

func (s *Service) lastRead(key string) read {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[key]
}

// recordRead notes a read that GitHub did not rate-limit, of a pull request
// discovery showed as seen.
func (s *Service) recordRead(key string, err error, epoch uint64, seen read) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return
	}
	if s.reads == nil {
		s.reads = map[string]read{}
	}
	r := read{at: s.now(), head: seen.head, open: seen.open, updated: seen.updated, checks: seen.checks, unresolved: seen.unresolved}
	// A conflict means a generation or another refresh published first, with
	// fresh evidence of its own.
	if err != nil && !errors.Is(err, store.ErrConflict) {
		r.failures = s.reads[key].failures + 1
	}
	s.reads[key] = r
}

// Asked records a refresh of repository the person asked for: the next pass
// reads its open saved reviews once more, and again those whose reads
// failed, stopped or not. Retired reviews of closed pull requests stay
// retired.
func (s *Service) Asked(repository string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repository = strings.ToLower(repository)
	if s.asked == nil {
		s.asked = map[string]time.Time{}
	}
	s.asked[repository] = s.now()
	marker := "\x00" + repository + "#"
	for key, r := range s.reads {
		if r.failures > 0 && strings.Contains(key, marker) {
			r.again = true
			s.reads[key] = r
		}
	}
}

func (s *Service) askedAt(repository string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked[strings.ToLower(repository)]
}

// retryFailedSince has the next pass read again the saved pull requests
// whose last read failed at or after since: GitHub answers again after it
// did not, so those failures may say nothing about the pull requests.
func (s *Service) retryFailedSince(since time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, r := range s.reads {
		if r.failures > 0 && !r.at.Before(since) {
			r.again = true
			s.reads[key] = r
		}
	}
}

// due says whether a saved pull request's evidence should be read now, and
// whether first (urgent): GitHub reported a change since the last read.
func (s *Service) due(key string, r read, saved SavedReview, observation discovery.Observation, pr discovery.PullRequest, open bool, asked time.Time) (due, urgent bool) {
	due, urgent, reason := s.dueReason(key, r, saved, observation, pr, open, asked)
	if due {
		workstats.Inc("background.due." + reason)
	} else if reason == "retired" || reason == "stopped" {
		workstats.Inc("background.skipped." + reason)
	}
	return due, urgent
}

// dueReason is due with the rule that decided it, which the work counters
// record; one that is not due may name why it waits for a change (retired,
// stopped). asked is when the person last refreshed the repository.
func (s *Service) dueReason(key string, r read, saved SavedReview, observation discovery.Observation, pr discovery.PullRequest, open bool, asked time.Time) (due, urgent bool, reason string) {
	last := maxTime(maxTime(observation.FetchedAt, r.at), saved.Generated)
	// What GitHub's update time is compared with: its own time when this
	// process's last read was due, else when the evidence was last read.
	since := maxTime(observation.FetchedAt, saved.Started)
	if !r.updated.IsZero() {
		since = r.updated
	}
	// Unread in this process: what was last observed stands for it.
	head, checks, unresolved := r.head, r.checks, r.unresolved
	if r.at.IsZero() {
		head = cmp.Or(observation.HeadSHA, saved.Head)
		if observation.HeadSHA == pr.HeadSHA {
			checks = observation.Checks
		}
	}
	now := s.now()
	// A read this process has not made yet waits for its turn in the spread.
	spread := r.at.IsZero() && now.Before(s.started.Add(spreadOffset(key)))
	if !open {
		switch {
		case saved.final():
			// Only a published document records the final state.
			return false, false, "retired"
		case saved.closedDraft(observation) && !r.at.IsZero() && !r.open && r.failures == 0:
			// A closed draft's document keeps saying draft: a final read that
			// this process published (or found published) retires it here. A
			// read that failed after GitHub answered left an observation of
			// the close too, but no published document, so it is retried.
			return false, false, "retired"
		case r.failures >= maxReadFailures && !r.again:
			// Not open, it moves no more: a failing final read stops until
			// a retry or the next process.
			return false, false, "stopped"
		case r.failures > 0:
			return r.again || !now.Before(r.at.Add(s.backoff(r.failures))), false, "retry"
		case r.at.IsZero():
			return !spread, false, "closed"
		case r.open:
			// Read open in this process and gone from the open ones since.
			return true, true, "closed"
		}
		// Read since it left the open ones, and GitHub still said open.
		return !now.Before(last.Add(openReadAge)), false, "age"
	}
	updated := pr.UpdatedAt.After(since)
	moved := head != "" && pr.HeadSHA != head
	switch {
	case r.failures >= maxReadFailures && !r.again:
		if updated || moved {
			return true, true, "changed"
		}
		return !now.Before(r.at.Add(stoppedRetry)), false, "stopped"
	case last.IsZero():
		return !spread || asked.After(r.at), false, "never_read"
	case updated:
		return true, true, "updated"
	case r.failures > 0:
		return r.again || !now.Before(r.at.Add(s.backoff(r.failures))), false, "retry"
	case moved:
		return true, true, "new_head"
	case rollup(checks) != "" && rollup(pr.Checks) != "" && rollup(checks) != rollup(pr.Checks):
		return !spread || asked.After(r.at), false, "checks"
	case !r.at.IsZero() && unresolved != nil && pr.Unresolved != nil && *unresolved != *pr.Unresolved:
		return true, false, "threads"
	case asked.After(r.at):
		return true, false, "asked"
	}
	return !spread && !now.Before(last.Add(openReadAge)), false, "age"
}

// rollup is a check rollup in one vocabulary, discovery's (passing, failing,
// running, none) or an observation's (success, failure, pending, none); ""
// when unknown.
func rollup(checks string) string {
	switch checks {
	case "passing", "success":
		return "passing"
	case "failing", "failure":
		return "failing"
	case "running", "pending":
		return "running"
	case "none":
		return "none"
	}
	return ""
}

// backoff is how long after its failures-th failure in a row a read is tried
// again: the interval, doubling.
func (s *Service) backoff(failures int) time.Duration {
	return s.interval() * time.Duration(1<<min(max(failures-1, 0), 8))
}

// spreadOffset is how long after this process's first pass a read it has
// not made yet may be due, the same for a pull request in every process.
func spreadOffset(key string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum64() % uint64(restartSpread))
}

// spend records the GraphQL requests a read made.
func (s *Service) spend(requests int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Not fenced by the identity epoch: the requests were made, and the
	// count survives an identity change.
	if requests <= 0 {
		return
	}
	s.spent = append(s.trimSpent(), spend{s.now(), requests})
}

// spentLastHour is the GraphQL requests the reads made in the last hour.
func (s *Service) spentLastHour() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spent = s.trimSpent()
	total := 0
	for _, entry := range s.spent {
		total += entry.requests
	}
	return total
}

// trimSpent drops what was spent more than an hour ago; s.mu is held.
func (s *Service) trimSpent() []spend {
	cutoff := s.now().Add(-time.Hour)
	i := 0
	for i < len(s.spent) && !s.spent[i].at.After(cutoff) {
		i++
	}
	return s.spent[i:]
}

// reserved says whether discovery last read less than refreshReserve of the
// host's GraphQL points left in the current window, or was asked to wait.
func (s *Service) reserved(ctx context.Context, repo github.Repository) bool {
	if s.Budget == nil {
		return false
	}
	rate, err := s.Budget(ctx, repo)
	if err != nil {
		return false
	}
	if rate.RetryAt.After(s.now()) {
		// A secondary limit discovery met.
		return true
	}
	return rate.Remaining != nil && rate.Reset.After(s.now()) && *rate.Remaining < refreshReserve
}

// read runs one Refresh within its time limits, counting its GitHub requests
// toward refreshRequestsPerHour.
func (s *Service) read(ctx context.Context, clone repos.Clone, number int) (discovery.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, readLimit)
	defer cancel()
	ctx, requests := github.CountRequests(github.WithRequestTimeout(ctx, readRequestTimeout))
	workstats.Inc("background.refreshes")
	observation, err := s.Refresh(ctx, clone, number)
	s.spend(requests())
	return observation, err
}

// Sync is a single serialized scheduling pass, exposed for deterministic tests.
// Discovery itself owns conditional requests and credential-wide rate budgets.
// It polls every due repository first, pollConcurrency at a time, so no
// repository's pull requests wait behind another's reads, then reads the saved
// pull requests that are due, those GitHub reported changed first, within
// refreshBudget.
func (s *Service) Sync(ctx context.Context) {
	workstats.Inc("background.passes")
	s.runMu.Lock()
	defer s.runMu.Unlock()
	epoch := s.identityEpoch()
	if s.probedEpoch != epoch {
		s.probed = time.Time{}
		s.probeFailed = false
		s.probedEpoch = epoch
		// Another identity: every read is a first one again.
		s.started = time.Time{}
	}
	if s.started.IsZero() {
		s.started = s.now()
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
	failedSince := s.failedSince
	changed, reconnected := s.probe(ctx, names)
	if reconnected {
		// Reads that failed while GitHub did not answer are tried again.
		s.retryFailedSince(failedSince)
	}

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
		// seen is what discovery showed of it, for recordRead.
		seen read
	}
	var passed []*polled
	var targets []target
	// A few repositories poll at once, on discovery's shared budget. What a
	// poll returned is handled one at a time: Discovered, and the reads it
	// makes due.
	var handle sync.Mutex
	var polls sync.WaitGroup
	slots := make(chan struct{}, pollConcurrency)
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		repo, err := github.ParseRepository(name)
		if err != nil {
			continue
		}
		previous := s.state(name)
		// GitHub answers again after the probes failed, as when the machine
		// comes back online: a repository whose poll failed meanwhile polls
		// now rather than after its backoff, which grew while offline.
		retry := reconnected && previous.Error != "" && previous.Error != "rate_limited"
		if s.now().Before(previous.NextRefresh) && !changed[repo.String()] && !retry {
			continue
		}
		p := &polled{repo: repo, state: State{Repository: name}, previous: previous}
		passed = append(passed, p)
		poll := s.Discovery.Poll
		if changed[repo.String()] || retry {
			poll = s.Discovery.PollNow
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		polls.Go(func() {
			defer func() { <-slots }()
			// A busy repository's sweep takes minutes; it stops only once
			// GitHub stops answering.
			requestCtx, cancel := discovery.WithIdleTimeout(ctx, pollTimeout)
			snapshot, pollErr := poll(requestCtx, repo)
			cancel()
			handle.Lock()
			defer handle.Unlock()
			if pollErr == nil && s.Discovered != nil {
				s.Discovered(ctx, snapshot)
			}
			p.next = s.now().Add(s.interval())
			// A new head: discovery reads its mergeability again soon.
			if pollErr == nil && snapshot.Settling && snapshot.NextPoll.Before(p.next) {
				p.next = snapshot.NextPoll
			}
			if pollErr != nil {
				p.state.Error = "discovery_unavailable"
				if errors.Is(pollErr, discovery.ErrTimeout) {
					p.state.Error = "discovery_timeout"
				}
				p.state.Failures = previous.Failures + 1
				backoff := min(s.interval()*time.Duration(1<<min(p.state.Failures-1, 8)), 15*time.Minute)
				p.next = s.now().Add(backoff)
				if snapshot.NextPoll.After(p.next) {
					p.next = snapshot.NextPoll
				}
				return
			}
			pause := s.pause(repo.Host)
			exhausted := snapshot.Rate.Remaining != nil && *snapshot.Rate.Remaining == 0
			if exhausted || snapshot.Rate.RetryAt.After(s.now()) || pause.After(s.now()) {
				p.state.Error = "rate_limited"
				p.next = maxTime(p.next, pause)
				if exhausted || snapshot.Rate.RetryAt.After(s.now()) {
					p.next = maxTime(p.next, maxTime(snapshot.Rate.Reset, snapshot.Rate.RetryAt))
				}
				return
			}
			open := map[int]discovery.PullRequest{}
			for _, pr := range snapshot.PullRequests {
				open[pr.Number] = pr
			}
			observations := map[int]discovery.Observation{}
			for _, observation := range snapshot.Observations {
				observations[observation.Number] = observation
			}
			// Only saved reviews are read: discovery covers the other open pull
			// requests.
			for _, clone := range groups[name] {
				saved, err := s.Saved(ctx, clone)
				if err != nil {
					p.state.Error = "store_unreadable"
				}
				asked := s.askedAt(name)
				for _, review := range saved {
					key := readKey(clone, review.Number)
					r := s.lastRead(key)
					observation := observations[review.Number]
					pr, isOpen := open[review.Number]
					if due, urgent := s.due(key, r, review, observation, pr, isOpen, asked); due {
						seen := read{head: pr.HeadSHA, open: isOpen, updated: pr.UpdatedAt, checks: pr.Checks, unresolved: pr.Unresolved}
						if !isOpen {
							seen.head = cmp.Or(observation.HeadSHA, review.Head)
						}
						targets = append(targets, target{p, clone, review.Number, key, urgent, maxTime(maxTime(observation.FetchedAt, r.at), review.Generated), seen})
					}
				}
			}
		})
	}
	polls.Wait()
	if ctx.Err() != nil {
		return
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
	// A pass is not latency-sensitive: its loads decode few sessions at once.
	ctx = sessions.WithBackground(ctx)
	started := s.now()
	// Whether each host's GraphQL points are down to discovery's reserve,
	// asked once a pass.
	reserves := map[string]bool{}
	// The pull requests of one clone read mostly the same sessions: decode
	// each once in this pass.
	if len(targets) > 1 {
		release := sessions.HoldMappings()
		defer release()
	}
	for _, current := range targets {
		if ctx.Err() != nil || s.identityEpoch() != epoch {
			// Under another identity the next pass starts over.
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
		if s.spentLastHour() >= refreshRequestsPerHour {
			// The rest wait for the hour's budget; the longest unread go
			// first then.
			workstats.Inc("background.budget_spent")
			break
		}
		low, ok := reserves[p.repo.Host]
		if !ok {
			low = s.reserved(ctx, p.repo)
			reserves[p.repo.Host] = low
			if low {
				workstats.Inc("background.budget_reserved")
			}
		}
		if low {
			// Left to discovery and generation until the window resets.
			continue
		}
		observation, err := s.read(ctx, current.clone, current.number)
		if ctx.Err() != nil {
			// Stopped, not failed: the read stays due.
			return
		}
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
		s.recordRead(current.key, err, epoch, current.seen)
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
	s.retain(ctx, groups)
}

// probe asks discovery, at most once per interval, which repositories' open
// pull requests changed since their last poll: those poll at once rather than
// after discovery's backoff, so a pull request opened or pushed from anywhere
// shows within about a minute. A failed probe changes nothing; the scheduled
// polls go on. reconnected says this probe succeeded after one that failed.
func (s *Service) probe(ctx context.Context, names []string) (changed map[string]bool, reconnected bool) {
	if s.now().Before(s.probed.Add(s.interval())) {
		return nil, false
	}
	// Reads may have failed since the last probe, before this one failed.
	previous := s.probed
	s.probed = s.now()
	repositories := make([]github.Repository, 0, len(names))
	for _, name := range names {
		if repo, err := github.ParseRepository(name); err == nil {
			repositories = append(repositories, repo)
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	changed, err := s.Discovery.Probe(requestCtx, repositories)
	if err != nil {
		if !s.probeFailed {
			s.failedSince = previous
		}
		s.probeFailed = true
		return nil, false
	}
	reconnected = s.probeFailed
	s.probeFailed = false
	return changed, reconnected
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

// Paused is when the host's rate limit lets refreshes resume, for another
// reader of the same account's GitHub budget, such as the PR page's facts
// (internal/facts), to wait it out too.
func (s *Service) Paused(host string) time.Time { return s.pause(host) }

// Limited records a rate limit another reader of the account's GitHub
// budget hit, so refreshes wait it out too.
func (s *Service) Limited(host string, until time.Time) {
	s.setPause(host, until, s.identityEpoch())
}

// RefreshInterval is how often the background refresh polls.
func (s *Service) RefreshInterval() time.Duration { return s.interval() }

// RefreshNow refreshes some pull requests of one clone at once, as Sync
// refreshes each of them, outside Sync's schedule: a hook event said they
// changed. It reads every pull request it is asked to, whatever Sync's rules
// say of it: retired, stopped after failures, or past the hour's request
// budget, which its reads still count toward. It honors the host's rate
// pause, which a rate-limited refresh here extends for Sync too, and returns
// the observations it made. It never enqueues or invokes generation.
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
		observation, err := s.read(ctx, clone, number)
		if ctx.Err() != nil {
			break
		}
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
		key := readKey(clone, number)
		seen := s.lastRead(key)
		seen.head, seen.open = observation.HeadSHA, observation.State == "OPEN" || observation.State == ""
		if err == nil || errors.Is(err, store.ErrConflict) {
			// This read stands for the last: GitHub's update time is compared
			// with its observation, and the checks with what it read.
			seen.updated, seen.checks = time.Time{}, observation.Checks
		}
		s.recordRead(key, err, epoch, seen)
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

// Saved lists the clone's saved reviews of its repository, from the store's
// summaries: no document is read.
func Saved(ctx context.Context, clone repos.Clone) ([]SavedReview, error) {
	versions, err := store.Open(clone.CommonDir, "", false)
	if err != nil {
		return nil, err
	}
	rows, err := versions.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var saved []SavedReview
	for _, row := range rows {
		if strings.EqualFold(row.Key.Repository, clone.Repository) && row.Latest != nil {
			review := SavedReview{Number: row.Key.Number, State: row.PRState, Head: row.Latest.Head}
			if generated, err := time.Parse(time.RFC3339Nano, row.Latest.GeneratedAt); err == nil {
				review.Generated, review.Started = generated, generated.Add(-row.Latest.Elapsed)
			}
			saved = append(saved, review)
		}
	}
	return saved, nil
}

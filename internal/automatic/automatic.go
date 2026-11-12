// Package automatic is the daemon's scheduler for reviews nobody asked for:
// it turns new pull request heads and new review requests, seen after
// automatic generation was enabled, into jobs on the existing queue. It has
// no model backend and no GitHub client of its own; discovery feeds it
// snapshots and jobs.Queue.SubmitAutomatic prepares, refuses or queues each
// job with the same preparation and isolation as a confirmed one.
package automatic

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/store"
)

// NoticeUnavailable is the notice of a repository whose triggered PR got no
// job because GitHub or the clone stayed unavailable; the most recent notice
// wins. A "daily_cap" notice an older baum saved is dropped on load.
const NoticeUnavailable = "unavailable"

// legacyNoticeDailyCap is the notice of the daily spending cap baum no longer
// has; legacyNoticeCursorAgent is the one of the cursor-agent acknowledgement,
// which the desktop app no longer asks for. Both are dropped on load.
const (
	legacyNoticeDailyCap    = "daily_cap"
	legacyNoticeCursorAgent = "cursor_agent_acknowledgement_required"
)

// maxAttempts bounds retries of a head whose job could not be prepared
// (GitHub or the clone unavailable); the next head triggers again.
const maxAttempts = 5

type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
	Preferences(context.Context) (repos.Preferences, error)
	UpdatePreferences(context.Context, func(repos.Preferences) repos.Preferences) (repos.Preferences, error)
	RepositorySettings(ctx context.Context, repository string) (repos.Settings, bool, error)
}

type Queue interface {
	SubmitAutomatic(context.Context, jobs.AutomaticRequest) (jobs.Job, error)
	// Interrupted lists automatic jobs a daemon stop or restart cut short
	// that may be retried once.
	Interrupted() []jobs.Job
	// Refused lists automatic jobs cancelled before any model call without
	// using up their head (the PR turned draft, or its clone went away).
	Refused() []jobs.Job
	// CancelDisallowedAutomatic cancels queued automatic jobs the gate
	// (Scheduler.Gate) now refuses.
	CancelDisallowedAutomatic() []jobs.Job
	// QueuedAutomatic lists the automatic jobs waiting to start.
	QueuedAutomatic() []jobs.Job
}

// queuedPerSlot bounds the automatic jobs a pass leaves waiting in the queue:
// this many for each job the queue runs at once (repos.Preferences.
// Concurrency). Heads past it stay pending, so a backlog waits in the
// scheduler, where a newer head simply replaces a pending one, and a pass
// submits the PRs that have waited longest first.
const queuedPerSlot = 2

// Scheduler decides which pull requests get automatic jobs. Run it once in
// the daemon; Observe and Trigger may be called from other goroutines.
type Scheduler struct {
	Registry Registry
	Queue    Queue
	// Path is the application database recording what was already seen, so a
	// restart neither forgets pending work nor treats old heads as new.
	Path string
	// Discover polls one repository's open pull requests; Trigger and
	// CatchUp use it.
	Discover func(context.Context, github.Repository) (discovery.Snapshot, error)
	// Discovered, when set, receives CatchUp's polls in place of Observe: the
	// daemon passes background discovery's, which also brings the stack
	// groups up to date before CatchUp reads them.
	Discovered func(context.Context, discovery.Snapshot)
	// Reviewed returns the head of the PR's latest saved review, "" if none.
	// Nil uses SavedHead.
	Reviewed func(context.Context, repos.Clone, int) (string, error)
	// Groups holds the review groups; the reviews of stack groups follow
	// their members' (see passGroups). Nil generates no group review.
	Groups Groups
	// Hold reports that the daemon is stopping or waiting to restart as a
	// newer version; nothing is submitted meanwhile, and the new daemon's
	// passes submit what is pending.
	Hold func() bool
	// Notify publishes a repository's changed status (a notice) for the app
	// and the desktop app's notifications.
	Notify   func(Status)
	Now      func() time.Time
	Interval time.Duration
	Log      *slog.Logger

	once sync.Once
	wake chan struct{}
	// run is a one-slot lock held by a pass or by an identity change, which
	// can hold it for a whole device sign-in. A pass waiting for it gives up
	// when its context ends, so the daemon can stop meanwhile.
	run   chan struct{}
	mu    sync.Mutex
	state *fileState
	// observed is when the newest snapshot Observe took for each repository
	// was fetched. Background refresh and Trigger observe from different
	// goroutines, so an older snapshot can arrive after a newer one.
	observed         map[string]time.Time
	resetAt          time.Time
	identitySince    atomic.Int64
	identityChanging atomic.Bool
}

// ResetIdentity forgets account-dependent PR baselines while preserving
// repository preferences and saved reviews.
func (s *Scheduler) ResetIdentity() {
	finish := s.BeginIdentityChange()
	finish(true)
}

// BeginIdentityChange stops submission before credentials change. The returned
// function must be called once to release the fence, even on failed sign-in.
func (s *Scheduler) BeginIdentityChange() func(bool) {
	s.init()
	s.run <- struct{}{}
	s.identityChanging.Store(true)
	return func(changed bool) {
		defer func() { <-s.run }()
		defer s.identityChanging.Store(false)
		if !changed {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resetAt = s.now().UTC()
		s.identitySince.Store(s.resetAt.UnixNano())
		for _, repository := range s.load().Repositories {
			repository.Since = s.resetAt
			repository.PRs = map[int]*prState{}
			repository.Groups = map[string]*groupState{}
			repository.AwaitingBaseline = true
		}
		s.observed = map[string]time.Time{}
		s.save()
		s.Wake()
	}
}

// Status is one repository's automatic generation as the app shows it.
type Status struct {
	Repository string `json:"repository"`
	// Enabled is effective: the repository's explicit choice, else the
	// global preference.
	Enabled bool `json:"enabled"`
	// Notice is the latest reason a triggered PR got no job:
	// NoticeUnavailable or a jobs.NotAutomaticError reason.
	Notice  string `json:"notice,omitempty"`
	Pending int    `json:"pending"`
}

// Overview is GET /api/automatic.
type Overview struct {
	Enabled bool `json:"enabled"`
	// PullRequests is which pull requests get automatic reviews: both,
	// authored, requested or all.
	PullRequests string   `json:"pull_requests"`
	Repositories []Status `json:"repositories"`
}

type fileState struct {
	Repositories map[string]*repoState `json:"repositories"`
}

// repoState exists while automatic generation is enabled for a repository.
// It starts from the first snapshot seen after enabling (the baseline), so
// pull requests already open then are not caught up. The paused_until and
// paused_cap of the daily cap an older baum had are ignored on load.
type repoState struct {
	Since time.Time        `json:"since"`
	PRs   map[int]*prState `json:"prs"`
	// Groups are the repository's stack groups by ID.
	Groups map[string]*groupState `json:"groups,omitempty"`
	Notice string                 `json:"notice,omitempty"`
	// AwaitingBaseline: enabled at Since, but no discovery snapshot fetched
	// since then has been seen yet; older snapshots are not a baseline.
	AwaitingBaseline bool `json:"awaiting_baseline,omitempty"`
}

type prState struct {
	Head      string `json:"head"`
	Requested bool   `json:"requested"`
	TeamKnown bool   `json:"team_known"`
	Draft     bool   `json:"draft,omitempty"`
	// Eligible is whether the latest snapshot allowed an automatic job.
	Eligible bool `json:"eligible,omitempty"`
	// Pending is a head that needs an automatic job; Trigger says why.
	Pending string `json:"pending,omitempty"`
	Trigger string `json:"trigger,omitempty"`
	// Waiting is when the PR started waiting for its pending head's job,
	// which orders a pass: a newer head that replaces a pending one keeps
	// it. Meaningless while nothing is pending.
	Waiting time.Time `json:"waiting,omitzero"`
	// Done is the last head queued or found already reviewed.
	Done     string    `json:"done,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	// Retried is the head whose interrupted automatic run was already
	// put back as pending, so it is retried at most once.
	Retried string `json:"retried,omitempty"`
	// Refused is the ID of the last job refused before it started (Queue
	// Refused) whose head was put back as pending, so each is put back once.
	Refused string `json:"refused,omitempty"`
}

// wait makes head pending for trigger, with fresh attempts. A PR already
// waiting for another head keeps its place (Waiting).
func (p *prState) wait(head, trigger string, now time.Time) {
	if p.Pending == "" || p.Waiting.IsZero() {
		p.Waiting = now.UTC()
	}
	p.Pending, p.Trigger, p.Attempts, p.RetryAt = head, trigger, 0, time.Time{}
}

func (s *Scheduler) init() {
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.run = make(chan struct{}, 1)
	})
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Wake runs a pass soon: settings changed or a job finished.
func (s *Scheduler) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run submits pending jobs until ctx ends: at once, whenever woken, and every
// Interval (30 s by default) to retry refusals.
func (s *Scheduler) Run(ctx context.Context) {
	s.init()
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Trigger looks at one repository again now and feeds what it finds through
// Observe, the only way pull requests become automatic jobs. The daemon calls
// it when a hook event showed a pull request's head moved (internal/
// hookrefresh), with Discover polling before discovery's deadline but within
// its rate budget.
func (s *Scheduler) Trigger(ctx context.Context, repository string) error {
	repo, err := github.ParseRepository(repository)
	if err != nil {
		return err
	}
	if s.Discover == nil {
		return errors.New("no discovery")
	}
	snapshot, err := s.Discover(ctx, repo)
	if err != nil {
		return err
	}
	s.Observe(ctx, snapshot)
	return nil
}

type repository struct {
	clone    repos.Clone
	settings repos.Settings
	enabled  bool
}

type view struct {
	prefs repos.Preferences
	repos map[string]repository
}

// current reads the preferences and registered repositories. A repository
// is listed even when no clone is available, or none is registered, so its
// state survives.
func (s *Scheduler) current(ctx context.Context) (view, error) {
	prefs, err := s.Registry.Preferences(ctx)
	if err != nil {
		return view{}, err
	}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return view{}, err
	}
	v := view{prefs: prefs, repos: map[string]repository{}}
	for _, p := range picked {
		name := strings.ToLower(p.Repository)
		r := repository{settings: p.Settings, enabled: p.Settings.AutomaticEnabled(prefs)}
		// A repository without a clone keeps the zero clone, which is not
		// Available: it is observed but nothing is submitted until one appears.
		for _, c := range p.Clones {
			if r.clone.ID == "" || (!r.clone.Available && c.Available) {
				r.clone = c
			}
		}
		v.repos[name] = r
	}
	return v, nil
}

func (s *Scheduler) load() *fileState {
	if s.state != nil {
		return s.state
	}
	s.state = &fileState{Repositories: map[string]*repoState{}}
	raw, err := appdb.Read(s.Path, "state", "automatic")
	if err == nil {
		var loaded fileState
		if err = json.Unmarshal(raw, &loaded); err == nil && loaded.Repositories != nil {
			s.state = &loaded
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Starting over records new baselines: nothing is caught up.
		s.log().Warn("automatic generation state unreadable; starting from current pull requests", "error", err)
	}
	for _, r := range s.state.Repositories {
		if r.PRs == nil {
			r.PRs = map[int]*prState{}
		}
		if r.Groups == nil {
			r.Groups = map[string]*groupState{}
		}
		if r.Notice == legacyNoticeDailyCap || r.Notice == legacyNoticeCursorAgent {
			r.Notice = ""
		}
	}
	return s.state
}

func (s *Scheduler) save() {
	raw, err := json.Marshal(s.state)
	if err == nil {
		err = appdb.Write(s.Path, "state", "automatic", raw)
	}
	if err != nil {
		s.log().Warn("automatic generation state not saved", "error", err)
	}
}

// prune forgets repositories that are no longer registered or enabled, so
// enabling one again starts from a new baseline. Called with mu held.
func (s *Scheduler) prune(v view) bool {
	changed := false
	for name := range s.state.Repositories {
		if r, ok := v.repos[name]; !ok || !r.enabled {
			delete(s.state.Repositories, name)
			changed = true
		}
	}
	return changed
}

// enable records that automatic generation is on for a repository from now
// on; its baseline is the first snapshot fetched after this. Called with mu
// held.
func (s *Scheduler) enable(name string) *repoState {
	rs := &repoState{Since: s.now().UTC(), PRs: map[int]*prState{}, Groups: map[string]*groupState{}, AwaitingBaseline: true}
	s.state.Repositories[name] = rs
	return rs
}

// eligible: the viewer's review is requested or the viewer wrote it, as
// the preferences narrow or widen that to every pull request, and it is not a
// draft, not from a fork and not by an author without known write access.
func eligible(pr discovery.PullRequest, prefs repos.Preferences) bool {
	return prefs.Covers(pr.AuthoredByMe, pr.ReviewRequestedFromMe) && !pr.Draft && !pr.Fork && pr.AuthorHasWrite != nil && *pr.AuthorHasWrite
}

// Observe records a complete discovery snapshot. The first one after a
// repository is enabled is its baseline; afterwards a new head, or a new
// review request, on an eligible PR leaves a pending job for the next pass.
// Background discovery calls it after every successful poll, and Trigger
// after its own; a snapshot fetched before the newest one observed for its
// repository is ignored.
func (s *Scheduler) Observe(ctx context.Context, snapshot discovery.Snapshot) {
	v, err := s.current(ctx)
	if err != nil {
		s.log().Warn("automatic generation skipped a snapshot", "error", err)
		return
	}
	name := strings.ToLower(snapshot.Repository)
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.FetchedAt.Before(s.resetAt) {
		return
	}
	if snapshot.FetchedAt.Before(s.observed[name]) {
		return
	}
	if s.observed == nil {
		s.observed = map[string]time.Time{}
	}
	s.observed[name] = snapshot.FetchedAt
	state := s.load()
	changed := s.prune(v)
	r, ok := v.repos[name]
	if !ok || !r.enabled {
		if changed {
			s.save()
		}
		return
	}
	rs := state.Repositories[name]
	if rs == nil {
		rs = s.enable(name)
		changed = true
	}
	if rs.AwaitingBaseline {
		// A snapshot fetched before automatic generation was enabled (a
		// cached one) must not become the baseline: what changed between it
		// and the enabling would be caught up.
		if snapshot.FetchedAt.Before(rs.Since) {
			if changed {
				s.save()
			}
			return
		}
		for _, pr := range snapshot.PullRequests {
			// A run interrupted before this baseline (before an off, say) is
			// not retried for the head it recorded: re-enabling catches up on
			// nothing.
			rs.PRs[pr.Number] = &prState{Head: pr.HeadSHA, Retried: pr.HeadSHA, Requested: pr.ReviewRequestedFromMe, TeamKnown: pr.TeamMembershipKnown, Draft: pr.Draft, Eligible: eligible(pr, v.prefs)}
		}
		rs.AwaitingBaseline = false
		s.save()
		return
	}
	before, _ := json.Marshal(rs)
	open := map[int]bool{}
	pending := false
	for _, pr := range snapshot.PullRequests {
		open[pr.Number] = true
		previous := rs.PRs[pr.Number]
		if previous == nil {
			previous = &prState{}
			rs.PRs[pr.Number] = previous
		}
		newHead := previous.Head != pr.HeadSHA
		// A team request that only became visible because team membership
		// became readable is not new.
		newRequest := pr.ReviewRequestedFromMe && !previous.Requested && (previous.TeamKnown || !pr.TeamMembershipKnown)
		// Marking a draft ready for review counts as a trigger.
		ready := previous.Head != "" && previous.Draft && !pr.Draft
		previous.Head, previous.Requested, previous.TeamKnown, previous.Draft = pr.HeadSHA, pr.ReviewRequestedFromMe, pr.TeamMembershipKnown, pr.Draft
		previous.Eligible = eligible(pr, v.prefs)
		switch {
		case (newHead || newRequest || ready) && previous.Eligible && pr.HeadSHA != previous.Done:
			trigger := jobs.TriggerReadyForReview
			switch {
			case newHead:
				trigger = jobs.TriggerNewHead
			case newRequest:
				trigger = jobs.TriggerReviewRequested
			}
			previous.wait(pr.HeadSHA, trigger, s.now())
		case previous.Pending != "" && (previous.Pending != pr.HeadSHA || !eligible(pr, v.prefs)):
			previous.Pending, previous.Trigger = "", ""
		}
		pending = pending || previous.Pending != ""
	}
	for number := range rs.PRs {
		if !open[number] {
			delete(rs.PRs, number)
		}
	}
	// Snapshots usually repeat; write the file only when something changed.
	if after, _ := json.Marshal(rs); changed || string(after) != string(before) {
		s.save()
	}
	if pending {
		s.Wake()
	}
}

// SetupCatchUp is how many pull requests desktop setup asks CatchUp for.
const SetupCatchUp = 3

// CatchUp queues automatic reviews of the n most recently updated open pull
// requests automatic generation covers, across its repositories, though they
// were open before it was enabled. Desktop setup asks for it once, so the
// first reviews run while the person takes the tour. Each repository is
// polled now, and the poll is its baseline when it has none yet; the chosen
// pull requests become pending with jobs.TriggerSetup and the next pass
// submits them like any other, skipping a head already reviewed or queued.
// A pull request in a stack group counts with its group as one and the
// group's eligible members are caught up together, so the group review can
// follow theirs (passGroups).
func (s *Scheduler) CatchUp(ctx context.Context, n int) error {
	if s.Discover == nil {
		return errors.New("no discovery")
	}
	v, err := s.current(ctx)
	if err != nil {
		return err
	}
	// Enabling is recorded before the polls, so they can be the baselines.
	s.mu.Lock()
	state := s.load()
	changed := s.prune(v)
	names := make([]string, 0, len(v.repos))
	for name, r := range v.repos {
		if !r.enabled {
			continue
		}
		names = append(names, name)
		if state.Repositories[name] == nil {
			s.enable(name)
			changed = true
		}
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	sort.Strings(names)
	type candidate struct {
		name string
		pr   discovery.PullRequest
	}
	// The eligible pull requests by repository, and all of them by recency.
	eligibleIn := map[string][]discovery.PullRequest{}
	var candidates []candidate
	for _, name := range names {
		repo, err := github.ParseRepository(name)
		if err != nil {
			continue
		}
		snapshot, err := s.Discover(ctx, repo)
		if err != nil {
			s.log().Warn("setup catch-up skipped a repository", "repository", name, "error", err)
			continue
		}
		if s.Discovered != nil {
			s.Discovered(ctx, snapshot)
		} else {
			s.Observe(ctx, snapshot)
		}
		for _, pr := range snapshot.PullRequests {
			if eligible(pr, v.prefs) {
				eligibleIn[name] = append(eligibleIn[name], pr)
				candidates = append(candidates, candidate{name, pr})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].pr.UpdatedAt.After(candidates[j].pr.UpdatedAt) })
	type prKey struct {
		name   string
		number int
	}
	stacks := map[prKey][]int{}
	if s.Groups != nil {
		groups, err := s.Groups.List(ctx)
		if err != nil {
			s.log().Warn("setup catch-up reads no stack groups", "error", err)
		}
		for _, g := range groups {
			if g.Stack && len(g.Members) >= reviewgroups.MinMembers {
				for _, number := range g.Members {
					stacks[prKey{g.Repository, number}] = g.Members
				}
			}
		}
	}
	var chosen []candidate
	taken := map[prKey]bool{}
	for _, c := range candidates {
		if n == 0 {
			break
		}
		if taken[prKey{c.name, c.pr.Number}] {
			continue
		}
		n--
		members := stacks[prKey{c.name, c.pr.Number}]
		if members == nil {
			members = []int{c.pr.Number}
		}
		for _, pr := range eligibleIn[c.name] {
			if slices.Contains(members, pr.Number) {
				taken[prKey{c.name, pr.Number}] = true
				chosen = append(chosen, candidate{c.name, pr})
			}
		}
	}
	s.mu.Lock()
	queued := false
	for _, c := range chosen {
		rs := s.load().Repositories[c.name]
		if rs == nil {
			continue
		}
		pr := rs.PRs[c.pr.Number]
		// A newer snapshot may have seen it turn ineligible at the same head.
		if pr == nil || pr.Head != c.pr.HeadSHA || !pr.Eligible || pr.Pending != "" || pr.Done == pr.Head {
			continue
		}
		// The baseline marked the head retried; an interrupted catch-up run
		// gets its one retry like any other.
		pr.wait(pr.Head, jobs.TriggerSetup, s.now())
		pr.Retried = ""
		queued = true
	}
	if queued {
		s.save()
	}
	s.mu.Unlock()
	if queued {
		s.Wake()
	}
	return nil
}

type work struct {
	name    string
	repo    repository
	number  int
	head    string
	trigger string
	waiting time.Time
}

// Pass submits every pending job that is due. Exposed for tests.
func (s *Scheduler) Pass(ctx context.Context) {
	s.init()
	select {
	case s.run <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.run }()
	v, err := s.current(ctx)
	if err != nil {
		s.log().Warn("automatic generation pass skipped", "error", err)
		return
	}
	// Whatever was turned off since the last pass loses its queued jobs.
	s.Queue.CancelDisallowedAutomatic()
	now := s.now()
	var due []work
	interrupted := s.Queue.Interrupted()
	refused := s.Queue.Refused()
	queued := s.Queue.QueuedAutomatic()
	s.mu.Lock()
	state := s.load()
	changed := s.prune(v)
	// Enabling is dated when it is first seen, so the baseline is a
	// snapshot fetched after it.
	for name, r := range v.repos {
		if r.enabled && state.Repositories[name] == nil {
			s.enable(name)
			changed = true
		}
	}
	// An automatic run a daemon stop or restart cut short is retried once,
	// if its PR is still open, eligible and at that head.
	for _, j := range interrupted {
		rs := state.Repositories[strings.ToLower(j.Repository)]
		if rs == nil {
			continue
		}
		pr := rs.PRs[j.PR]
		if pr == nil || pr.Head != j.HeadSHA || !pr.Eligible || pr.Pending != "" || pr.Retried == j.HeadSHA {
			continue
		}
		pr.wait(j.HeadSHA, jobs.TriggerRetry, now)
		pr.Retried = j.HeadSHA
		changed = true
	}
	// A queued job stopped before it started did not use up its head: one
	// stopped because its PR turned draft is pending again once the PR is
	// eligible (ready for review), and one whose clone went away once a
	// clone is back. Jobs from before the repository's baseline are not
	// caught up.
	for _, j := range refused {
		rs := state.Repositories[strings.ToLower(j.Repository)]
		if rs == nil || j.CreatedAt.Before(rs.Since) {
			continue
		}
		pr := rs.PRs[j.PR]
		if pr == nil || pr.Head != j.HeadSHA || !pr.Eligible || pr.Pending != "" || pr.Refused == j.ID {
			continue
		}
		trigger := j.Trigger
		if j.Reason == jobs.ReasonDraft {
			trigger = jobs.TriggerReadyForReview
		}
		pr.wait(j.HeadSHA, trigger, now)
		pr.Refused = j.ID
		changed = true
	}
	for name, rs := range state.Repositories {
		r := v.repos[name]
		if !r.clone.Available {
			continue
		}
		for number, pr := range rs.PRs {
			if pr.Pending != "" && !now.Before(pr.RetryAt) {
				due = append(due, work{name, r, number, pr.Pending, pr.Trigger, pr.Waiting})
			}
		}
	}
	// The PRs that have waited longest go first, whatever their repository.
	slices.SortFunc(due, func(a, b work) int {
		return cmp.Or(a.waiting.Compare(b.waiting), strings.Compare(a.name, b.name), cmp.Compare(a.number, b.number))
	})
	if changed {
		s.save()
	}
	s.mu.Unlock()
	// At most queuedPerSlot automatic jobs per running slot wait in the
	// queue; the rest stay pending for a later pass. A new head that a PR's
	// waiting review takes over adds none, so it is always submitted.
	room := queuedPerSlot * v.prefs.Concurrency()
	type prKey struct {
		name   string
		number int
	}
	waiting := map[prKey]bool{}
	for _, j := range queued {
		room--
		if j.Group == "" && j.Kind == "" {
			waiting[prKey{strings.ToLower(j.Repository), j.PR}] = true
		}
	}
	paused := map[string]bool{}
	for _, w := range due {
		if ctx.Err() != nil || (s.Hold != nil && s.Hold()) {
			return
		}
		replaces := waiting[prKey{w.name, w.number}]
		if paused[w.name] || (room <= 0 && !replaces) {
			continue
		}
		stop, queuedOne := s.submit(ctx, v, w, now)
		if queuedOne && !replaces {
			room--
		}
		if stop == stopAll {
			return
		}
		if stop == stopRepository {
			paused[w.name] = true
		}
	}
	s.passGroups(ctx, v, interrupted, refused, paused, now)
}

type outcome int

const (
	carryOn outcome = iota
	stopRepository
	stopAll
)

// submit asks the queue for w's job and records the outcome; queued reports
// that a job joined the queue.
func (s *Scheduler) submit(ctx context.Context, v view, w work, now time.Time) (_ outcome, queued bool) {
	reviewed := s.Reviewed
	if reviewed == nil {
		reviewed = SavedHead
	}
	var err error
	var job jobs.Job
	if head, rerr := reviewed(ctx, w.repo.clone, w.number); rerr == nil && head == w.head {
		err = jobs.ErrDuplicate
	} else {
		job, err = s.Queue.SubmitAutomatic(ctx, jobs.AutomaticRequest{Item: jobs.Item{Clone: w.repo.clone.ID, PR: w.number}, Repository: w.repo.clone.Repository, Head: w.head, Trigger: w.trigger})
		queued = err == nil
	}
	s.mu.Lock()
	rs := s.load().Repositories[w.name]
	var pr *prState
	if rs != nil {
		pr = rs.PRs[w.number]
	}
	if pr == nil || pr.Pending != w.head {
		// Disabled, closed or moved on while this was submitted.
		s.mu.Unlock()
		return carryOn, queued
	}
	result := carryOn
	notice := rs.Notice
	var notAutomatic *jobs.NotAutomaticError
	var notAllowed *jobs.NotAllowedError
	quiet := false
	switch {
	case err == nil:
		s.log().Info("queued an automatic review", "repository", w.name, "pr", w.number, "job", job.ID, "trigger", w.trigger)
		pr.Done, pr.Pending, pr.Trigger = w.head, "", ""
		notice = ""
	case errors.Is(err, jobs.ErrDuplicate):
		pr.Done, pr.Pending, pr.Trigger = w.head, "", ""
	case errors.Is(err, jobs.ErrBusy):
		// Its running job's completion wakes the next pass. (A waiting
		// automatic review of another head is never busy: it takes this head.)
	case errors.Is(err, jobs.ErrRestarting):
		result = stopAll
	case errors.Is(err, jobs.ErrNeedsClone):
		// The clone went away since this pass read it: stay pending without
		// using up an attempt; passes skip the repository until one is back.
		// Nothing changed that anyone needs announcing.
		result, quiet = stopRepository, true
	case errors.Is(err, jobs.ErrHeadChanged):
		// GitHub shows a newer head than discovery did: it stays pending,
		// keeping its place (Waiting), and is tried again a minute later,
		// by when discovery has usually replaced the pending head.
		pr.RetryAt = now.Add(time.Minute)
	case errors.As(err, &notAllowed):
		// Turned off since this pass read the settings: the next pass drops
		// the repository's state, or submits once it is back on.
		result = stopRepository
	case errors.As(err, &notAutomatic):
		pr.Pending, pr.Trigger = "", ""
		notice = notAutomatic.Reason
	default:
		pr.Attempts++
		if pr.Attempts >= maxAttempts {
			pr.Pending, pr.Trigger = "", ""
			notice = NoticeUnavailable
		} else {
			pr.RetryAt = now.Add(time.Duration(1<<pr.Attempts) * time.Minute)
		}
		s.log().Warn("automatic review not queued", "repository", w.name, "pr", w.number, "attempt", pr.Attempts, "error", err)
	}
	announce := notice != rs.Notice || (result == stopRepository && !quiet)
	rs.Notice = notice
	s.save()
	status := s.status(w.name, w.repo, rs)
	s.mu.Unlock()
	if announce {
		s.notify([]Status{status})
	}
	return result, queued
}

func (s *Scheduler) notify(statuses []Status) {
	if s.Notify == nil {
		return
	}
	for _, status := range statuses {
		s.Notify(status)
	}
}

// status is called with mu held.
func (s *Scheduler) status(name string, r repository, rs *repoState) Status {
	st := Status{Repository: name, Enabled: r.enabled}
	if rs != nil {
		st.Notice = rs.Notice
		for _, pr := range rs.PRs {
			if pr.Pending != "" {
				st.Pending++
			}
		}
	}
	return st
}

// Overview reports the preferences and every registered repository.
func (s *Scheduler) Overview(ctx context.Context) (Overview, error) {
	v, err := s.current(ctx)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{Enabled: v.prefs.Automatic, PullRequests: cmp.Or(v.prefs.PullRequests, "both"), Repositories: []Status{}}
	names := make([]string, 0, len(v.repos))
	for name := range v.repos {
		names = append(names, name)
	}
	sort.Strings(names)
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.load()
	for _, name := range names {
		r := v.repos[name]
		rs := state.Repositories[name]
		if !r.enabled {
			rs = nil
		}
		o.Repositories = append(o.Repositories, s.status(name, r, rs))
	}
	return o, nil
}

// Why Gate refuses an automatic job.
const (
	GateOff               = "automatic_off"
	GateRepositoryOff     = "repository_automatic_off"
	GateRepositoryRemoved = "repository_removed"
	GateUnreadable        = jobs.ReasonSettingsUnreadable
)

// Gate reads the current settings for an automatic job, for
// jobs.Queue.SetAutomaticGate: the queue asks it when the job is submitted
// and again before it starts, so turning generation off (globally or for the
// repository) stops jobs that are queued but not started. It reads only the
// registry database, never the scheduler's state or the queue: the queue's
// mutex is held.
func (s *Scheduler) Gate(j jobs.Job) jobs.Admission {
	if s.identityChanging.Load() {
		return jobs.Admission{Reason: GateOff}
	}
	since := s.identitySince.Load()
	obsolete := since != 0 && j.CreatedAt.UnixNano() < since
	if obsolete {
		return jobs.Admission{Reason: GateOff}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefs, err := s.Registry.Preferences(ctx)
	if err != nil {
		return jobs.Admission{Reason: GateUnreadable}
	}
	settings, registered, err := s.Registry.RepositorySettings(ctx, j.Repository)
	switch {
	case err != nil:
		return jobs.Admission{Reason: GateUnreadable}
	case !prefs.Automatic:
		return jobs.Admission{Reason: GateOff}
	case !registered:
		return jobs.Admission{Reason: GateRepositoryRemoved}
	case !settings.AutomaticEnabled(prefs):
		return jobs.Admission{Reason: GateRepositoryOff}
	}
	return jobs.Admission{}
}

// SavedHead is the head of the PR's latest saved review in the clone's
// version store, "" when there is none.
func SavedHead(ctx context.Context, clone repos.Clone, number int) (string, error) {
	versions, err := store.Open(clone.CommonDir, "", false)
	if err != nil {
		return "", err
	}
	latest, err := versions.Latest(ctx, store.Key{Repository: clone.Repository, Number: number})
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return latest.Metadata.Head, nil
}

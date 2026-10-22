// Package automatic is the daemon's scheduler for reviews nobody asked for:
// it turns new pull request heads and new review requests, seen after
// automatic generation was enabled, into jobs on the existing queue. It has
// no model backend and no GitHub client of its own; discovery feeds it
// snapshots and jobs.Queue.SubmitAutomatic prepares, refuses or queues each
// job with the same estimates and isolation as a confirmed one.
package automatic

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Notices shown for a repository; the most recent one wins.
const (
	NoticeDailyCap    = "daily_cap"
	NoticeUnavailable = "unavailable"
)

// maxAttempts bounds retries of a head whose job could not be prepared
// (GitHub or the clone unavailable); the next head triggers again.
const maxAttempts = 5

type Registry interface {
	List(context.Context) ([]repos.Clone, error)
	Preferences(context.Context) (repos.Preferences, error)
	UpdatePreferences(context.Context, func(repos.Preferences) repos.Preferences) (repos.Preferences, error)
	RepositorySettings(ctx context.Context, repository string) (repos.Settings, bool, error)
}

type Queue interface {
	SubmitAutomatic(context.Context, jobs.AutomaticRequest) (jobs.Job, error)
	AutomaticSpend(repository string, day time.Time) (float64, bool)
	// Interrupted lists automatic jobs a daemon stop or restart cut short
	// that may be retried once.
	Interrupted() []jobs.Job
	// Refused lists automatic jobs cancelled before any model call without
	// using up their head (the daily cap reached at start, or the PR turned
	// draft).
	Refused() []jobs.Job
	// CancelDisallowedAutomatic cancels queued automatic jobs the gate
	// (Scheduler.Gate) now refuses.
	CancelDisallowedAutomatic() []jobs.Job
}

// Scheduler decides which pull requests get automatic jobs. Run it once in
// the daemon; Observe and Trigger may be called from other goroutines.
type Scheduler struct {
	Registry Registry
	Queue    Queue
	// Path is the private JSON file recording what was already seen, so a
	// restart neither forgets pending work nor treats old heads as new.
	Path string
	// Discover polls one repository's open pull requests; Trigger uses it.
	Discover func(context.Context, github.Repository) (discovery.Snapshot, error)
	// Reviewed returns the head of the PR's latest saved review, "" if none.
	// Nil uses SavedHead.
	Reviewed func(context.Context, repos.Clone, int) (string, error)
	// Hold reports that the daemon is stopping or waiting to restart as a
	// newer version; nothing is submitted meanwhile, so the queue can drain.
	Hold func() bool
	// Notify publishes a repository's changed status (a pause or a notice)
	// for the app and the desktop app's notifications.
	Notify   func(Status)
	Now      func() time.Time
	Interval time.Duration
	Log      *slog.Logger

	once  sync.Once
	wake  chan struct{}
	runMu sync.Mutex
	mu    sync.Mutex
	state *fileState
	// observed is when the newest snapshot Observe took for each repository
	// was fetched. Background refresh and Trigger observe from different
	// goroutines, so an older snapshot can arrive after a newer one.
	observed map[string]time.Time
}

// Status is one repository's automatic generation as the app shows it.
type Status struct {
	Repository string `json:"repository"`
	// Enabled is effective: the repository's explicit choice, else the
	// global preference.
	Enabled     bool       `json:"enabled"`
	DailyCap    *float64   `json:"daily_cap"`
	SpentToday  float64    `json:"spent_today"`
	SpentKnown  bool       `json:"spent_known"`
	PausedUntil *time.Time `json:"paused_until,omitempty"`
	// Notice is the latest reason a triggered PR got no job: NoticeDailyCap,
	// NoticeUnavailable or a jobs.NotAutomaticError reason.
	Notice  string `json:"notice,omitempty"`
	Pending int    `json:"pending"`
}

// Overview is GET /api/automatic.
type Overview struct {
	Enabled                 bool `json:"enabled"`
	CursorAgentAcknowledged bool `json:"cursor_agent_acknowledged"`
	// PullRequests is which pull requests get automatic reviews: both,
	// authored or requested.
	PullRequests string   `json:"pull_requests"`
	Repositories []Status `json:"repositories"`
}

type fileState struct {
	Repositories map[string]*repoState `json:"repositories"`
}

// repoState exists while automatic generation is enabled for a repository.
// It starts from the first snapshot seen after enabling (the baseline), so
// pull requests already open then are not caught up.
type repoState struct {
	Since       time.Time        `json:"since"`
	PRs         map[int]*prState `json:"prs"`
	PausedUntil time.Time        `json:"paused_until,omitzero"`
	Notice      string           `json:"notice,omitempty"`
	// PausedCap is the daily cap that caused the pause; raising or clearing
	// the cap lifts it.
	PausedCap *float64 `json:"paused_cap,omitempty"`
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

func (s *Scheduler) init() {
	s.once.Do(func() { s.wake = make(chan struct{}, 1) })
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
// Interval (30 s by default) to retry refusals and lift expired pauses.
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
// is listed even when no clone is available, so its state survives.
func (s *Scheduler) current(ctx context.Context) (view, error) {
	prefs, err := s.Registry.Preferences(ctx)
	if err != nil {
		return view{}, err
	}
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return view{}, err
	}
	v := view{prefs: prefs, repos: map[string]repository{}}
	for _, c := range clones {
		name := strings.ToLower(c.Repository)
		r, seen := v.repos[name]
		if !seen || (!r.clone.Available && c.Available) {
			v.repos[name] = repository{clone: c, settings: c.Settings, enabled: c.Settings.AutomaticEnabled(prefs)}
		}
	}
	return v, nil
}

func (s *Scheduler) load() *fileState {
	if s.state != nil {
		return s.state
	}
	s.state = &fileState{Repositories: map[string]*repoState{}}
	raw, err := os.ReadFile(s.Path)
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
	}
	return s.state
}

func (s *Scheduler) save() {
	raw, err := json.Marshal(s.state)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(s.Path), 0o700)
	}
	if err == nil {
		err = generate.WriteFileAtomic(s.Path, raw)
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
	rs := &repoState{Since: s.now().UTC(), PRs: map[int]*prState{}, AwaitingBaseline: true}
	s.state.Repositories[name] = rs
	return rs
}

// eligible: the viewer's review is requested or the viewer wrote it, as
// the preferences narrow that, and it is not a draft, not from a fork and not
// by an author without known write access.
func eligible(pr discovery.PullRequest, prefs repos.Preferences) bool {
	var wanted bool
	switch prefs.PullRequests {
	case repos.PullRequestsAuthored:
		wanted = pr.AuthoredByMe
	case repos.PullRequestsRequested:
		wanted = pr.ReviewRequestedFromMe
	default:
		wanted = pr.ReviewRequestedFromMe || pr.AuthoredByMe
	}
	return wanted && !pr.Draft && !pr.Fork && pr.AuthorHasWrite != nil && *pr.AuthorHasWrite
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
			previous.Pending, previous.Attempts, previous.RetryAt = pr.HeadSHA, 0, time.Time{}
			switch {
			case newHead:
				previous.Trigger = jobs.TriggerNewHead
			case newRequest:
				previous.Trigger = jobs.TriggerReviewRequested
			default:
				previous.Trigger = jobs.TriggerReadyForReview
			}
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

type work struct {
	name    string
	repo    repository
	number  int
	head    string
	trigger string
}

// Pass submits every pending job that is due. Exposed for tests.
func (s *Scheduler) Pass(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	v, err := s.current(ctx)
	if err != nil {
		s.log().Warn("automatic generation pass skipped", "error", err)
		return
	}
	// Whatever was turned off since the last pass loses its queued jobs.
	s.Queue.CancelDisallowedAutomatic()
	now := s.now()
	var due []work
	var notices []Status
	interrupted := s.Queue.Interrupted()
	refused := s.Queue.Refused()
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
		pr.Pending, pr.Trigger, pr.Retried, pr.Attempts, pr.RetryAt = j.HeadSHA, jobs.TriggerRetry, j.HeadSHA, 0, time.Time{}
		changed = true
	}
	// A queued job the daily cap stopped before it started did not use up
	// its head: the head is pending again, so this pass submits it and the cap
	// pauses the repository as it does at submission, and the head runs once
	// the pause ends. A job stopped because its PR turned draft waits until
	// the PR is eligible (ready for review) again. Jobs from before the
	// repository's baseline are not caught up.
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
		pr.Pending, pr.Trigger, pr.Refused, pr.Attempts, pr.RetryAt = j.HeadSHA, trigger, j.ID, 0, time.Time{}
		changed = true
	}
	names := make([]string, 0, len(state.Repositories))
	for name := range state.Repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rs := state.Repositories[name]
		r := v.repos[name]
		// A pause ends the next day, or as soon as the cap that caused it is
		// raised or removed; a lowered cap keeps it.
		capRaised := r.settings.DailyCap == nil || (rs.PausedCap != nil && *r.settings.DailyCap > *rs.PausedCap)
		if !rs.PausedUntil.IsZero() && (!now.Before(rs.PausedUntil) || capRaised) {
			rs.PausedUntil, rs.PausedCap, rs.Notice, changed = time.Time{}, nil, "", true
			notices = append(notices, s.status(name, r, rs, now))
		}
		if !rs.PausedUntil.IsZero() || !r.clone.Available {
			continue
		}
		numbers := make([]int, 0, len(rs.PRs))
		for number, pr := range rs.PRs {
			if pr.Pending != "" && !now.Before(pr.RetryAt) {
				numbers = append(numbers, number)
			}
		}
		sort.Ints(numbers)
		for _, number := range numbers {
			pr := rs.PRs[number]
			due = append(due, work{name, r, number, pr.Pending, pr.Trigger})
		}
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	s.notify(notices)
	paused := map[string]bool{}
	for _, w := range due {
		if ctx.Err() != nil || (s.Hold != nil && s.Hold()) {
			return
		}
		if paused[w.name] {
			continue
		}
		stop := s.submit(ctx, v, w, now)
		if stop == stopAll {
			return
		}
		if stop == stopRepository {
			paused[w.name] = true
		}
	}
}

type outcome int

const (
	carryOn outcome = iota
	stopRepository
	stopAll
)

func (s *Scheduler) submit(ctx context.Context, v view, w work, now time.Time) outcome {
	reviewed := s.Reviewed
	if reviewed == nil {
		reviewed = SavedHead
	}
	var err error
	var job jobs.Job
	if head, rerr := reviewed(ctx, w.repo.clone, w.number); rerr == nil && head == w.head {
		err = jobs.ErrDuplicate
	} else {
		day := startOfDay(now)
		job, err = s.Queue.SubmitAutomatic(ctx, jobs.AutomaticRequest{Item: jobs.Item{Clone: w.repo.clone.ID, PR: w.number}, Repository: w.repo.clone.Repository, Head: w.head, Trigger: w.trigger, DailyCap: w.repo.settings.DailyCap, Day: day})
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
		return carryOn
	}
	result := carryOn
	notice := rs.Notice
	var notAutomatic *jobs.NotAutomaticError
	var notAllowed *jobs.NotAllowedError
	switch {
	case err == nil:
		s.log().Info("queued an automatic review", "repository", w.name, "pr", w.number, "job", job.ID, "trigger", w.trigger)
		pr.Done, pr.Pending, pr.Trigger = w.head, "", ""
		notice = ""
	case errors.Is(err, jobs.ErrDuplicate):
		pr.Done, pr.Pending, pr.Trigger = w.head, "", ""
	case errors.Is(err, jobs.ErrBusy):
		// Its running job's completion wakes the next pass.
	case errors.Is(err, jobs.ErrRestarting):
		result = stopAll
	case errors.Is(err, jobs.ErrHeadChanged):
		// Discovery reports the newer head, which triggers again.
		pr.Pending, pr.Trigger = "", ""
	case errors.Is(err, jobs.ErrDailyCap):
		rs.PausedUntil = startOfDay(now).AddDate(0, 0, 1)
		if limit := w.repo.settings.DailyCap; limit != nil {
			rs.PausedCap = new(*limit)
		}
		notice = NoticeDailyCap
		result = stopRepository
		s.log().Info("automatic generation paused by the daily cap", "repository", w.name, "until", rs.PausedUntil)
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
	announce := notice != rs.Notice || result == stopRepository
	rs.Notice = notice
	s.save()
	status := s.status(w.name, w.repo, rs, now)
	s.mu.Unlock()
	if announce {
		s.notify([]Status{status})
	}
	return result
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
func (s *Scheduler) status(name string, r repository, rs *repoState, now time.Time) Status {
	st := Status{Repository: name, Enabled: r.enabled, DailyCap: r.settings.DailyCap}
	if s.Queue != nil {
		st.SpentToday, st.SpentKnown = s.Queue.AutomaticSpend(name, startOfDay(now))
	}
	if rs != nil {
		if !rs.PausedUntil.IsZero() {
			until := rs.PausedUntil
			st.PausedUntil = &until
		}
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
	now := s.now()
	o := Overview{Enabled: v.prefs.Automatic, CursorAgentAcknowledged: v.prefs.CursorAgentAcknowledged, PullRequests: cmp.Or(v.prefs.PullRequests, "both"), Repositories: []Status{}}
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
		o.Repositories = append(o.Repositories, s.status(name, r, rs, now))
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
// and again before it starts, so turning generation off (globally, for the
// repository, or withdrawing the cursor-agent acknowledgement) stops jobs
// that are queued but not started, and a job that would take the repository
// past its current daily cap on the scheduler clock's day does not start. It
// reads only the registry database and the clock, never the scheduler's state
// or the queue: the queue's mutex is held.
func (s *Scheduler) Gate(j jobs.Job) jobs.Admission {
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
	return jobs.Admission{DailyCap: settings.DailyCap, Day: startOfDay(s.now())}
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

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Package automatic is the daemon's scheduler for reviews nobody asked for:
// it turns new pull request heads and new review requests, seen after
// automatic generation was enabled, into jobs on the existing queue. It has
// no model backend and no GitHub client of its own; discovery feeds it
// snapshots and jobs.Queue.SubmitAutomatic prepares, refuses or queues each
// job with the same estimates and isolation as a confirmed one.
package automatic

import (
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
}

type Queue interface {
	SubmitAutomatic(context.Context, jobs.AutomaticRequest) (jobs.Job, error)
	AutomaticSpend(repository string, day time.Time) (float64, bool)
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
	Enabled                 bool     `json:"enabled"`
	CursorAgentAcknowledged bool     `json:"cursor_agent_acknowledged"`
	Repositories            []Status `json:"repositories"`
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
}

type prState struct {
	Head      string `json:"head"`
	Requested bool   `json:"requested"`
	TeamKnown bool   `json:"team_known"`
	// Pending is a head that needs an automatic job; Trigger says why.
	Pending string `json:"pending,omitempty"`
	Trigger string `json:"trigger,omitempty"`
	// Done is the last head queued or found already reviewed.
	Done     string    `json:"done,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
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
// Observe, the only way pull requests become automatic jobs. Hook events
// (an agent session ending, a push) call it.
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

// eligible: the viewer's review is requested or the viewer wrote it, and it
// is neither from a fork nor by an author without known write access.
func eligible(pr discovery.PullRequest) bool {
	return !pr.Fork && pr.AuthorHasWrite != nil && *pr.AuthorHasWrite && (pr.ReviewRequestedFromMe || pr.AuthoredByMe)
}

// Observe records a complete discovery snapshot. The first one after a
// repository is enabled is its baseline; afterwards a new head, or a new
// review request, on an eligible PR leaves a pending job for the next pass.
// Background discovery calls it after every successful poll.
func (s *Scheduler) Observe(ctx context.Context, snapshot discovery.Snapshot) {
	v, err := s.current(ctx)
	if err != nil {
		s.log().Warn("automatic generation skipped a snapshot", "error", err)
		return
	}
	name := strings.ToLower(snapshot.Repository)
	s.mu.Lock()
	defer s.mu.Unlock()
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
		rs = &repoState{Since: s.now().UTC(), PRs: map[int]*prState{}}
		for _, pr := range snapshot.PullRequests {
			rs.PRs[pr.Number] = &prState{Head: pr.HeadSHA, Requested: pr.ReviewRequestedFromMe, TeamKnown: pr.TeamMembershipKnown}
		}
		state.Repositories[name] = rs
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
		previous.Head, previous.Requested, previous.TeamKnown = pr.HeadSHA, pr.ReviewRequestedFromMe, pr.TeamMembershipKnown
		switch {
		case (newHead || newRequest) && eligible(pr) && pr.HeadSHA != previous.Done:
			previous.Pending, previous.Attempts, previous.RetryAt = pr.HeadSHA, 0, time.Time{}
			previous.Trigger = jobs.TriggerNewHead
			if !newHead {
				previous.Trigger = jobs.TriggerReviewRequested
			}
		case previous.Pending != "" && (previous.Pending != pr.HeadSHA || !eligible(pr)):
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
	now := s.now()
	var due []work
	var notices []Status
	s.mu.Lock()
	state := s.load()
	changed := s.prune(v)
	names := make([]string, 0, len(state.Repositories))
	for name := range state.Repositories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rs := state.Repositories[name]
		if !rs.PausedUntil.IsZero() && !now.Before(rs.PausedUntil) {
			rs.PausedUntil, rs.Notice, changed = time.Time{}, "", true
			notices = append(notices, s.status(name, v.repos[name], rs, now))
		}
		r := v.repos[name]
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
		job, err = s.Queue.SubmitAutomatic(ctx, jobs.AutomaticRequest{Item: jobs.Item{Clone: w.repo.clone.ID, PR: w.number}, Repository: w.repo.clone.Repository, Head: w.head, Trigger: w.trigger, AllowUnpriced: v.prefs.CursorAgentAcknowledged, DailyCap: w.repo.settings.DailyCap, Day: day})
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
		notice = NoticeDailyCap
		result = stopRepository
		s.log().Info("automatic generation paused by the daily cap", "repository", w.name, "until", rs.PausedUntil)
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
	o := Overview{Enabled: v.prefs.Automatic, CursorAgentAcknowledged: v.prefs.CursorAgentAcknowledged, Repositories: []Status{}}
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

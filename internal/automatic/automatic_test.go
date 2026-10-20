package automatic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
)

type fakeRegistry struct {
	mu       sync.Mutex
	prefs    repos.Preferences
	settings repos.Settings
	// others are the settings of any other registered repository, by
	// lower-case name.
	others map[string]repos.Settings
	// unreadable makes RepositorySettings fail, as a transient database error.
	unreadable bool
}

func (r *fakeRegistry) List(context.Context) ([]repos.Clone, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []repos.Clone{{ID: "clone", Path: "/clone", CommonDir: "/clone/.git", Repository: "org/repo", Settings: r.settings, Available: true}}, nil
}
func (r *fakeRegistry) Preferences(context.Context) (repos.Preferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prefs, nil
}
func (r *fakeRegistry) UpdatePreferences(_ context.Context, edit func(repos.Preferences) repos.Preferences) (repos.Preferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefs = edit(r.prefs)
	return r.prefs, nil
}
func (r *fakeRegistry) RepositorySettings(_ context.Context, repository string) (repos.Settings, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unreadable {
		return repos.Settings{}, false, errors.New("database is locked")
	}
	if s, ok := r.others[strings.ToLower(repository)]; ok {
		return s, true, nil
	}
	if !strings.EqualFold(repository, "org/repo") {
		return repos.Settings{}, false, nil
	}
	return r.settings, true, nil
}
func (r *fakeRegistry) set(f func(*repos.Preferences, *repos.Settings)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.prefs, &r.settings)
}

// executor is the GitHub side of jobs.Local: current heads per PR. Jobs
// finish at once unless block is set.
type executor struct {
	mu      sync.Mutex
	heads   map[int]string
	drafts  map[int]bool
	backend string
	block   chan struct{}
	started chan struct{}
	fail    error
}

func (e *executor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	price := 2.0
	backend := e.backend
	if backend == "" {
		backend = "claude"
	}
	return jobs.Prepared{ConfigDigest: "d", Item: item, Repository: "org/repo", HeadSHA: e.heads[item.PR], Backend: backend, Model: "claude-sonnet-4-5", EstimateUSD: &price}, "2026-10-01", nil
}
func (e *executor) Execute(ctx context.Context, p jobs.Prepared, _ func(jobs.Progress)) (*float64, error) {
	// Like jobs.Local's recheck: an automatic job of a PR now a draft is
	// refused before any model call.
	e.mu.Lock()
	draft := e.drafts[p.PR]
	e.mu.Unlock()
	if p.RefuseDraft && draft {
		return nil, &jobs.NotAutomaticError{Reason: jobs.ReasonDraft}
	}
	if e.started != nil {
		e.started <- struct{}{}
	}
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, e.fail
}
func (e *executor) setHead(pr int, head string, draft bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.heads[pr] = head
	if e.drafts == nil {
		e.drafts = map[int]bool{}
	}
	e.drafts[pr] = draft
}

type harness struct {
	t         *testing.T
	dir       string
	registry  *fakeRegistry
	exec      *executor
	queue     *jobs.Queue
	scheduler *Scheduler
	now       time.Time
	notices   []Status
	reviewed  map[int]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, registry: &fakeRegistry{prefs: repos.DefaultPreferences()}, exec: &executor{heads: map[int]string{}}, now: time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local), reviewed: map[int]string{}}
	h.openQueue()
	h.restart()
	return h
}

func (h *harness) openQueue() {
	q, err := jobs.Open(filepath.Join(h.dir, "jobs.json"), h.exec, 1, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(q.Close)
	q.SetClock(func() time.Time { return h.now })
	h.queue = q
}

// restart builds a new scheduler on the same state file, as a daemon
// restart does.
func (h *harness) restart() {
	h.scheduler = &Scheduler{Registry: h.registry, Queue: h.queue, Path: filepath.Join(h.dir, "automatic.json"), Now: func() time.Time { return h.now },
		Reviewed: func(_ context.Context, _ repos.Clone, n int) (string, error) { return h.reviewed[n], nil },
		Notify:   func(s Status) { h.notices = append(h.notices, s) },
	}
	h.queue.SetAutomaticGate(h.scheduler.Gate)
}

type pr struct {
	number    int
	head      string
	requested bool
	authored  bool
	fork      bool
	draft     bool
	// write is the author's permission: "" write, "no" or "unknown".
	write     string
	teamKnown *bool
}

func (h *harness) observe(prs ...pr) {
	h.t.Helper()
	snapshot := discovery.Snapshot{Repository: "org/repo", FetchedAt: h.now, PullRequests: []discovery.PullRequest{}}
	for _, p := range prs {
		var write *bool
		switch p.write {
		case "":
			write = new(true)
		case "no":
			write = new(false)
		}
		team := true
		if p.teamKnown != nil {
			team = *p.teamKnown
		}
		h.exec.setHead(p.number, p.head, p.draft)
		snapshot.PullRequests = append(snapshot.PullRequests, discovery.PullRequest{Repository: "org/repo", Number: p.number, HeadSHA: p.head, State: "OPEN", Fork: p.fork, Draft: p.draft, AuthoredByMe: p.authored, ReviewRequestedFromMe: p.requested, AuthorHasWrite: write, TeamMembershipKnown: team})
	}
	h.scheduler.Observe(context.Background(), snapshot)
}

// queued lists repository#PR@head:trigger of every automatic job so far.
func (h *harness) queued() []string {
	var out []string
	for _, j := range h.queue.List() {
		if j.Automatic {
			out = append(out, j.Repository+"#"+strconv.Itoa(j.PR)+"@"+j.HeadSHA+":"+j.Trigger)
		}
	}
	return out
}

func put(s *Scheduler, body string) int {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/automatic", strings.NewReader(body)))
	return w.Code
}

func (h *harness) pass() { h.scheduler.Pass(context.Background()) }

func (h *harness) want(want ...string) {
	h.t.Helper()
	got := h.queued()
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		h.t.Fatalf("automatic jobs %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			h.t.Fatalf("automatic jobs %v, want %v", got, want)
		}
	}
}

// waitIdle waits for the fake jobs to finish so a PR is not busy.
func (h *harness) waitIdle() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !h.queue.Idle() {
		if time.Now().After(deadline) {
			h.t.Fatalf("jobs still running: %+v", h.queue.List())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOnlyRequestedAndAuthoredPRsGetJobsForNewHeadsAfterEnabling(t *testing.T) {
	h := newHarness(t)
	open := []pr{
		{number: 1, head: "a1", requested: true},
		{number: 2, head: "b1", authored: true},
		{number: 3, head: "c1"},
		{number: 4, head: "d1", authored: true, fork: true},
		{number: 5, head: "e1", requested: true, write: "no"},
		{number: 6, head: "f1", requested: true, write: "unknown"},
	}
	// Pull requests open when automatic generation starts are not caught up.
	h.observe(open...)
	h.pass()
	h.want()
	// The same snapshot again changes nothing.
	h.observe(open...)
	h.pass()
	h.want()
	for i := range open {
		open[i].head = open[i].head[:1] + "2"
	}
	h.observe(open...)
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	h.waitIdle()
	// A new review request on an unchanged head, and a newly opened PR.
	open[2].requested = true
	open = append(open, pr{number: 7, head: "g1", authored: true})
	h.observe(open...)
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#3@c2:review_requested", "org/repo#7@g1:new_head")
	h.waitIdle()
	// Seen again: deduplicated by head.
	h.observe(open...)
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#3@c2:review_requested", "org/repo#7@g1:new_head")
}

// A team request that only became visible because team membership became
// readable is not a new request; a direct request is, even without it.
func TestTeamMembershipBecomingKnownIsNotANewRequest(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", teamKnown: new(false)}, pr{number: 2, head: "b1", teamKnown: new(false)})
	h.observe(pr{number: 1, head: "a1", requested: true, teamKnown: new(true)}, pr{number: 2, head: "b1", teamKnown: new(false), requested: true})
	h.pass()
	h.want("org/repo#2@b1:review_requested")
}

func TestRestartNeitherBurstsNorForgetsPendingWork(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", requested: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", requested: true}, pr{number: 2, head: "b1", authored: true})
	held := true
	h.scheduler.Hold = func() bool { return held }
	h.pass()
	h.want()
	// The daemon restarts: the pending head survives, nothing else is new.
	h.restart()
	h.observe(pr{number: 1, head: "a2", requested: true}, pr{number: 2, head: "b1", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.waitIdle()
	h.restart()
	h.observe(pr{number: 1, head: "a2", requested: true}, pr{number: 2, head: "b1", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

func TestAlreadyReviewedHeadIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", requested: true})
	h.reviewed[1] = "a2"
	h.observe(pr{number: 1, head: "a2", requested: true})
	h.pass()
	h.want()
}

func TestOneJobPerPRAndNewerHeadWaitsForTheRunningJob(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	close(h.exec.block)
	h.waitIdle()
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#1@a3:new_head")
}

func TestDailyCapPausesTheRepositoryUntilTomorrow(t *testing.T) {
	h := newHarness(t)
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(3.0) })
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	if len(h.notices) != 1 || h.notices[0].Notice != NoticeDailyCap || h.notices[0].PausedUntil == nil || !h.notices[0].PausedUntil.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("pause notice %+v", h.notices)
	}
	o, err := h.scheduler.Overview(context.Background())
	if err != nil || len(o.Repositories) != 1 || o.Repositories[0].Notice != NoticeDailyCap || o.Repositories[0].Pending != 1 || o.Repositories[0].SpentToday != 2 {
		t.Fatalf("overview %+v %v", o, err)
	}
	h.waitIdle()
	h.now = h.now.Add(13 * time.Hour)
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.now = time.Date(2026, 10, 20, 0, 1, 0, 0, time.Local)
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	last := h.notices[len(h.notices)-1]
	if last.PausedUntil != nil || last.Notice != "" {
		t.Fatalf("resume not announced %+v", h.notices)
	}
}

// The cap's day is the scheduler clock's calendar day in its own zone, also
// when that differs from the UTC day the job was stamped in.
func TestDailyCapDayFollowsTheClockZoneNearMidnight(t *testing.T) {
	for _, zone := range []*time.Location{time.FixedZone("west", -8*3600), time.FixedZone("east", 10*3600), time.UTC} {
		t.Run(zone.String(), func(t *testing.T) {
			h := newHarness(t)
			h.now = time.Date(2026, 10, 19, 23, 30, 0, 0, zone)
			h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(3.0) })
			h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
			h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
			h.pass()
			h.want("org/repo#1@a2:new_head")
			midnight := time.Date(2026, 10, 20, 0, 0, 0, 0, zone)
			if len(h.notices) != 1 || h.notices[0].PausedUntil == nil || !h.notices[0].PausedUntil.Equal(midnight) {
				t.Fatalf("pause notice %+v", h.notices)
			}
			h.waitIdle()
			h.now = midnight.Add(-time.Minute)
			h.pass()
			h.want("org/repo#1@a2:new_head")
			h.now = midnight.Add(time.Minute)
			h.pass()
			h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
		})
	}
}

func TestCursorAgentNeedsTheAcknowledgement(t *testing.T) {
	h := newHarness(t)
	h.exec.backend = agent.BackendCursorAgent
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want()
	if len(h.notices) != 1 || h.notices[0].Notice != jobs.ReasonCursorAgentConsent {
		t.Fatalf("notice %+v", h.notices)
	}
	// Acknowledging does not catch up the refused head; the next one runs.
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.CursorAgentAcknowledged = true })
	h.pass()
	h.want()
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.pass()
	h.want("org/repo#1@a3:new_head")
}

func TestTurningOffStopsNewJobsKeepsRunningOnesAndReenablingDoesNotCatchUp(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	<-h.exec.started
	// Off (the tray's pause): the queued, not yet started job is cancelled
	// with a reason; the running one keeps running.
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = false })
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b3", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	states := map[int]jobs.Job{}
	for _, j := range h.queue.List() {
		states[j.PR] = j
	}
	if states[1].State != "running" || states[2].State != "cancelled" || states[2].Reason != GateOff {
		t.Fatalf("after turning off: %+v", states)
	}
	close(h.exec.block)
	h.waitIdle()
	// Back on: what changed while off is the new baseline.
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = true })
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b4", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b5", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b5:new_head")
}

// A repository turned off, or cursor-agent no longer acknowledged, cancels
// that scope's queued automatic jobs; the worker re-checks before starting.
func TestRepositoryOffAndWithdrawnAcknowledgementCancelQueuedJobs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		off     func(*repos.Preferences, *repos.Settings)
		reason  string
	}{
		{"repository off", "", func(_ *repos.Preferences, s *repos.Settings) { s.Automatic = new(false) }, GateRepositoryOff},
		{"acknowledgement withdrawn", agent.BackendCursorAgent, func(p *repos.Preferences, _ *repos.Settings) { p.CursorAgentAcknowledged = false }, jobs.ReasonCursorAgentConsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.exec.backend = tc.backend
			h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.CursorAgentAcknowledged = true })
			h.exec.block = make(chan struct{})
			h.exec.started = make(chan struct{}, 10)
			h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
			h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
			h.pass()
			<-h.exec.started
			h.registry.set(tc.off)
			// The worker refuses the queued job when it would start it, even
			// before a pass runs.
			close(h.exec.block)
			h.waitIdle()
			for _, j := range h.queue.List() {
				if j.PR == 2 && (j.State != "cancelled" || j.Reason != tc.reason) {
					t.Fatalf("queued job %+v", j)
				}
				if j.PR == 1 && j.State != "completed" {
					t.Fatalf("running job %+v", j)
				}
			}
		})
	}
}

// A stale pass cannot queue after an off: SubmitAutomatic re-reads the
// settings under the queue's mutex.
func TestStalePassCannotQueueAfterOff(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.exec.setHead(1, "a2", false)
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = false })
	v := view{prefs: repos.Preferences{Automatic: true}, repos: map[string]repository{"org/repo": {clone: repos.Clone{ID: "clone", Repository: "org/repo", Available: true}, enabled: true}}}
	h.scheduler.submit(context.Background(), v, work{name: "org/repo", repo: v.repos["org/repo"], number: 1, head: "a2", trigger: jobs.TriggerNewHead}, h.now)
	h.want()
}

func TestRaisingOrClearingTheCapLiftsThePause(t *testing.T) {
	h := newHarness(t)
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(3.0) })
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true}, pr{number: 3, head: "c1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true}, pr{number: 3, head: "c2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	// Lowering keeps the pause.
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(1.0) })
	h.pass()
	h.want("org/repo#1@a2:new_head")
	// Raising lifts it; the next job that fits runs, the one after pauses again.
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(5.0) })
	h.waitIdle()
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	if last := h.notices[len(h.notices)-1]; last.Notice != NoticeDailyCap {
		t.Fatalf("not paused again %+v", last)
	}
	h.waitIdle()
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = nil })
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#3@c2:new_head")
}

// The baseline is a snapshot fetched after enabling, never an older cached one.
func TestBaselineIsNotTakenFromASnapshotOlderThanEnabling(t *testing.T) {
	h := newHarness(t)
	h.pass() // enabling is seen at 10:00
	old := h.now
	h.now = old.Add(-time.Hour)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.now = old.Add(time.Minute)
	// Pushed before enabling, fetched after: part of the baseline.
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want()
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.pass()
	h.want("org/repo#1@a3:new_head")
}

func TestPutCancelsQueuedJobsAndPublishes(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	<-h.exec.started
	h.notices = nil
	if code := put(h.scheduler, `{"enabled":false}`); code != 200 {
		t.Fatal(code)
	}
	for _, j := range h.queue.List() {
		if j.PR == 2 && j.State != "cancelled" {
			t.Fatalf("queued job survived the pause: %+v", j)
		}
	}
	if len(h.notices) != 1 || h.notices[0].Enabled {
		t.Fatalf("no event for other windows: %+v", h.notices)
	}
	close(h.exec.block)
}

// The global switch is a master switch (the desktop tray's pause): off stops
// every repository, even one set to on. With it on, a repository's off wins
// and its on or default generate.
func TestGlobalOffIsAMasterSwitch(t *testing.T) {
	h := newHarness(t)
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.Automatic = new(false) })
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want()
	h.registry.set(func(p *repos.Preferences, s *repos.Settings) { p.Automatic, s.Automatic = false, new(true) })
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.observe(pr{number: 1, head: "a4", authored: true})
	h.pass()
	h.want()
	o, err := h.scheduler.Overview(context.Background())
	if err != nil || o.Repositories[0].Enabled {
		t.Fatalf("repository set to on enabled under the global off switch: %+v %v", o, err)
	}
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = true })
	h.observe(pr{number: 1, head: "a5", authored: true})
	h.observe(pr{number: 1, head: "a6", authored: true})
	h.pass()
	h.want("org/repo#1@a6:new_head")
	h.waitIdle()
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.Automatic = nil })
	h.observe(pr{number: 1, head: "a7", authored: true})
	h.pass()
	h.want("org/repo#1@a6:new_head", "org/repo#1@a7:new_head")
}

func TestDrainRefusalIsRetried(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	if !h.queue.Drain() {
		t.Fatal("not drained")
	}
	h.pass()
	h.want()
	h.queue.Resume()
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

func TestTriggerFeedsTheSameScheduler(t *testing.T) {
	h := newHarness(t)
	heads := map[string]string{"1": "a1"}
	h.scheduler.Discover = func(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
		write := true
		h.exec.setHead(1, heads["1"], false)
		return discovery.Snapshot{Repository: repo.String(), FetchedAt: h.now, PullRequests: []discovery.PullRequest{{Repository: repo.String(), Number: 1, HeadSHA: heads["1"], AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}}, nil
	}
	if err := h.scheduler.Trigger(context.Background(), "org/repo"); err != nil {
		t.Fatal(err)
	}
	heads["1"] = "a2"
	if err := h.scheduler.Trigger(context.Background(), "org/repo"); err != nil {
		t.Fatal(err)
	}
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

func TestHandlerUpdatesPreferences(t *testing.T) {
	h := newHarness(t)
	o, err := h.scheduler.Overview(context.Background())
	if err != nil || !o.Enabled || o.CursorAgentAcknowledged || len(o.Repositories) != 1 || !o.Repositories[0].Enabled {
		t.Fatalf("%+v %v", o, err)
	}
	if code := put(h.scheduler, `{"enabled":false,"cursor_agent_acknowledged":true}`); code != 200 {
		t.Fatal(code)
	}
	if code := put(h.scheduler, `{"unknown":true}`); code != 400 {
		t.Fatal(code)
	}
	o, _ = h.scheduler.Overview(context.Background())
	if o.Enabled || !o.CursorAgentAcknowledged || o.Repositories[0].Enabled {
		t.Fatalf("%+v", o)
	}
}

// Drafts get no automatic review; marking one ready for review is a trigger
// when the PR otherwise qualifies and its head has no review yet.
func TestDraftsWaitUntilReadyForReview(t *testing.T) {
	h := newHarness(t)
	open := []pr{
		{number: 1, head: "a1", authored: true, draft: true},
		{number: 2, head: "b1", requested: true, draft: true, fork: true},
		{number: 3, head: "c1", requested: true, draft: true},
		{number: 4, head: "d1", draft: true},
	}
	h.observe(open...)
	for i := range open {
		open[i].head = open[i].head[:1] + "2"
	}
	h.observe(open...)
	h.pass()
	h.want()
	h.reviewed[3] = "c2"
	for i := range open {
		open[i].draft = false
	}
	h.observe(open...)
	h.pass()
	h.want("org/repo#1@a2:ready_for_review")
	h.waitIdle()
	// Back to draft and a new push: nothing until it is ready again.
	h.observe(pr{number: 1, head: "a3", authored: true, draft: true})
	h.pass()
	h.want("org/repo#1@a2:ready_for_review")
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.pass()
	h.want("org/repo#1@a2:ready_for_review", "org/repo#1@a3:ready_for_review")
}

// stop records a running job as interrupted, as a daemon stop does, and
// starts a new queue and scheduler on the same files.
func (h *harness) stop() {
	h.t.Helper()
	h.queue.Close()
	h.openQueue()
	h.restart()
}

func TestInterruptedAutomaticRunIsRetriedOnce(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	snapshot := []pr{{number: 1, head: "a2", authored: true}}
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(snapshot...)
	h.pass()
	<-h.exec.started
	h.stop()
	h.observe(snapshot...)
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#1@a2:retry")
	<-h.exec.started
	// Interrupted again: not retried a second time, even after a restart.
	h.stop()
	h.observe(snapshot...)
	h.pass()
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#1@a2:retry")
	for _, j := range h.queue.List() {
		if j.State != "interrupted" {
			t.Fatalf("unexpected job %+v", j)
		}
	}
	// The next push runs as usual.
	close(h.exec.block)
	h.observe(pr{number: 1, head: "a3", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#1@a2:retry", "org/repo#1@a3:new_head")
}

func TestInterruptedRetryRespectsTheMasterSwitch(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	<-h.exec.started
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = false })
	h.stop()
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

func TestFailedAndCancelledAutomaticRunsAreNotRetried(t *testing.T) {
	h := newHarness(t)
	h.exec.fail = errors.New("backend failed")
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.pass()
	h.waitIdle()
	h.exec.fail = nil
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	<-h.exec.started
	for _, j := range h.queue.List() {
		if j.PR == 2 {
			if _, err := h.queue.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.waitIdle()
	states := map[int]string{}
	for _, j := range h.queue.List() {
		states[j.PR] = j.State
	}
	if states[1] != "failed" || states[2] != "cancelled" {
		t.Fatalf("states %v", states)
	}
	h.stop()
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
}

// A re-enabled repository does not retry a run interrupted before it was
// turned off: the new baseline treats that head as seen.
func TestInterruptedRunBeforeAnOffIsNotRetriedAfterReenabling(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	<-h.exec.started
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = false })
	h.stop()
	h.pass()
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = true })
	h.now = h.now.Add(time.Minute)
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

// Two repositories are gated by their own settings.
func TestGateReadsEachRepositoryOwnSettings(t *testing.T) {
	h := newHarness(t)
	h.registry.mu.Lock()
	h.registry.others = map[string]repos.Settings{"org/other": {Automatic: new(false)}}
	h.registry.mu.Unlock()
	price := 2.0
	job := func(repository string) jobs.Job {
		return jobs.Job{Prepared: jobs.Prepared{Repository: repository, EstimateUSD: &price}}
	}
	if a := h.scheduler.Gate(job("org/repo")); a.Reason != "" {
		t.Fatalf("org/repo gated: %q", a.Reason)
	}
	if a := h.scheduler.Gate(job("Org/Other")); a.Reason != GateRepositoryOff {
		t.Fatalf("org/other gated: %q", a.Reason)
	}
	if a := h.scheduler.Gate(job("org/unknown")); a.Reason != GateRepositoryRemoved {
		t.Fatalf("org/unknown gated: %q", a.Reason)
	}
}

// A settings read that fails while a job waits does not use up its head: the
// job is recorded as interrupted and gets the one retry such a run gets.
func TestUnreadableSettingsDoNotConsumeTheHead(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	<-h.exec.started
	h.registry.mu.Lock()
	h.registry.unreadable = true
	h.registry.mu.Unlock()
	close(h.exec.block)
	h.waitIdle()
	states := map[int]jobs.Job{}
	for _, j := range h.queue.List() {
		states[j.PR] = j
	}
	if states[1].State != "completed" || states[2].State != "interrupted" || states[2].Reason != GateUnreadable {
		t.Fatalf("states %+v", states)
	}
	h.registry.mu.Lock()
	h.registry.unreadable = false
	h.registry.mu.Unlock()
	h.pass()
	h.waitIdle()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b2:retry")
}

// Background refresh and Trigger observe from different goroutines: a
// snapshot fetched before the newest one already observed changes nothing.
func TestOlderSnapshotArrivingLateIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", authored: true})
	fetched := h.now
	h.now = h.now.Add(2 * time.Minute)
	h.observe(pr{number: 1, head: "a2", authored: true})
	write := true
	h.scheduler.Observe(context.Background(), discovery.Snapshot{Repository: "org/repo", FetchedAt: fetched.Add(time.Minute), PullRequests: []discovery.PullRequest{{Repository: "org/repo", Number: 1, HeadSHA: "a1", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}})
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

// A queued job the daily cap stops before it starts (here the cap was lowered
// while it waited) pauses the repository with the daily cap notice and keeps
// its head pending; raising the cap runs it.
func TestDailyCapReachedWhileQueuedPausesAndKeepsTheHead(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(10.0) })
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	<-h.exec.started
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(0.0) })
	close(h.exec.block)
	h.waitIdle()
	select {
	case <-h.exec.started:
		t.Fatal("a job started over the lowered cap")
	default:
	}
	states := map[int]jobs.Job{}
	for _, j := range h.queue.List() {
		states[j.PR] = j
	}
	if states[1].State != "completed" || states[2].State != "cancelled" || states[2].Reason != jobs.ReasonDailyCap {
		t.Fatalf("states %+v", states)
	}
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	last := h.notices[len(h.notices)-1]
	if last.Notice != NoticeDailyCap || last.PausedUntil == nil || !last.PausedUntil.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)) || last.Pending != 1 {
		t.Fatalf("pause notice %+v", h.notices)
	}
	// Still paused: nothing is submitted, and the head is put back only once.
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	h.registry.set(func(_ *repos.Preferences, s *repos.Settings) { s.DailyCap = new(10.0) })
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b2:new_head")
	h.waitIdle()
	completed := 0
	for _, j := range h.queue.List() {
		if j.PR == 2 && j.State == "completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("head not run after the cap was raised: %+v", h.queue.List())
	}
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b2:new_head")
}

// A PR converted back to draft while its automatic job waited gets no model
// call; marked ready for review again at the same head, it runs.
func TestPRTurnedDraftWhileQueuedRunsWhenReadyAgain(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	<-h.exec.started
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true, draft: true})
	close(h.exec.block)
	h.waitIdle()
	select {
	case <-h.exec.started:
		t.Fatal("the draft's job started")
	default:
	}
	for _, j := range h.queue.List() {
		if j.PR == 2 && (j.State != "cancelled" || j.Reason != jobs.ReasonDraft) {
			t.Fatalf("draft job %+v", j)
		}
	}
	// Still a draft: nothing.
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head")
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b2:ready_for_review")
	h.waitIdle()
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#2@b2:ready_for_review")
}

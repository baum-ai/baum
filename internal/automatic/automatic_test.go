package automatic

import (
	"context"
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
	backend string
	block   chan struct{}
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
func (e *executor) Execute(ctx context.Context, _ jobs.Prepared, _ func(jobs.Progress)) (*float64, error) {
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}
func (e *executor) setHead(pr int, head string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.heads[pr] = head
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
	h.queue = q
}

// restart builds a new scheduler on the same state file, as a daemon
// restart does.
func (h *harness) restart() {
	h.scheduler = &Scheduler{Registry: h.registry, Queue: h.queue, Path: filepath.Join(h.dir, "automatic.json"), Now: func() time.Time { return h.now },
		Reviewed: func(_ context.Context, _ repos.Clone, n int) (string, error) { return h.reviewed[n], nil },
		Notify:   func(s Status) { h.notices = append(h.notices, s) },
	}
}

type pr struct {
	number    int
	head      string
	requested bool
	authored  bool
	fork      bool
	// write is the author's permission: "" write, "no" or "unknown".
	write     string
	teamKnown *bool
}

func (h *harness) observe(prs ...pr) {
	h.t.Helper()
	snapshot := discovery.Snapshot{Repository: "org/repo", PullRequests: []discovery.PullRequest{}}
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
		h.exec.setHead(p.number, p.head)
		snapshot.PullRequests = append(snapshot.PullRequests, discovery.PullRequest{Repository: "org/repo", Number: p.number, HeadSHA: p.head, State: "OPEN", Fork: p.fork, AuthoredByMe: p.authored, ReviewRequestedFromMe: p.requested, AuthorHasWrite: write, TeamMembershipKnown: team})
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
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = false })
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	for _, j := range h.queue.List() {
		if j.State != "running" && j.State != "queued" {
			t.Fatalf("turning off touched a job: %+v", j)
		}
	}
	close(h.exec.block)
	h.waitIdle()
	// Back on: what changed while off is the new baseline.
	h.registry.set(func(p *repos.Preferences, _ *repos.Settings) { p.Automatic = true })
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b3", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b4", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head", "org/repo#2@b4:new_head")
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
		h.exec.setHead(1, heads["1"])
		return discovery.Snapshot{Repository: repo.String(), PullRequests: []discovery.PullRequest{{Repository: repo.String(), Number: 1, HeadSHA: heads["1"], AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}}, nil
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

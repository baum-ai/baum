package automatic

import (
	"context"
	"encoding/json"
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
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
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
	// noClone leaves the repository picked without a registered clone.
	noClone bool
}

func (r *fakeRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := repos.Repository{Repository: "org/repo", Settings: r.settings, Clones: []repos.Clone{}}
	if !r.noClone {
		v.Clones = append(v.Clones, repos.Clone{ID: "clone", Path: "/clone", CommonDir: "/clone/.git", Repository: "org/repo", Settings: r.settings, Available: true})
	}
	out := []repos.Repository{v}
	for name, s := range r.others {
		out = append(out, repos.Repository{Repository: name, Settings: s, Clones: []repos.Clone{{ID: "clone-" + name, Repository: name, Settings: s, Available: true}}})
	}
	return out, nil
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
	// needsClone is a clone ID whose items Prepare refuses with ErrNeedsClone,
	// though the registry reports the clone available.
	needsClone string
	// groups are the review groups a group item names.
	groups *reviewgroups.Store
}

func (e *executor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	backend := e.backend
	if backend == "" {
		backend = "claude"
	}
	if item.Clone == e.needsClone {
		return jobs.Prepared{}, "", jobs.ErrNeedsClone
	}
	repository := "org/repo"
	if item.Clone == "clone-org/other" {
		repository = "org/other"
	}
	p := jobs.Prepared{ConfigDigest: "d", Item: item, Repository: repository, HeadSHA: e.heads[item.PR], Backend: backend, Model: "claude-sonnet-4-5"}
	if item.Group != "" {
		g, err := e.groups.Get(context.Background(), item.Group)
		if err != nil {
			return jobs.Prepared{}, "", err
		}
		heads := []string{}
		for _, n := range g.Members {
			heads = append(heads, strconv.Itoa(n)+"@"+e.heads[n])
		}
		p.HeadSHA, p.Members = e.heads[g.Members[0]], strings.Join(heads, ",")
	}
	return p, "2026-10-01", nil
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
		if j.Automatic && j.Group != "" {
			out = append(out, j.Repository+"#group@"+j.Members+":"+j.Trigger)
		} else if j.Automatic {
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

// Narrowing the pull requests to authored or requested ones leaves the others
// without automatic jobs, including a pending one; all reviews every PR, still
// not drafts, forks or authors without write access.
func TestPullRequestsPreferenceNarrowsWhatGetsJobs(t *testing.T) {
	for _, tc := range []struct {
		choice string
		want   []string
	}{
		{repos.PullRequestsAuthored, []string{"org/repo#2@b2:new_head"}},
		{repos.PullRequestsRequested, []string{"org/repo#1@a2:new_head"}},
		{repos.PullRequestsAll, []string{"org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#3@c2:new_head"}},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			h := newHarness(t)
			_, _ = h.registry.UpdatePreferences(context.Background(), func(p repos.Preferences) repos.Preferences {
				p.PullRequests = tc.choice
				return p
			})
			open := []pr{{number: 1, head: "a1", requested: true}, {number: 2, head: "b1", authored: true}, {number: 3, head: "c1"}, {number: 4, head: "d1", fork: true}, {number: 5, head: "e1", draft: true}, {number: 6, head: "f1", write: "no"}}
			h.observe(open...)
			for i := range open {
				open[i].head = open[i].head[:1] + "2"
			}
			h.observe(open...)
			h.pass()
			h.want(tc.want...)
		})
	}
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

// cursor-agent, which baum cannot price, runs like any backend.
func TestCursorAgentRunsWithoutAnAcknowledgement(t *testing.T) {
	h := newHarness(t)
	h.exec.backend = agent.BackendCursorAgent
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	if len(h.notices) != 0 {
		t.Fatalf("notice %+v", h.notices)
	}
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

// A repository turned off cancels its queued automatic jobs; the worker
// re-checks before starting.
func TestRepositoryOffCancelsQueuedJobs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		off     func(*repos.Preferences, *repos.Settings)
		reason  string
	}{
		{"repository off", "", func(_ *repos.Preferences, s *repos.Settings) { s.Automatic = new(false) }, GateRepositoryOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.exec.backend = tc.backend
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
	if err != nil || !o.Enabled || len(o.Repositories) != 1 || !o.Repositories[0].Enabled {
		t.Fatalf("%+v %v", o, err)
	}
	if code := put(h.scheduler, `{"enabled":false}`); code != 200 {
		t.Fatal(code)
	}
	// The cursor-agent acknowledgement is gone, so it is unknown like any
	// other field.
	for _, body := range []string{`{"unknown":true}`, `{"cursor_agent_acknowledged":true}`} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	o, _ = h.scheduler.Overview(context.Background())
	if o.Enabled || o.Repositories[0].Enabled {
		t.Fatalf("%+v", o)
	}
}

// The settings page chooses the backend, model and pull requests; a bad
// value changes nothing, and cursor-agent is chosen like any other harness.
func TestHandlerSavesReviewChoices(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{`{"backend":"nope"}`, `{"pull_requests":"everyone"}`} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if code := put(h.scheduler, `{"backend":"cursor-agent","model":"composer-2.5","pull_requests":"requested"}`); code != 200 {
		t.Fatal(code)
	}
	prefs, _ := h.registry.Preferences(context.Background())
	if prefs.Backend == nil || *prefs.Backend != "cursor-agent" || prefs.Model == nil || *prefs.Model != "composer-2.5" || prefs.PullRequests != repos.PullRequestsRequested {
		t.Fatalf("%+v", prefs)
	}
	if code := put(h.scheduler, `{"pull_requests":"all"}`); code != 200 {
		t.Fatal(code)
	}
	if o, _ := h.scheduler.Overview(context.Background()); o.PullRequests != "all" {
		t.Fatalf("%+v", o)
	}
	if code := put(h.scheduler, `{"pull_requests":"both"}`); code != 200 {
		t.Fatal(code)
	}
	if o, _ := h.scheduler.Overview(context.Background()); o.PullRequests != "both" {
		t.Fatalf("%+v", o)
	}
	// The reviews' and fixes' reasoning efforts; empty is the agent's own.
	for _, body := range []string{`{"effort":"max"}`, `{"fix_effort":"high"}`} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if code := put(h.scheduler, `{"effort":" High ","fix_backend":"codex","fix_effort":"low"}`); code != 200 {
		t.Fatal(code)
	}
	prefs, _ = h.registry.Preferences(context.Background())
	if prefs.Effort == nil || *prefs.Effort != "high" || prefs.FixEffort == nil || *prefs.FixEffort != "low" {
		t.Fatalf("%+v", prefs)
	}
	if code := put(h.scheduler, `{"effort":"","fix_backend":""}`); code != 200 {
		t.Fatal(code)
	}
	prefs, _ = h.registry.Preferences(context.Background())
	if prefs.Effort != nil || prefs.FixEffort != nil || prefs.FixBackend != nil {
		t.Fatalf("after clearing %+v", prefs)
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
	job := func(repository string) jobs.Job {
		return jobs.Job{Prepared: jobs.Prepared{Repository: repository}}
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

// A repository an older baum paused for its daily cap, with the cap and its
// notice saved, is not paused now: the saved pause and notice are ignored and
// its pending head runs.
func TestAnOldDailyCapPauseIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", authored: true})
	h.observe(pr{number: 1, head: "a2", authored: true})
	raw, err := appdb.Read(h.scheduler.Path, "state", "automatic")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]map[string]map[string]any
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["repositories"]["org/repo"]["paused_until"] = "2999-01-01T00:00:00Z"
	state["repositories"]["org/repo"]["paused_cap"] = 1.0
	state["repositories"]["org/repo"]["notice"] = "daily_cap"
	if raw, err = json.Marshal(state); err != nil {
		t.Fatal(err)
	}
	if err = appdb.Write(h.scheduler.Path, "state", "automatic", raw); err != nil {
		t.Fatal(err)
	}
	h.restart()
	o, err := h.scheduler.Overview(context.Background())
	if err != nil || len(o.Repositories) != 1 || o.Repositories[0].Notice != "" {
		t.Fatalf("overview %+v %v", o, err)
	}
	h.pass()
	h.want("org/repo#1@a2:new_head")
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

func TestIdentityFencePreservesSettingsAndRequiresNewBaseline(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "old", requested: true})
	h.now = h.now.Add(time.Minute)
	finish := h.scheduler.BeginIdentityChange()
	pass := make(chan struct{})
	go func() { h.scheduler.Pass(context.Background()); close(pass) }()
	select {
	case <-pass:
		t.Fatal("pass crossed identity fence")
	case <-time.After(20 * time.Millisecond):
	}
	finish(true)
	select {
	case <-pass:
	case <-time.After(time.Second):
		t.Fatal("fence not released")
	}
	h.observe(pr{number: 1, head: "new-account-existing", requested: true})
	h.scheduler.Pass(context.Background())
	if jobs := h.queued(); len(jobs) != 0 {
		t.Fatal("historical PR automatically queued", jobs)
	}
	h.now = h.now.Add(time.Minute)
	h.observe(pr{number: 1, head: "subsequent-change", requested: true})
	h.scheduler.Pass(context.Background())
	if jobs := h.queued(); len(jobs) != 1 {
		t.Fatal("future changes did not resume", jobs)
	}
}

func TestPassWaitingAtIdentityFenceStopsWithItsContext(t *testing.T) {
	h := newHarness(t)
	finish := h.scheduler.BeginIdentityChange()
	defer finish(false)
	ctx, cancel := context.WithCancel(context.Background())
	pass := make(chan struct{})
	go func() { h.scheduler.Pass(ctx); close(pass) }()
	cancel()
	select {
	case <-pass:
	case <-time.After(time.Second):
		t.Fatal("pass kept waiting for a sign-in after its context ended")
	}
}

func TestIdentityTransitionFencesQueuedAdmission(t *testing.T) {
	h := newHarness(t)
	j := jobs.Job{Prepared: jobs.Prepared{Repository: "org/repo"}, CreatedAt: h.now}
	if admission := h.scheduler.Gate(j); admission.Reason != "" {
		t.Fatal(admission)
	}
	finish := h.scheduler.BeginIdentityChange()
	if admission := h.scheduler.Gate(j); admission.Reason == "" {
		t.Fatal("queued job admitted during transition")
	}
	h.now = h.now.Add(time.Minute)
	finish(true)
	if admission := h.scheduler.Gate(j); admission.Reason == "" {
		t.Fatal("previous-account queued job admitted after transition")
	}
}

// A picked repository without a clone is listed and observed, but nothing is
// submitted for it; once a clone appears the next pass submits as usual.
func TestRepositoryWithoutACloneIsObservedNotSubmitted(t *testing.T) {
	h := newHarness(t)
	h.registry.mu.Lock()
	h.registry.noClone = true
	h.registry.mu.Unlock()
	h.observe(pr{number: 1, head: "a1", requested: true})
	h.pass()
	h.observe(pr{number: 1, head: "a2", requested: true})
	h.pass()
	h.want()
	o, err := h.scheduler.Overview(context.Background())
	if err != nil || len(o.Repositories) != 1 || o.Repositories[0].Repository != "org/repo" || !o.Repositories[0].Enabled || o.Repositories[0].Pending != 1 {
		t.Fatalf("overview %+v %v", o, err)
	}
	// Still admitted by the gate: the repository is registered.
	if a := h.scheduler.Gate(jobs.Job{Prepared: jobs.Prepared{Repository: "org/repo"}, Automatic: true}); a.Reason != "" {
		t.Fatalf("gate refused a picked repository: %q", a.Reason)
	}
	h.registry.mu.Lock()
	h.registry.noClone = false
	h.registry.mu.Unlock()
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

// A head whose queued job ended because the clone was removed did not use up
// its head or spend: once a clone is back, the next pass submits it once.
func TestHeadWaitsForACloneAfterAJobLostIt(t *testing.T) {
	h := newHarness(t)
	h.observe(pr{number: 1, head: "a1", requested: true})
	h.pass()
	h.observe(pr{number: 1, head: "a2", requested: true})
	h.exec.fail = jobs.ErrNeedsClone
	h.pass()
	h.waitIdle()
	states := func() (cancelled, completed int) {
		for _, j := range h.queue.List() {
			switch {
			case j.State == "cancelled" && j.Reason == jobs.ReasonNeedsClone:
				cancelled++
			case j.State == "completed":
				completed++
			}
		}
		return
	}
	if c, _ := states(); c != 1 {
		t.Fatalf("jobs %+v", h.queue.List())
	}
	h.registry.mu.Lock()
	h.registry.noClone = true
	h.registry.mu.Unlock()
	h.pass()
	h.pass()
	if len(h.queue.List()) != 1 {
		t.Fatalf("submitted without a clone: %+v", h.queue.List())
	}
	h.registry.mu.Lock()
	h.registry.noClone = false
	h.registry.mu.Unlock()
	h.exec.fail = nil
	h.pass()
	h.waitIdle()
	h.pass()
	if c, d := states(); c != 1 || d != 1 || len(h.queue.List()) != 2 {
		t.Fatalf("want one cancelled and one completed job: %+v", h.queue.List())
	}
}

// ErrNeedsClone from submission (the clone vanished after the pass read it)
// keeps the head pending without using an attempt or a retry time, stays
// quiet, and does not hold back other repositories.
func TestNeedsCloneAtSubmissionKeepsHeadPending(t *testing.T) {
	h := newHarness(t)
	h.registry.others = map[string]repos.Settings{"org/other": {}}
	observeOther := func(head string) {
		h.exec.setHead(5, head, false)
		h.scheduler.Observe(context.Background(), discovery.Snapshot{Repository: "org/other", FetchedAt: h.now, PullRequests: []discovery.PullRequest{{Repository: "org/other", Number: 5, HeadSHA: head, State: "OPEN", ReviewRequestedFromMe: true, AuthorHasWrite: new(true), TeamMembershipKnown: true}}})
	}
	h.observe(pr{number: 1, head: "a1", requested: true})
	observeOther("o1")
	h.pass()
	h.observe(pr{number: 1, head: "a2", requested: true})
	observeOther("o2")
	h.exec.needsClone = "clone"
	h.notices = nil
	h.pass()
	h.pass()
	h.want("org/other#5@o2:new_head")
	rs := h.scheduler.load().Repositories["org/repo"]
	if p := rs.PRs[1]; p.Pending != "a2" || p.Attempts != 0 || !p.RetryAt.IsZero() {
		t.Fatalf("pr state %+v", p)
	}
	for _, n := range h.notices {
		if n.Repository == "org/repo" {
			t.Fatalf("announced %+v", n)
		}
	}
	h.exec.needsClone = ""
	h.pass()
	h.want("org/other#5@o2:new_head", "org/repo#1@a2:new_head")
}

func TestPutSetsAndClearsTheFixAgent(t *testing.T) {
	h := newHarness(t)
	if code := put(h.scheduler, `{"fix_backend":"codex","fix_model":" gpt-fix "}`); code != 200 {
		t.Fatal(code)
	}
	p, _ := h.registry.Preferences(context.Background())
	if p.FixBackend == nil || *p.FixBackend != "codex" || p.FixModel == nil || *p.FixModel != "gpt-fix" {
		t.Fatalf("set: %+v", p)
	}
	for _, body := range []string{`{"fix_backend":"cursor-agent"}`, `{"fix_backend":"nope"}`} {
		if code := put(h.scheduler, body); code != 400 {
			t.Fatalf("%s = %d", body, code)
		}
	}
	if p, _ = h.registry.Preferences(context.Background()); p.FixBackend == nil || *p.FixBackend != "codex" {
		t.Fatalf("a rejected change was saved: %+v", p)
	}
	if code := put(h.scheduler, `{"fix_backend":"","fix_model":""}`); code != 200 {
		t.Fatal(code)
	}
	if p, _ = h.registry.Preferences(context.Background()); p.FixBackend != nil || p.FixModel != nil {
		t.Fatalf("cleared: %+v", p)
	}
}

package hookrefresh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/prlink"
	"github.com/baum-ai/baum/internal/repos"
)

const repoName = "acme/app"

var (
	clonePath = filepath.Join(os.TempDir(), "baum-hookrefresh-app")
	commonDir = filepath.Join(clonePath, ".git")
	oldHead   = strings.Repeat("a", 40)
	newHead   = strings.Repeat("b", 40)
	zeroOID   = strings.Repeat("0", 40)
)

type registry struct {
	mu       sync.Mutex
	clones   []repos.Clone
	prefs    repos.Preferences
	settings repos.Settings
}

func (r *registry) List(context.Context) ([]repos.Clone, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]repos.Clone, len(r.clones))
	for i, c := range r.clones {
		c.Settings = r.settings
		out[i] = c
	}
	return out, nil
}

func (r *registry) edit(f func(*registry)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

func (r *registry) Preferences(context.Context) (repos.Preferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prefs, nil
}

func (r *registry) UpdatePreferences(_ context.Context, edit func(repos.Preferences) repos.Preferences) (repos.Preferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefs = edit(r.prefs)
	return r.prefs, nil
}

func (r *registry) RepositorySettings(_ context.Context, repository string) (repos.Settings, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !strings.EqualFold(repository, repoName) || len(r.clones) == 0 {
		return repos.Settings{}, false, nil
	}
	return r.settings, true, nil
}

// listing counts the service's listings of the registry, apart from the
// scheduler's.
type listing struct {
	*registry
	lists int
}

func (l *listing) List(ctx context.Context) ([]repos.Clone, error) {
	l.lists++
	return l.registry.List(ctx)
}

// executor is the GitHub side of the job queue: it prepares jobs at the
// head GitHub shows and finishes them at once.
type executor struct{ gh *fakeGitHub }

func (e *executor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	price := 2.0
	pr := e.gh.pr(item.PR)
	return jobs.Prepared{ConfigDigest: "d", Item: item, Repository: repoName, HeadSHA: pr.HeadSHA, Draft: pr.Draft, Backend: "claude", Model: "claude-sonnet-4-5", EstimateUSD: &price}, "2026-10-01", nil
}

func (e *executor) Execute(context.Context, jobs.Prepared, func(jobs.Progress)) (*float64, error) {
	return nil, nil
}

// fakeGitHub holds the pull requests as GitHub shows them and counts what
// is asked of it.
type fakeGitHub struct {
	mu        sync.Mutex
	prs       map[int]discovery.PullRequest
	refreshes map[int]int
	polls     int
}

func (g *fakeGitHub) pr(number int) discovery.PullRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prs[number]
}

func (g *fakeGitHub) set(number int, edit func(*discovery.PullRequest)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pr := g.prs[number]
	edit(&pr)
	g.prs[number] = pr
}

func (g *fakeGitHub) snapshot(at time.Time) discovery.Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := discovery.Snapshot{Repository: repoName, FetchedAt: at, PullRequests: []discovery.PullRequest{}}
	for n := 1; n <= 20; n++ {
		if pr, ok := g.prs[n]; ok {
			s.PullRequests = append(s.PullRequests, pr)
		}
	}
	return s
}

func (g *fakeGitHub) refreshCount(number int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refreshes[number]
}

func (g *fakeGitHub) pollCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.polls
}

type harness struct {
	t        *testing.T
	now      time.Time
	registry *registry
	listing  *listing
	gh       *fakeGitHub
	// cache is the discovery snapshot on disk: what the last poll saw.
	cache     discovery.Snapshot
	worktrees map[string]Worktree
	gitCalls  int
	// refreshed lists each refresh as clone path and PR numbers.
	refreshed []string
	queue     *jobs.Queue
	scheduler *automatic.Scheduler
	service   *Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := newUnobservedHarness(t)
	// Automatic generation records its baseline from the first poll after
	// enabling; the discovery cache is that poll.
	if err := h.scheduler.Trigger(context.Background(), repoName); err != nil {
		t.Fatal(err)
	}
	h.gh.polls = 0
	return h
}

// newUnobservedHarness has automatic generation enabled but no poll since:
// the scheduler awaits its baseline. The discovery cache predates enabling.
func newUnobservedHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := true
	h := &harness{
		t:   t,
		now: time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local),
		registry: &registry{prefs: repos.DefaultPreferences(), clones: []repos.Clone{
			{ID: "clone", Path: clonePath, CommonDir: commonDir, Repository: repoName, Available: true},
		}},
		gh: &fakeGitHub{refreshes: map[int]int{}, prs: map[int]discovery.PullRequest{
			7: {Repository: repoName, Number: 7, HeadSHA: oldHead, HeadRef: "feature", State: "OPEN", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true},
			8: {Repository: repoName, Number: 8, HeadSHA: strings.Repeat("c", 40), HeadRef: "other", State: "OPEN", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true},
		}},
		worktrees: map[string]Worktree{
			clonePath: {CommonDir: commonDir, Work: prlink.Work{Branch: "feature"}, Pushed: repoName},
		},
	}
	queue, err := jobs.Open(filepath.Join(dir, "jobs.json"), &executor{gh: h.gh}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	queue.SetClock(func() time.Time { return h.now })
	h.queue = queue
	h.scheduler = &automatic.Scheduler{Registry: h.registry, Queue: queue, Path: filepath.Join(dir, "automatic.json"), Now: func() time.Time { return h.now },
		Reviewed: func(context.Context, repos.Clone, int) (string, error) { return "", nil },
		Discover: func(context.Context, github.Repository) (discovery.Snapshot, error) {
			h.gh.mu.Lock()
			h.gh.polls++
			h.gh.mu.Unlock()
			h.cache = h.gh.snapshot(h.now)
			return h.cache, nil
		},
	}
	queue.SetAutomaticGate(h.scheduler.Gate)
	h.listing = &listing{registry: h.registry}
	h.service = &Service{
		Registry: h.listing,
		Now:      func() time.Time { return h.now },
		Cached: func(_ context.Context, repo github.Repository) (discovery.Snapshot, error) {
			if repo.String() != repoName || h.cache.Repository == "" {
				return discovery.Snapshot{}, errors.New("no cache")
			}
			return h.cache, nil
		},
		Refresh: func(_ context.Context, clone repos.Clone, numbers []int) ([]discovery.Observation, error) {
			if clone.Repository != repoName {
				t.Fatalf("refreshed %s", clone.Repository)
			}
			h.refreshed = append(h.refreshed, clone.Path+":"+joinInts(numbers))
			var out []discovery.Observation
			for _, n := range numbers {
				h.gh.mu.Lock()
				h.gh.refreshes[n]++
				pr := h.gh.prs[n]
				h.gh.mu.Unlock()
				out = append(out, discovery.Observation{Number: n, HeadSHA: pr.HeadSHA, State: "OPEN", Draft: pr.Draft})
			}
			return out, nil
		},
		Trigger: h.scheduler.Trigger,
		Git: func(_ context.Context, dir, repository, remote string) (Worktree, error) {
			if repository != repoName {
				t.Fatalf("inspected %s as %s", dir, repository)
			}
			h.gitCalls++
			w, ok := h.worktrees[dir]
			if !ok {
				return Worktree{}, errors.New("not a working tree")
			}
			if remote != "origin" {
				w.Pushed = ""
			}
			return w, nil
		},
	}
	h.cache = h.gh.snapshot(h.now.Add(-time.Minute))
	h.now = h.now.Add(time.Second)
	h.scheduler.Pass(context.Background())
	return h
}

func joinInts(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.service.Step(context.Background())
	h.scheduler.Pass(context.Background())
}

// run steps through d one second at a time.
func (h *harness) run(d time.Duration) {
	for end := h.now.Add(d); h.now.Before(end); {
		h.advance(time.Second)
	}
}

func (h *harness) sessionEnd(cwd string) {
	e := hookevents.New(hookevents.KindAgent, "claude-code", "session-end", cwd, "test")
	e.Time = h.now
	e.Session = &hookevents.Session{ID: "s", Cwd: cwd}
	h.service.Receive(hookevents.Record{Received: h.now, Via: hookevents.ViaSocket, Event: e})
}

func (h *harness) prePush(remote string, lines ...string) {
	e := hookevents.New(hookevents.KindGit, "", "pre-push", clonePath, "test")
	e.Time = h.now
	e.Git = &hookevents.Git{Args: []string{remote}, Lines: lines}
	h.service.Receive(hookevents.Record{Received: h.now, Via: hookevents.ViaSocket, Event: e})
}

func pushLine(branch, local, remote string) string {
	return "refs/heads/" + branch + " " + local + " refs/heads/" + branch + " " + remote
}

func (h *harness) jobs() []string {
	var out []string
	for _, j := range h.queue.List() {
		if j.Automatic {
			out = append(out, j.Repository+"#"+strconv.Itoa(j.PR)+"@"+j.HeadSHA[:1]+":"+j.Trigger)
		}
	}
	return out
}

func (h *harness) wantJobs(want ...string) {
	h.t.Helper()
	got := h.jobs()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Fatalf("automatic jobs %v, want %v", got, want)
	}
}

func (h *harness) wantRefreshes(pr, want int) {
	h.t.Helper()
	if got := h.gh.refreshCount(pr); got != want {
		h.t.Fatalf("PR %d refreshed %d times, want %d", pr, got, want)
	}
}

func TestSessionEndRefreshesTheBranchPullRequest(t *testing.T) {
	h := newHarness(t)
	h.worktrees[filepath.Join(clonePath, "sub")] = h.worktrees[clonePath]
	h.sessionEnd(filepath.Join(clonePath, "sub"))
	h.advance(time.Second)
	h.wantRefreshes(7, 0)
	h.advance(Debounce)
	h.wantRefreshes(7, 1)
	h.wantRefreshes(8, 0)
	h.run(5 * time.Minute)
	h.wantRefreshes(7, 1)
	if h.gh.pollCount() != 0 {
		t.Fatalf("an unchanged head polled discovery %d times", h.gh.pollCount())
	}
	h.wantJobs()
}

// A session end links its pull request as live sessions do: by the branch's
// name, then the head branch its upstream names, then the commit checked
// out.
func TestSessionEndFindsThePullRequestByUpstreamOrCommit(t *testing.T) {
	for _, tc := range []struct {
		name string
		work prlink.Work
		want int
	}{
		{"upstream name", prlink.Work{Branch: "local-name", Upstream: "feature"}, 1},
		{"detached at the head", prlink.Work{Head: oldHead}, 1},
		{"detached elsewhere", prlink.Work{Head: newHead}, 0},
		{"unknown branch", prlink.Work{Branch: "local-name", Head: newHead}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.worktrees[clonePath] = Worktree{CommonDir: commonDir, Work: tc.work}
			h.sessionEnd(clonePath)
			h.run(time.Minute)
			h.wantRefreshes(7, tc.want)
			h.wantRefreshes(8, 0)
		})
	}
}

func TestPrePushWaitsAndRechecksOnceWhileTheHeadIsNotVisible(t *testing.T) {
	h := newHarness(t)
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.advance(PushDelay - time.Second)
	h.wantRefreshes(7, 0)
	h.advance(time.Second)
	h.wantRefreshes(7, 1)
	h.advance(Recheck - time.Second)
	h.wantRefreshes(7, 1)
	h.advance(time.Second)
	h.wantRefreshes(7, 2)
	h.run(5 * time.Minute)
	h.wantRefreshes(7, 2)
	h.wantRefreshes(8, 0)
	h.wantJobs()
	if h.gh.pollCount() != 0 {
		t.Fatal("a head that never showed triggered discovery")
	}
}

func TestPushedHeadSeenOnRecheckQueuesOneAutomaticJob(t *testing.T) {
	h := newHarness(t)
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.advance(PushDelay)
	h.wantRefreshes(7, 1)
	h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = newHead })
	h.advance(Recheck)
	h.wantRefreshes(7, 2)
	h.wantJobs("acme/app#7@b:new_head")
	if h.gh.pollCount() != 1 {
		t.Fatalf("discovery polled %d times", h.gh.pollCount())
	}
}

func TestMovedHeadQueuesExactlyOneJobThroughTheScheduler(t *testing.T) {
	h := newHarness(t)
	h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = newHead })
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.advance(PushDelay)
	h.wantRefreshes(7, 1)
	h.wantJobs("acme/app#7@b:new_head")
	// The same push reported again, and a session ending on the branch,
	// queue nothing more: the scheduler has handled that head.
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.sessionEnd(clonePath)
	h.run(5 * time.Minute)
	h.wantJobs("acme/app#7@b:new_head")
	if h.gh.pollCount() != 1 {
		t.Fatalf("discovery polled %d times", h.gh.pollCount())
	}
}

func TestNoJobWhenAutomaticGenerationDoesNotApply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*harness)
		polls   int
		refresh int
	}{
		{"automatic off", func(h *harness) { h.registry.edit(func(r *registry) { r.prefs.Automatic = false }) }, 0, 1},
		{"repository off", func(h *harness) { h.registry.edit(func(r *registry) { r.settings.Automatic = new(false) }) }, 0, 1},
		{"fork", func(h *harness) {
			h.gh.set(7, func(pr *discovery.PullRequest) { pr.Fork = true })
			h.cache = h.gh.snapshot(h.now)
		}, 0, 1},
		{"draft", func(h *harness) { h.gh.set(7, func(pr *discovery.PullRequest) { pr.Draft = true }) }, 0, 1},
		{"not mine", func(h *harness) {
			h.gh.set(7, func(pr *discovery.PullRequest) { pr.AuthoredByMe = false })
			h.cache = h.gh.snapshot(h.now)
		}, 0, 1},
		{"cap reached", func(h *harness) { h.registry.edit(func(r *registry) { r.settings.DailyCap = new(1.0) }) }, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = newHead })
			h.prePush("origin", pushLine("feature", newHead, oldHead))
			h.run(5 * time.Minute)
			h.wantRefreshes(7, tc.refresh)
			h.wantJobs()
			if h.gh.pollCount() != tc.polls {
				t.Fatalf("discovery polled %d times, want %d", h.gh.pollCount(), tc.polls)
			}
		})
	}
}

func TestBurstOfEventsCausesBoundedRefreshes(t *testing.T) {
	h := newHarness(t)
	h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = newHead })
	for i := range 50 {
		if i%2 == 0 {
			h.prePush("origin", pushLine("feature", newHead, oldHead), pushLine("other", strings.Repeat("d", 40), strings.Repeat("c", 40)))
		} else {
			h.sessionEnd(clonePath)
		}
	}
	h.run(5 * time.Minute)
	h.wantRefreshes(7, 1)
	h.wantRefreshes(8, 2)
	if h.gitCalls != 2 || h.listing.lists != 1 {
		t.Fatalf("git ran %d times, registry listed %d times", h.gitCalls, h.listing.lists)
	}
	if h.gh.pollCount() != 1 {
		t.Fatalf("discovery polled %d times", h.gh.pollCount())
	}
	h.wantJobs("acme/app#7@b:new_head")

	// Events trickling in for ten minutes refresh a PR at most once per
	// Window (plus a recheck of a head that does not show).
	h = newHarness(t)
	for range 300 {
		h.sessionEnd(clonePath)
		h.advance(2 * time.Second)
	}
	h.run(time.Minute)
	// The first refresh follows the debounce, then one per Window while
	// events keep coming.
	if got, want := h.gh.refreshCount(7), int(10*time.Minute/Window)+1; got != want {
		t.Fatalf("PR 7 refreshed %d times in ten minutes, want %d", got, want)
	}
}

// Each registered clone keeps its own saved reviews, so a PR is refreshed in
// the store of every clone a hook names, not only the first.
func TestEveryCloneOfARepositoryIsRefreshedInItsOwnStore(t *testing.T) {
	h := newHarness(t)
	second := repos.Clone{ID: "second", Path: clonePath + "2", CommonDir: filepath.Join(clonePath+"2", ".git"), Repository: repoName, Available: true}
	h.registry.edit(func(r *registry) { r.clones = append(r.clones, second) })
	h.worktrees[second.Path] = Worktree{CommonDir: second.CommonDir, Work: prlink.Work{Branch: "feature"}, Pushed: repoName}
	h.sessionEnd(clonePath)
	h.sessionEnd(second.Path)
	h.run(time.Minute)
	// Store paths sort differently with Unix and Windows separators.
	want := []string{clonePath + ":7", second.Path + ":7"}
	slices.Sort(want)
	slices.Sort(h.refreshed)
	if !slices.Equal(h.refreshed, want) {
		t.Fatalf("refreshes %q, want %q", h.refreshed, want)
	}
}

// A hook's poll while the scheduler awaits its baseline becomes the baseline:
// pull requests already open then are not caught up.
func TestTriggerBeforeTheBaselineCatchesNothingUp(t *testing.T) {
	h := newUnobservedHarness(t)
	h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = newHead })
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.run(time.Minute)
	if h.gh.pollCount() != 1 {
		t.Fatalf("discovery polled %d times", h.gh.pollCount())
	}
	h.wantJobs()
	// The next push is new.
	next := strings.Repeat("e", 40)
	h.gh.set(7, func(pr *discovery.PullRequest) { pr.HeadSHA = next })
	h.prePush("origin", pushLine("feature", next, newHead))
	h.run(time.Minute)
	h.wantJobs("acme/app#7@e:new_head")
}

func TestOnlyRegisteredClonesAndTheirOwnRepositoryCount(t *testing.T) {
	h := newHarness(t)
	h.worktrees[filepath.Join(clonePath, "vendor", "lib")] = Worktree{CommonDir: filepath.Join(clonePath, "vendor", "lib", ".git"), Work: prlink.Work{Branch: "feature"}, Pushed: repoName}
	// Not registered: no git runs.
	h.sessionEnd("/elsewhere/app")
	// A nested repository inside the clone is not the clone.
	h.sessionEnd(filepath.Join(clonePath, "vendor", "lib"))
	// A push to another remote (a fork) changes nothing here.
	h.prePush("fork", pushLine("feature", newHead, oldHead))
	// A push to a URL that names no remote: Git passes the URL as the name.
	h.prePush("https://github.com/acme/app.git", pushLine("feature", newHead, oldHead))
	h.prePush("git@github.com:acme/app.git", pushLine("feature", newHead, oldHead))
	h.prePush("../app-mirror", pushLine("feature", newHead, oldHead))
	h.prePush("", pushLine("feature", newHead, oldHead))
	// Deleting the branch moves no head.
	h.prePush("origin", "(delete) "+zeroOID+" refs/heads/feature "+oldHead)
	h.run(5 * time.Minute)
	h.wantRefreshes(7, 0)
	if h.gitCalls != 3 {
		t.Fatalf("git ran %d times", h.gitCalls)
	}
}

func TestRemoteName(t *testing.T) {
	for name, want := range map[string]bool{
		"origin": true, "upstream/team": true, "": false,
		"https://github.com/acme/app.git": false, "git@github.com:acme/app.git": false,
		"github.com:acme/app": false, "/srv/git/app.git": false, "../app": false, `C:\repos\app`: false, "-x": false,
	} {
		if got := remoteName(name); got != want {
			t.Errorf("remoteName(%q) = %t", name, got)
		}
	}
}

func TestNothingRegisteredDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.registry.edit(func(r *registry) { r.clones = nil })
	h.sessionEnd(clonePath)
	h.prePush("origin", pushLine("feature", newHead, oldHead))
	h.run(time.Minute)
	if h.gitCalls != 0 || h.gh.refreshCount(7) != 0 || h.listing.lists != 1 {
		t.Fatalf("git %d refreshes %d lists %d", h.gitCalls, h.gh.refreshCount(7), h.listing.lists)
	}
}

func TestOldSpooledEventsAreLeftToThePeriodicRefresh(t *testing.T) {
	h := newHarness(t)
	e := hookevents.New(hookevents.KindAgent, "claude-code", "session-end", clonePath, "test")
	e.Time = h.now.Add(-MaxAge - time.Second)
	h.service.Receive(hookevents.Record{Received: h.now, Via: hookevents.ViaSpool, Event: e})
	h.run(time.Minute)
	h.wantRefreshes(7, 0)
}

func TestPushedRefs(t *testing.T) {
	got := pushedRefs([]string{
		pushLine("feature", newHead, oldHead),
		pushLine("new-branch", newHead, zeroOID),
		"refs/heads/gone " + zeroOID + " refs/heads/gone " + oldHead,
		"refs/tags/v1 " + newHead + " refs/tags/v1 " + zeroOID,
		"refs/heads/x " + newHead + " refs/heads/x",
		"refs/heads/x NOTANOID refs/heads/x " + oldHead,
	})
	want := []push{{branch: "feature", old: oldHead, new: newHead}, {branch: "new-branch", new: newHead}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pushed refs %+v", got)
	}
}

// The record hands events to the worker without waiting: while a refresh
// runs, events past the buffer are dropped, never blocking the hook's
// request. Cancelling the daemon's context stops the worker mid-refresh.
func TestEventHandlerNeverBlocksAndShutdownIsClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := hookevents.OpenStore(filepath.Join(dir, "events"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var clock sync.Mutex
	now := time.Now()
	started := make(chan struct{})
	var once sync.Once
	write := true
	s := &Service{
		Registry: &registry{prefs: repos.DefaultPreferences(), clones: []repos.Clone{{Path: clonePath, CommonDir: commonDir, Repository: repoName, Available: true}}},
		// Every reading of the clock is a minute later, so whatever is
		// scheduled is due at the next step.
		Now: func() time.Time {
			clock.Lock()
			defer clock.Unlock()
			now = now.Add(time.Minute)
			return now
		},
		Git: func(context.Context, string, string, string) (Worktree, error) {
			return Worktree{CommonDir: commonDir, Work: prlink.Work{Branch: "feature"}}, nil
		},
		Cached: func(context.Context, github.Repository) (discovery.Snapshot, error) {
			return discovery.Snapshot{Repository: repoName, PullRequests: []discovery.PullRequest{{Number: 7, HeadSHA: oldHead, HeadRef: "feature", AuthoredByMe: true, AuthorHasWrite: &write}}}, nil
		},
		Refresh: func(ctx context.Context, _ repos.Clone, _ []int) ([]discovery.Observation, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
		Trigger: func(context.Context, string) error { t.Error("triggered"); return nil },
	}
	events, cancel := store.Subscribe(Buffer, Match)
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx, events) }()
	// Events from the clock's future are never too old.
	future := time.Now().Add(24 * 365 * time.Hour)
	send := func() {
		e := hookevents.New(hookevents.KindAgent, "claude-code", "session-end", clonePath, "test")
		e.Time = future
		if _, err := store.Add(e, hookevents.ViaSocket); err != nil {
			t.Fatal(err)
		}
	}
	send()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("refresh did not start")
	}
	begin := time.Now()
	for range 10 * Buffer {
		send()
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("recording events waited for the worker: %s", elapsed)
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop with its context")
	}
}

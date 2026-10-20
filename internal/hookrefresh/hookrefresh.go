// Package hookrefresh refreshes pull requests as soon as a hook reports they
// may have changed: a coding-agent session ending in a registered clone, or a
// push from one. It runs the same model-free refresh as background
// synchronization (background.Service.RefreshNow) instead of waiting for its
// next pass. When a refresh shows that a pull request's head moved and
// automatic generation could apply, it asks the automatic scheduler to look at
// the repository again (automatic.Scheduler.Trigger), which alone decides
// whether a job is queued. It never enqueues or invokes generation itself.
package hookrefresh

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/repos"
)

// Timing. A burst of events (several pushes, sessions ending together)
// becomes at most one refresh of each pull request it names per Window, plus
// one recheck of a pushed head, and at most one discovery poll of each
// repository per Window.
const (
	// Debounce is how long events are collected before any is acted on.
	Debounce = 3 * time.Second
	// PushDelay is how long after a pre-push hook ran its pull requests are
	// refreshed: the hook runs before the push reaches GitHub.
	PushDelay = 10 * time.Second
	// Recheck is how long after a refresh that did not show the pushed head
	// yet the pull request is refreshed once more.
	Recheck = 30 * time.Second
	// MaxChecks bounds the refreshes that wait for one pushed head.
	MaxChecks = 2
	// Window is the least time between two refreshes of one pull request,
	// and between two discovery polls of one repository.
	Window = 30 * time.Second
	// MaxAge is the oldest event acted on: an event spooled while no daemon
	// ran is left to the periodic refresh.
	MaxAge = 10 * time.Minute
	// Buffer is how many events wait for the worker; the record drops (and
	// logs) what arrives while it is full.
	Buffer = 64
	// maxBatch bounds the events collected during one Debounce.
	maxBatch = 256
	// maxDelay bounds how far later events postpone one pull request's
	// refresh.
	maxDelay = 2 * Window
)

// Agent and Git hook events that can change a pull request.
const (
	sessionEnd = "session-end"
	prePush    = "pre-push"
)

// Match accepts the events the service acts on: an agent session that
// ended, and a push Git went on with (a pre-push hook that exited 0).
// post-rewrite is not one: a rewrite changes nothing on GitHub until it is
// pushed.
func Match(e hookevents.Event) bool {
	switch e.Kind {
	case hookevents.KindAgent:
		return e.Name == sessionEnd
	case hookevents.KindGit:
		return e.Name == prePush && e.Exit == 0 && e.Git != nil
	}
	return false
}

type Registry interface {
	List(context.Context) ([]repos.Clone, error)
	Preferences(context.Context) (repos.Preferences, error)
	RepositorySettings(ctx context.Context, repository string) (repos.Settings, bool, error)
}

// Worktree is what Git reports about the working tree a hook ran in.
type Worktree struct {
	// CommonDir is the shared git directory, the same for linked worktrees.
	CommonDir string
	// Branch is the current branch, "" when HEAD is detached.
	Branch string
	// Pushed is the repository the asked-for remote fetches from, as the
	// registry names repositories; "" when there is no such remote or it is
	// not on GitHub.
	Pushed string
}

// Service turns hook events into refreshes. Run owns it; it is not safe for
// concurrent use.
type Service struct {
	Registry Registry
	// Cached reads a repository's discovery snapshot from disk, without
	// network access.
	Cached func(context.Context, github.Repository) (discovery.Snapshot, error)
	// Refresh refreshes some pull requests of one clone and returns what it
	// observed: background.Service.RefreshNow.
	Refresh func(context.Context, repos.Clone, []int) ([]discovery.Observation, error)
	// Trigger has the automatic scheduler poll one repository and observe
	// the result: automatic.Scheduler.Trigger.
	Trigger func(context.Context, string) error
	// Git inspects the working tree containing dir and, when remote is set,
	// that remote. Nil runs git (LocalGit).
	Git func(ctx context.Context, dir, remote string) (Worktree, error)
	// Changed tells the app that discovery changed after a trigger.
	Changed func()
	Now     func() time.Time
	Log     *slog.Logger

	batch     []hookevents.Record
	batchDue  time.Time
	clones    []repos.Clone
	listed    time.Time
	tasks     map[key]*task
	refreshed map[key]time.Time
	triggers  map[string]time.Time
	triggered map[string]time.Time
}

type key struct {
	repository string
	number     int
}

// task is a pull request waiting for a refresh.
type task struct {
	clone repos.Clone
	pr    discovery.PullRequest
	due   time.Time
	// limit is as late as further events may postpone due.
	limit time.Time
	// known is the head before the events, from the discovery cache.
	known string
	// want is the head the latest push sent, "" when no push named one;
	// sent is when that push ran.
	want   string
	sent   time.Time
	checks int
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Run acts on events until ctx ends or events closes. Events come from a
// hookevents.Store subscription (Match, Buffer): the record never waits for
// this worker, and drops what does not fit while it refreshes.
func (s *Service) Run(ctx context.Context, events <-chan hookevents.Record) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-events:
			if !ok {
				return
			}
			s.Receive(record)
		case <-timer.C:
		}
		s.Step(ctx)
		timer.Stop()
		if next, ok := s.Next(); ok {
			timer.Reset(max(next.Sub(s.now()), 0))
		}
	}
}

// Receive collects one event for the next Step after Debounce.
func (s *Service) Receive(record hookevents.Record) {
	e := record.Event
	if !Match(e) || s.now().Sub(e.Time) > MaxAge {
		return
	}
	if len(s.batch) >= maxBatch {
		s.log().Warn("hook refresh dropped an event of a burst", "kind", e.Kind, "name", e.Name)
		return
	}
	if len(s.batch) == 0 {
		s.batchDue = s.now().Add(Debounce)
	}
	s.batch = append(s.batch, record)
}

// Next is when Step has work next.
func (s *Service) Next() (time.Time, bool) {
	var next time.Time
	ok := false
	consider := func(t time.Time) {
		if !ok || t.Before(next) {
			next, ok = t, true
		}
	}
	if len(s.batch) > 0 {
		consider(s.batchDue)
	}
	for _, t := range s.tasks {
		consider(t.due)
	}
	for _, due := range s.triggers {
		consider(due)
	}
	return next, ok
}

// Step does what is due: it maps the collected events to pull requests,
// refreshes the pull requests whose time has come, and triggers the
// scheduler for repositories where a head moved. Exposed for tests.
func (s *Service) Step(ctx context.Context) {
	if len(s.batch) > 0 && !s.now().Before(s.batchDue) {
		batch := s.batch
		s.batch = nil
		s.plan(ctx, batch)
	}
	s.refreshDue(ctx)
	s.triggerDue(ctx)
	s.forget()
}

// push is one branch a hook names: a pushed ref, or the branch an agent
// session ended on.
type push struct {
	branch string
	// old is the head GitHub had before a push, new the pushed one.
	old, new string
}

// plan maps events to registered clones and to the pull requests of the
// branches they name, as the discovery cache knows them.
func (s *Service) plan(ctx context.Context, batch []hookevents.Record) {
	clones, err := s.registered(ctx)
	if err != nil {
		s.log().Warn("hook refresh could not read the registered clones", "error", err)
		return
	}
	if len(clones) == 0 {
		return
	}
	type inspection struct {
		worktree Worktree
		err      error
	}
	inspected := map[[2]string]inspection{}
	snapshots := map[string]*discovery.Snapshot{}
	now := s.now()
	for _, record := range batch {
		e := record.Event
		clone, dir, ok := cloneOf(clones, e)
		if !ok {
			continue
		}
		remote := ""
		if e.Kind == hookevents.KindGit {
			// A push to a URL that names no remote keeps no remote name;
			// where it went is unknown.
			if len(e.Git.Args) == 0 || e.Git.Args[0] == "" {
				continue
			}
			remote = e.Git.Args[0]
		}
		at := [2]string{dir, remote}
		result, seen := inspected[at]
		if !seen {
			result.worktree, result.err = s.git()(ctx, dir, remote)
			inspected[at] = result
		}
		// A repository nested in the clone's directory is not the clone.
		if result.err != nil || result.worktree.CommonDir != clone.CommonDir {
			continue
		}
		var pushes []push
		due := now
		switch e.Kind {
		case hookevents.KindAgent:
			if result.worktree.Branch == "" {
				continue
			}
			pushes = []push{{branch: result.worktree.Branch}}
		case hookevents.KindGit:
			// A push to another repository (a fork) changes none of this
			// one's pull requests.
			if result.worktree.Pushed != strings.ToLower(clone.Repository) {
				continue
			}
			pushes = pushedRefs(e.Git.Lines)
			// A hook's clock ahead of the daemon's must not hold the
			// refresh back.
			due = minTime(maxTime(now, e.Time.Add(PushDelay)), now.Add(PushDelay))
		}
		name := strings.ToLower(clone.Repository)
		snapshot, seen := snapshots[name]
		if !seen {
			snapshot = s.cached(ctx, name, now)
			snapshots[name] = snapshot
		}
		if snapshot == nil {
			continue
		}
		for _, pr := range snapshot.PullRequests {
			for _, p := range pushes {
				// A fork's branch of the same name is another branch.
				if (!pr.Fork && pr.HeadRef != "" && pr.HeadRef == p.branch) || (p.old != "" && pr.HeadSHA == p.old) {
					s.add(clone, pr, p.new, e.Time, due)
				}
			}
		}
	}
}

// cached is the repository's discovery snapshot, nil when there is none or
// its rate budget is exhausted: the periodic refresh resumes after the reset.
func (s *Service) cached(ctx context.Context, name string, now time.Time) *discovery.Snapshot {
	repo, err := github.ParseRepository(name)
	if err != nil {
		return nil
	}
	snapshot, err := s.Cached(ctx, repo)
	if err != nil {
		return nil
	}
	rate := snapshot.Rate
	if rate.RetryAt.After(now) || (rate.Remaining != nil && *rate.Remaining == 0 && rate.Reset.After(now)) {
		s.log().Info("hook refresh skipped while the GitHub rate limit is exhausted", "repository", name)
		return nil
	}
	return &snapshot
}

// registered lists the registered clones, at most once per Window: listing
// checks every clone with git.
func (s *Service) registered(ctx context.Context) ([]repos.Clone, error) {
	now := s.now()
	if s.clones != nil && now.Before(s.listed.Add(Window)) {
		return s.clones, nil
	}
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	if clones == nil {
		clones = []repos.Clone{}
	}
	s.clones, s.listed = clones, now
	return clones, nil
}

func (s *Service) git() func(context.Context, string, string) (Worktree, error) {
	if s.Git != nil {
		return s.Git
	}
	return LocalGit
}

// cloneOf is the available registered clone a hook ran in, by its working
// directory (an agent's own, else the hook's) resolved to the longest clone
// path containing it, and that directory. Other directories are ignored
// without running git.
func cloneOf(clones []repos.Clone, e hookevents.Event) (repos.Clone, string, bool) {
	dirs := []string{e.Dir}
	if e.Kind == hookevents.KindAgent && e.Session != nil && e.Session.Cwd != "" {
		dirs = []string{e.Session.Cwd, e.Dir}
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		var best repos.Clone
		for _, c := range clones {
			if c.Available && within(c.Path, dir) && len(c.Path) > len(best.Path) {
				best = c
			}
		}
		if best.Path != "" {
			return best, dir, true
		}
	}
	return repos.Clone{}, "", false
}

func within(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// pushedRefs reads a pre-push hook's lines, "<local ref> <local oid> <remote
// ref> <remote oid>", keeping branch updates and creations; a deletion moves
// no pull request head.
func pushedRefs(lines []string) []push {
	var pushes []push
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 || !validOID(fields[1]) || !validOID(fields[3]) || zero(fields[1]) {
			continue
		}
		branch, ok := strings.CutPrefix(fields[2], "refs/heads/")
		if !ok || branch == "" {
			continue
		}
		p := push{branch: branch, new: fields[1]}
		if !zero(fields[3]) {
			p.old = fields[3]
		}
		pushes = append(pushes, p)
	}
	return pushes
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func zero(oid string) bool { return strings.Trim(oid, "0") == "" }

// add schedules a refresh of pr at due, but never sooner than Window after
// its last one; later events postpone it, by at most maxDelay.
func (s *Service) add(clone repos.Clone, pr discovery.PullRequest, want string, sent, due time.Time) {
	if s.tasks == nil {
		s.tasks = map[key]*task{}
	}
	k := key{strings.ToLower(clone.Repository), pr.Number}
	if last, ok := s.refreshed[k]; ok {
		due = maxTime(due, last.Add(Window))
	}
	t := s.tasks[k]
	if t == nil {
		t = &task{clone: clone, pr: pr, due: due, limit: due.Add(maxDelay), known: pr.HeadSHA}
		s.tasks[k] = t
	} else {
		t.due = minTime(maxTime(t.due, due), t.limit)
	}
	if want != "" && !sent.Before(t.sent) {
		if want != t.want {
			t.checks = 0
		}
		t.want, t.sent = want, sent
	}
}

// refreshDue refreshes the pull requests that are due, one repository at a
// time. A pushed head GitHub does not show yet is checked once more after
// Recheck; a refresh that failed is left to the periodic refresh.
func (s *Service) refreshDue(ctx context.Context) {
	now := s.now()
	due := map[string][]int{}
	for k, t := range s.tasks {
		if !now.Before(t.due) {
			due[k.repository] = append(due[k.repository], k.number)
		}
	}
	names := make([]string, 0, len(due))
	for name := range due {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		numbers := due[name]
		sort.Ints(numbers)
		tasks := map[int]*task{}
		for _, number := range numbers {
			k := key{name, number}
			tasks[number] = s.tasks[k]
			delete(s.tasks, k)
		}
		clone := tasks[numbers[0]].clone
		observations, err := s.Refresh(ctx, clone, numbers)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.log().Warn("hook refresh incomplete", "repository", name, "error", err)
		}
		if s.refreshed == nil {
			s.refreshed = map[key]time.Time{}
		}
		for _, number := range numbers {
			s.refreshed[key{name, number}] = now
		}
		moved := false
		for _, o := range observations {
			t := tasks[o.Number]
			if t == nil {
				continue
			}
			if t.want != "" && o.HeadSHA != t.want && t.checks+1 < MaxChecks {
				t.checks++
				t.due = now.Add(Recheck)
				t.limit = t.due
				s.tasks[key{name, o.Number}] = t
			}
			if o.HeadSHA != t.known && candidate(t.pr, o) {
				moved = true
				t.known = o.HeadSHA
			}
		}
		if moved {
			s.queueTrigger(name, now)
		}
	}
}

// candidate is a pull request whose new head automatic generation could
// review: what the scheduler requires (eligible), as far as the cached
// snapshot and the refresh show. The scheduler decides again from a fresh
// poll; this only spares a poll that could queue nothing.
func candidate(pr discovery.PullRequest, o discovery.Observation) bool {
	open := o.State == "" || o.State == "OPEN"
	return open && !o.Draft && !pr.Fork && pr.AuthorHasWrite != nil && *pr.AuthorHasWrite && (pr.AuthoredByMe || pr.ReviewRequestedFromMe)
}

// queueTrigger has the scheduler poll the repository now, or Window after
// its last poll for a hook.
func (s *Service) queueTrigger(name string, now time.Time) {
	if s.triggers == nil {
		s.triggers = map[string]time.Time{}
	}
	if _, queued := s.triggers[name]; queued {
		return
	}
	due := now
	if last, ok := s.triggered[name]; ok {
		due = maxTime(due, last.Add(Window))
	}
	s.triggers[name] = due
}

// triggerDue triggers the scheduler for each repository due, while automatic
// generation is on for it. The scheduler's own gate, dedupe, daily cap and
// baseline still decide every job.
func (s *Service) triggerDue(ctx context.Context) {
	now := s.now()
	names := make([]string, 0, len(s.triggers))
	for name, due := range s.triggers {
		if !now.Before(due) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		delete(s.triggers, name)
		if !s.automatic(ctx, name) {
			continue
		}
		if s.triggered == nil {
			s.triggered = map[string]time.Time{}
		}
		s.triggered[name] = now
		if err := s.Trigger(ctx, name); err != nil {
			if ctx.Err() == nil {
				s.log().Warn("automatic generation not triggered by a hook", "repository", name, "error", err)
			}
			continue
		}
		if s.Changed != nil {
			s.Changed()
		}
	}
}

// automatic reports whether automatic generation is on for the repository,
// globally and for it.
func (s *Service) automatic(ctx context.Context, name string) bool {
	prefs, err := s.Registry.Preferences(ctx)
	if err != nil {
		return false
	}
	settings, registered, err := s.Registry.RepositorySettings(ctx, name)
	return err == nil && registered && settings.AutomaticEnabled(prefs)
}

// forget drops refresh and trigger times older than Window.
func (s *Service) forget() {
	cutoff := s.now().Add(-Window)
	for k, at := range s.refreshed {
		if at.Before(cutoff) {
			delete(s.refreshed, k)
		}
	}
	for name, at := range s.triggered {
		if at.Before(cutoff) {
			delete(s.triggered, name)
		}
	}
}

// LocalGit inspects the working tree containing dir with git: its common
// directory, its current branch and, when remote is set, the repository that
// remote fetches from. It reads configuration only and contacts no remote.
func LocalGit(ctx context.Context, dir, remote string) (Worktree, error) {
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{})
	if err != nil {
		return Worktree{}, err
	}
	defer func() { _ = repo.Close() }()
	common, err := filepath.EvalSymlinks(repo.CommonDir())
	if err != nil {
		return Worktree{}, err
	}
	branch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return Worktree{}, err
	}
	w := Worktree{CommonDir: common, Branch: branch}
	if remote != "" {
		url, err := repo.RemoteFetchURL(ctx, remote)
		if err != nil {
			return Worktree{}, err
		}
		if gh, err := github.RepositoryFromRemote(url); url != "" && err == nil {
			w.Pushed = strings.ToLower(gh.String())
		}
	}
	return w, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

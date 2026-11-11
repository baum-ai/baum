package livesessions

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/sessions/vendors"
)

// A session's working directory is not always where its work goes: an agent
// commits in another worktree of the clone, or on a branch it creates and
// then leaves. Git's post-commit and pre-push hooks carry their ancestors, and
// the session whose agent is among them made the commit or the push, so a
// session links to the pull request its latest commit or push reaches before
// the one its directory has checked out.

// maxWork bounds the commits and pushes kept of a session.
const maxWork = 16

// Link methods of a pull request found through a session's own Git work
// rather than its working directory.
const (
	MethodPush   = "push"
	MethodCommit = "commit"
)

// workItem is a commit or a push a session's agent made.
type workItem struct {
	// id is the hook event's; with branch and head it names the item.
	id string
	at time.Time
	// dir is where Git ran the hook; empty for an item restored from
	// StateFile, which keeps no paths.
	dir    string
	pushed bool
	// remote is a push's; branch and head are the branch and commit pushed,
	// or the commit made and the branch it was made on, as the hook read them.
	remote, branch, head string
	// link is what the item was found to be once read, with the clone's
	// remotes; its Repository is empty when it links to no registered clone.
	link    Link
	read    bool
	durable bool
	queued  bool
}

func (w workItem) same(o workItem) bool {
	return w.id != "" && w.id == o.id && w.branch == o.branch && w.head == o.head
}

// WorkLinker links the commits and pushes of a session's agent; *Resolver is
// one. With a Linker that is not one, they link nothing.
type WorkLinker interface {
	// LinkCommit links the commit head made on branch ("" when detached) in
	// the working tree dir, from what the hook recorded rather than what dir
	// has checked out now, and reports false when dir is in no registered
	// clone.
	LinkCommit(ctx context.Context, dir, branch, head string) (Link, bool)
	// LinkPush links a push from the working tree dir through remote, of
	// head to branch, and reports false when dir is in no registered clone.
	LinkPush(ctx context.Context, dir, remote, branch, head string) (Link, bool)
}

// matchWork reports the Git hook events that say an agent committed or
// pushed: a post-commit, a post-rewrite, or a pre-push that let the push go.
// They say which agent by the session the hook's environment names, or by the
// hook's ancestors.
func matchWork(e hookevents.Event) bool {
	if e.Kind != hookevents.KindGit || !filepath.IsAbs(e.Dir) {
		return false
	}
	switch e.Name {
	case "post-commit", "post-rewrite":
	case "pre-push":
		if e.Exit != 0 {
			return false
		}
	default:
		return false
	}
	_, _, named := namedSession(e)
	return named || (e.Terminal != nil && len(e.Terminal.Processes) > 0)
}

// namedSession is the session a Git hook's environment named: an agent whose
// sessions the daemon stores observations for, its session id, and where the
// agent's own process is among the hook's ancestors (the index in
// Terminal.Processes). A hook that does not show the agent among its ancestors
// is left to them: what it names may only be inherited.
func namedSession(e hookevents.Event) (key, int, bool) {
	if e.Kind != hookevents.KindGit || e.Session == nil || e.Session.ID == "" || len(e.Session.ID) > maxID ||
		e.Agent == "" || !vendors.Slug(commitattribution.Vendor(e.Agent)).Valid() ||
		e.Session.AgentPID <= 0 || e.Terminal == nil {
		return key{}, 0, false
	}
	i := slices.IndexFunc(e.Terminal.Processes, func(p hookevents.Process) bool { return p.PID == e.Session.AgentPID })
	if i < 0 {
		return key{}, 0, false
	}
	return key{e.Agent, e.Session.ID}, i, true
}

func matchTracked(e hookevents.Event) bool { return matchAgent(e) || matchWork(e) }

type processKey struct {
	pid   int
	start string
}

// ownerLocked is the open session whose agent ran the Git hook of e: the
// nearest of the hook's ancestors that is the process a session's own hooks
// run under (their parent), matched by id and start time. A process two open
// sessions name is neither's. It also returns the index of that process among
// the ancestors.
func (t *Tracker) ownerLocked(e hookevents.Event) (key, *entry, int) {
	agents := map[processKey]key{}
	shared := map[processKey]bool{}
	for k, s := range t.sessions {
		if s.ended || s.terminal == nil || len(s.terminal.Processes) == 0 {
			continue
		}
		p := s.terminal.Processes[0]
		if p.PID <= 1 || p.Start == "" {
			continue
		}
		pk := processKey{p.PID, p.Start}
		if _, seen := agents[pk]; seen {
			shared[pk] = true
		}
		agents[pk] = k
	}
	for i, p := range e.Terminal.Processes {
		pk := processKey{p.PID, p.Start}
		if k, ok := agents[pk]; ok {
			if shared[pk] {
				return key{}, nil, 0
			}
			return k, t.sessions[k], i
		}
	}
	return key{}, nil, 0
}

// applyWorkLocked records the commit or push of e for the session whose agent
// made it, and asks for its lookup.
func (t *Tracker) applyWorkLocked(e hookevents.Event, now time.Time) {
	at := e.Time.UTC()
	if at.After(now) {
		at = now
	}
	if now.Sub(at) > EndedFor {
		return
	}
	k, agentAt, named := namedSession(e)
	var s *entry
	if named {
		s = t.sessions[k]
		// The agent's command may run under another agent it started, such as a
		// Codex session launched from a Claude command: the nearer one ran Git.
		if owner, entry, i := t.ownerLocked(e); entry != nil && i < agentAt {
			k, s = owner, entry
		}
	} else {
		k, s, _ = t.ownerLocked(e)
		if s == nil {
			return
		}
	}
	var items []workItem
	switch e.Name {
	case "pre-push":
		if e.Git == nil || len(e.Git.Args) == 0 || !hookevents.RemoteName(e.Git.Args[0]) {
			return
		}
		for _, p := range hookevents.Pushes(e.Git.Lines) {
			items = append(items, workItem{id: e.ID, at: at, dir: e.Dir, pushed: true, remote: e.Git.Args[0], branch: p.Branch, head: p.New})
		}
	case "post-rewrite":
		// A rewrite made these commits; the lines name each new one. The
		// post-commit of an amend or a rebase pick may have recorded one.
		if e.Git == nil {
			return
		}
		for _, head := range hookevents.Rewritten(e.Git.Lines) {
			if !s.hasCommit(head) {
				items = append(items, workItem{id: e.ID, at: at, dir: e.Dir, head: head})
			}
		}
	default:
		// A commit whose hook could not read it says nothing: the directory's
		// checkout later may be another branch's.
		if e.Git == nil || e.Git.Head == "" {
			return
		}
		items = []workItem{{id: e.ID, at: at, dir: e.Dir, branch: e.Git.Branch, head: e.Git.Head}}
	}
	if s == nil {
		// The named session has no entry: it ended and was dropped, or has not
		// been seen. Its work is still recorded, without a session to show.
		t.detachLocked(k, items)
		return
	}
	added := false
	for _, item := range items {
		if !slices.ContainsFunc(s.work, item.same) {
			s.work = append(s.work, item)
			added = true
		}
	}
	if !added {
		return
	}
	slices.SortStableFunc(s.work, func(a, b workItem) int { return a.at.Compare(b.at) })
	s.trimWork(t.Attributions != nil)
	if !s.unlinked {
		t.queue(k)
	}
}

// hasCommit reports whether the session recorded a commit of head; a nil
// session has none.
func (s *entry) hasCommit(head string) bool {
	return s != nil && slices.ContainsFunc(s.work, func(w workItem) bool { return !w.pushed && w.head == head })
}

// maxDetached bounds the work waiting to be looked up for sessions that are not
// tracked.
const maxDetached = 256

// detachedWork is a commit or push of a session the tracker holds no entry for.
type detachedWork struct {
	k    key
	item workItem
}

// detachLocked queues items of a session without an entry, to be looked up and
// recorded as observations by persistAttributions. Without a store there is
// nothing to record them in.
func (t *Tracker) detachLocked(k key, items []workItem) {
	if t.Attributions == nil || len(items) == 0 {
		return
	}
	dropped := 0
	for _, item := range items {
		if len(t.detached) >= maxDetached {
			dropped++
			continue
		}
		t.detached = append(t.detached, detachedWork{k, item})
	}
	if dropped > 0 {
		t.logger().Warn("commit observations of untracked sessions dropped: too many waiting", "dropped", dropped)
	}
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// resolveDetached looks up the queued work of untracked sessions and adds what
// links to a repository to the observations to record.
func (t *Tracker) resolveDetached(ctx context.Context) {
	t.mu.Lock()
	batch := t.detached
	t.detached = nil
	t.mu.Unlock()
	var observations []commitattribution.Observation
	for i, d := range batch {
		read := t.readWork(ctx, []workItem{d.item}, false)
		if ctx.Err() != nil {
			t.mu.Lock()
			t.detached = append(slices.Clone(batch[i:]), t.detached...)
			t.mu.Unlock()
			break
		}
		if len(read) != 1 || read[0].link.Repository == "" {
			continue
		}
		w, l := read[0], read[0].link
		observations = append(observations, workObservation(d.k, savedWork{WorkObservation: WorkObservation{At: w.at, Repository: l.Repository, HeadRepository: l.HeadRepository, HeadSHA: l.HeadSHA}, ID: w.id, Push: w.pushed}))
	}
	t.mu.Lock()
	for _, o := range observations {
		if !slices.Contains(t.pendingAttributions, o) {
			t.pendingAttributions = append(t.pendingAttributions, o)
		}
	}
	t.mu.Unlock()
}

// readWork looks up the items not read yet, running git, and, when fallback
// is set, looks again, in the discovery cache only, at the pull requests of
// the read ones, newest first, until one has a pull request. It returns the
// items it changed.
func (t *Tracker) readWork(ctx context.Context, work []workItem, fallback bool) []workItem {
	var out []workItem
	for i := range work {
		w := &work[i]
		if w.read || w.dir == "" {
			continue
		}
		var link Link
		var ok bool
		if linker, supported := t.Linker.(WorkLinker); supported {
			if w.pushed {
				link, ok = linker.LinkPush(ctx, w.dir, w.remote, w.branch, w.head)
			} else {
				link, ok = linker.LinkCommit(ctx, w.dir, w.branch, w.head)
			}
		}
		if ctx.Err() != nil {
			return out
		}
		if !ok {
			link = Link{}
		}
		w.link, w.read = link, true
		out = append(out, *w)
	}
	recorded, supported := t.Linker.(RecordedLinker)
	if !fallback || !supported {
		return out
	}
	for i := len(work) - 1; i >= 0; i-- {
		w := &work[i]
		if !w.read || w.link.Repository == "" {
			continue
		}
		if !slices.ContainsFunc(out, w.same) {
			l := w.link
			if link, ok := recorded.Relink(ctx, Session{Repository: l.Repository, Branch: l.Branch, HeadSHA: l.HeadSHA, Upstream: l.Upstream, HeadRepository: l.HeadRepository, UpstreamRepository: l.UpstreamRepository}); ok {
				w.link.PR, w.link.Method, w.link.Status = link.PR, link.Method, link.Status
				w.link.PRState = link.PRState
				out = append(out, *w)
			}
		}
		if w.link.PR != 0 {
			break
		}
	}
	return out
}

// storeWork writes the items read back to the session's, which may have
// gained or dropped items meanwhile, and reports whether any it holds is still
// to be read.
func (s *entry) storeWork(read []workItem) bool {
	for _, r := range read {
		if i := slices.IndexFunc(s.work, r.same); i >= 0 {
			r.durable = r.durable || s.work[i].durable
			r.queued = r.queued || s.work[i].queued
			s.work[i] = r
		}
	}
	return slices.ContainsFunc(s.work, toRead)
}

// toRead reports an item still to be looked up with git.
func toRead(w workItem) bool { return !w.read && w.dir != "" }

func sameLink(a, b workItem) bool {
	return a.id == b.id && a.branch == b.branch && a.read == b.read && a.link == b.link
}

// generations are the generations (GenerationLinker) a Relink of a session
// of repository and of its read work would link against now, by repository,
// each asked once per pass; nil when the Linker cannot tell one.
func (t *Tracker) generations(ctx context.Context, repository string, work []workItem, pass map[string]*Generation) map[string]Generation {
	linker, ok := t.Linker.(GenerationLinker)
	if !ok {
		return nil
	}
	repositories := []string{repository}
	for _, w := range work {
		if w.read && w.link.Repository != "" {
			repositories = append(repositories, w.link.Repository)
		}
	}
	out := map[string]Generation{}
	for _, name := range repositories {
		g, asked := pass[name]
		if !asked {
			if generation, ok := linker.Generation(ctx, name); ok {
				g = &generation
			}
			pass[name] = g
		}
		if g == nil {
			return nil
		}
		out[name] = *g
	}
	return out
}

// withWork is the pull request of the session's latest commit or push that
// has one, or, when none has, link, its directory's. The session's own work
// comes first: its directory is shared with whatever else runs Git there, so
// what it has checked out now may be another session's branch.
func withWork(link Link, work []workItem) Link {
	for i := len(work) - 1; i >= 0; i-- {
		if w := work[i]; w.read && w.link.PR != 0 {
			found := w.link
			found.Method = MethodCommit
			if w.pushed {
				found.Method = MethodPush
			}
			return found
		}
	}
	return link
}

// savedWork is a read commit or push as StateFile keeps it: no directory.
type savedWork struct {
	WorkObservation
	ID   string `json:"id,omitempty"`
	Push bool   `json:"push,omitempty"`
}

func (s *entry) savedWork() []savedWork {
	var out []savedWork
	for _, w := range s.work {
		if !w.read || w.link.Repository == "" {
			continue
		}
		l := w.link
		out = append(out, savedWork{
			WorkObservation: WorkObservation{At: w.at, Repository: l.Repository, Branch: l.Branch, HeadSHA: l.HeadSHA, Upstream: l.Upstream, PR: l.PR, HeadRepository: l.HeadRepository, UpstreamRepository: l.UpstreamRepository},
			ID:              w.id,
			Push:            w.pushed,
		})
	}
	return out
}

func restoredWork(saved []savedWork) []workItem {
	var out []workItem
	for _, w := range saved {
		if w.Repository == "" {
			continue
		}
		// An item is named by its event, branch and commit, so the replayed
		// event does not add it again.
		out = append(out, workItem{id: w.ID, at: w.At, pushed: w.Push, branch: w.Branch, head: w.HeadSHA, read: true, link: Link{Repository: w.Repository, Branch: w.Branch, HeadSHA: w.HeadSHA, Upstream: w.Upstream, PR: w.PR, HeadRepository: w.HeadRepository, UpstreamRepository: w.UpstreamRepository}})
	}
	if len(out) > maxWork {
		out = out[len(out)-maxWork:]
	}
	return out
}

func savedKey(k key) string { return k.agent + "/" + k.id }

// trimWork bounds live history without discarding observations awaiting durable
// storage. A failed database write is retried by the next resolve or sweep.
func (s *entry) trimWork(durable bool) {
	drop := len(s.work) - maxWork
	if drop <= 0 {
		return
	}
	kept := make([]workItem, 0, len(s.work))
	for _, w := range s.work {
		if drop > 0 && (!durable || w.durable) {
			drop--
			continue
		}
		kept = append(kept, w)
	}
	s.work = kept
}

func workObservation(k key, w savedWork) commitattribution.Observation {
	repository := w.HeadRepository
	if repository == "" {
		repository = w.Repository
	}
	kind := commitattribution.Commit
	if w.Push {
		kind = commitattribution.Push
	}
	return commitattribution.Observation{Repository: repository, CommitSHA: w.HeadSHA, Agent: k.agent, SessionID: k.id, Kind: kind, EventID: w.ID, At: w.At}
}

func (t *Tracker) persistAttributions(ctx context.Context) {
	if t.Attributions == nil || ctx.Err() != nil {
		return
	}
	t.attributionMu.Lock()
	defer t.attributionMu.Unlock()
	t.resolveDetached(ctx)
	t.mu.Lock()
	pending := map[key][]workItem{}
	for k, s := range t.sessions {
		for i := range s.work {
			w := &s.work[i]
			if w.durable || !w.read || w.link.Repository == "" {
				continue
			}
			if !w.queued {
				l := w.link
				saved := savedWork{WorkObservation: WorkObservation{At: w.at, Repository: l.Repository, HeadRepository: l.HeadRepository, HeadSHA: l.HeadSHA}, ID: w.id, Push: w.pushed}
				observation := workObservation(k, saved)
				if !slices.Contains(t.pendingAttributions, observation) {
					t.pendingAttributions = append(t.pendingAttributions, observation)
				}
				w.queued = true
			}
			pending[k] = append(pending[k], *w)
		}
	}
	observations := slices.Clone(t.pendingAttributions)
	t.mu.Unlock()
	if len(observations) == 0 {
		return
	}
	if err := t.Attributions.Record(observations); err != nil {
		t.logger().Warn("commit observations not persisted", "error", err)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingAttributions = t.pendingAttributions[len(observations):]
	for k, work := range pending {
		if s := t.sessions[k]; s != nil {
			for i := range s.work {
				if slices.ContainsFunc(work, s.work[i].same) {
					s.work[i].durable = true
				}
			}
			s.trimWork(true)
		}
	}
}

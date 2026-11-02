package livesessions

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

// A session's working directory is not always where its work goes: an agent
// commits in another worktree of the clone, or on a branch it creates and
// then leaves. Git's post-commit and pre-push hooks carry their ancestors, and
// the session whose agent is among them made the commit or the push, so a
// session whose own directory links to no pull request links to the one its
// latest commit or push reaches.

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
	// id is the hook event's; with branch it names the item.
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
	link Link
	read bool
}

func (w workItem) same(o workItem) bool {
	return w.id != "" && w.id == o.id && w.branch == o.branch
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
// pushed: a post-commit, or a pre-push that let the push go, carrying the
// hook's ancestors.
func matchWork(e hookevents.Event) bool {
	return e.Kind == hookevents.KindGit && (e.Name == "post-commit" || (e.Name == "pre-push" && e.Exit == 0)) &&
		e.Terminal != nil && len(e.Terminal.Processes) > 0 && filepath.IsAbs(e.Dir)
}

func matchTracked(e hookevents.Event) bool { return matchAgent(e) || matchWork(e) }

type processKey struct {
	pid   int
	start string
}

// ownerLocked is the open session whose agent ran the Git hook of e: the
// nearest of the hook's ancestors that is the process a session's own hooks
// run under (their parent), matched by id and start time. A process two open
// sessions name is neither's.
func (t *Tracker) ownerLocked(e hookevents.Event) (key, *entry) {
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
	for _, p := range e.Terminal.Processes {
		pk := processKey{p.PID, p.Start}
		if k, ok := agents[pk]; ok {
			if shared[pk] {
				return key{}, nil
			}
			return k, t.sessions[k]
		}
	}
	return key{}, nil
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
	k, s := t.ownerLocked(e)
	if s == nil {
		return
	}
	var items []workItem
	if e.Name == "pre-push" {
		if e.Git == nil || len(e.Git.Args) == 0 || !hookevents.RemoteName(e.Git.Args[0]) {
			return
		}
		for _, p := range hookevents.Pushes(e.Git.Lines) {
			items = append(items, workItem{id: e.ID, at: at, dir: e.Dir, pushed: true, remote: e.Git.Args[0], branch: p.Branch, head: p.New})
		}
	} else {
		// A commit whose hook could not read it says nothing: the directory's
		// checkout later may be another branch's.
		if e.Git == nil || e.Git.Head == "" {
			return
		}
		items = []workItem{{id: e.ID, at: at, dir: e.Dir, branch: e.Git.Branch, head: e.Git.Head}}
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
	if len(s.work) > maxWork {
		s.work = slices.Clone(s.work[len(s.work)-maxWork:])
	}
	if !s.unlinked {
		t.queue(k)
	}
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
			s.work[i] = r
		}
	}
	return slices.ContainsFunc(s.work, func(w workItem) bool { return !w.read && w.dir != "" })
}

// withWork is link, or, when its directory gives no pull request (and does not
// give several), the pull request of the session's latest commit or push
// that has one.
func withWork(link Link, work []workItem) Link {
	if link.PR != 0 || link.Status == "ambiguous" {
		return link
	}
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
		// An item is named by its event and branch, so the replayed event does
		// not add it again.
		out = append(out, workItem{id: w.ID, at: w.At, pushed: w.Push, branch: w.Branch, head: w.HeadSHA, read: true, link: Link{Repository: w.Repository, Branch: w.Branch, HeadSHA: w.HeadSHA, Upstream: w.Upstream, PR: w.PR, HeadRepository: w.HeadRepository, UpstreamRepository: w.UpstreamRepository}})
	}
	if len(out) > maxWork {
		out = out[len(out)-maxWork:]
	}
	return out
}

func savedKey(k key) string { return k.agent + "/" + k.id }

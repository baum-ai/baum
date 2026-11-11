package livesessions

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/prlink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/workstats"
)

// Registry lists the registered clones; *repos.Registry is one.
type Registry interface {
	List(context.Context) ([]repos.Clone, error)
}

// Discovery reads cached open pull requests and terminal observations without a
// request to GitHub; *discovery.Service is one.
type Discovery interface {
	Cached(context.Context, github.Repository) (discovery.Snapshot, error)
}

// Revisions is a Discovery that stamps its caches (discovery.Revision);
// *discovery.Service is one. The Resolver then reads a repository's cache
// again only once its revision changed.
type Revisions interface {
	Revision(context.Context, github.Repository) (discovery.Revision, error)
}

// Generation is what a repository's recorded work was linked against: the
// cache's revision and whether it was stale then. Relink links the same
// evidence the same way while its repository's generation is unchanged.
type Generation struct {
	revision discovery.Revision
	stale    bool
}

// GenerationLinker is a RecordedLinker that tells the generation of a
// repository's links; *Resolver is one, with a Discovery that has Revisions.
type GenerationLinker interface {
	RecordedLinker
	// Generation is false when it cannot be told: the repository is then
	// relinked whatever it was linked against.
	Generation(ctx context.Context, repository string) (Generation, bool)
}

// Lookup bounds. Directories outside every registered clone are common (any
// project an agent works in), so a failed lookup is remembered for a while.
const (
	clonesFor   = 30 * time.Second
	unlinkedFor = time.Minute
	gitTimeout  = 5 * time.Second
	maxUnlinked = 256
)

// Resolver is the Linker the daemon uses. It follows the existing linkage and
// adds nothing to it: the directory's clone is the registered clone sharing
// its git directory (a linked worktree resolves to its clone, as it does for
// session linkage), the branch is what git has checked out, and the pull
// request is the open one of the discovery cache whose head is that branch.
// A cached merged or closed PR can still match the exact same-repository head.
// It never contacts GitHub and never fetches.
type Resolver struct {
	Registry  Registry
	Discovery Discovery
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu     sync.Mutex
	clones []repos.Clone
	loaded time.Time
	// failed is when listing the clones last failed, so a failing registry
	// is asked again only after clonesFor, not by every lookup.
	failed   time.Time
	unlinked map[string]time.Time
	// worked is what git last said each directory has checked out, kept
	// while its gitlocal.CheckoutStamp is unchanged, for at most workedFor:
	// an active session is relinked every RelinkEvery, and reading its
	// branch, upstream and remotes runs several git processes.
	worked map[string]workedEntry
	// snapshots are the discovery caches last read, by repository, at the
	// revision read before them: every session and commit of a repository
	// links against one decoded cache until discovery writes it again.
	snapshots map[string]readSnapshot
}

type readSnapshot struct {
	revision discovery.Revision
	snapshot discovery.Snapshot
	err      error
}

// maxSnapshots bounds the caches kept, one per repository sessions work in.
const maxSnapshots = 64

// workedEntry is one directory's checked-out work as git last read it.
type workedEntry struct {
	stamp      string
	at         time.Time
	common     string
	repository string
	work       prlink.Work
}

const (
	// workedFor bounds how long a directory's work is reused without git,
	// for what the stamp does not see (a global configuration edit).
	workedFor = 10 * time.Minute
	maxWorked = 256
)

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// registered is the registered clones, listed at most every clonesFor: listing
// checks each clone with git.
func (r *Resolver) registered(ctx context.Context) []repos.Clone {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.failed.IsZero() && now.Sub(r.failed) < clonesFor {
		return r.clones
	}
	if r.loaded.IsZero() || now.Sub(r.loaded) >= clonesFor {
		clones, err := r.Registry.List(ctx)
		if err != nil {
			r.failed = now
			return r.clones
		}
		r.clones, r.loaded, r.failed = clones, now, time.Time{}
	}
	return r.clones
}

// Link implements Linker.
func (r *Resolver) Link(ctx context.Context, cwd string) (Link, bool) {
	if !filepath.IsAbs(cwd) {
		return Link{}, false
	}
	now := r.now()
	r.mu.Lock()
	if at, ok := r.unlinked[cwd]; ok && now.Sub(at) < unlinkedFor {
		r.mu.Unlock()
		return Link{}, false
	}
	r.mu.Unlock()
	link, ok := r.link(ctx, cwd)
	if !ok && ctx.Err() == nil {
		r.mu.Lock()
		if r.unlinked == nil || len(r.unlinked) >= maxUnlinked {
			r.unlinked = map[string]time.Time{}
		}
		r.unlinked[cwd] = now
		r.mu.Unlock()
	}
	return link, ok
}

func (r *Resolver) link(ctx context.Context, cwd string) (Link, bool) {
	stamp, _, stamped := gitlocal.CheckoutStamp(cwd)
	if stamped {
		if entry, ok := r.reuse(ctx, cwd, stamp); ok {
			workstats.Inc("livesessions.relink_reused")
			return r.resolve(ctx, entry.repository, entry.work)
		}
	}
	repo, repository, common, ok := r.open(ctx, cwd)
	if !ok {
		return Link{}, false
	}
	defer func() { _ = repo.Close() }()
	work := prlink.Local(ctx, repo, repository)
	if stamped && ctx.Err() == nil {
		r.mu.Lock()
		if r.worked == nil || len(r.worked) >= maxWorked {
			r.worked = map[string]workedEntry{}
		}
		r.worked[cwd] = workedEntry{stamp: stamp, at: r.now(), common: common, repository: repository, work: work}
		r.mu.Unlock()
	}
	return r.resolve(ctx, repository, work)
}

// reuse is the work last read in dir while its stamp is unchanged, younger
// than workedFor, and its clone still an available registered one.
func (r *Resolver) reuse(ctx context.Context, dir, stamp string) (workedEntry, bool) {
	r.mu.Lock()
	entry, ok := r.worked[dir]
	r.mu.Unlock()
	if !ok || entry.stamp != stamp || r.now().Sub(entry.at) >= workedFor {
		return workedEntry{}, false
	}
	for _, clone := range r.registered(ctx) {
		if clone.Available && clone.CommonDir == entry.common {
			return entry, true
		}
	}
	return workedEntry{}, false
}

// open opens the working tree dir when its git directory is an available
// registered clone's, and names the clone's repository in lower case and its
// common git directory.
func (r *Resolver) open(ctx context.Context, dir string) (*gitlocal.Repo, string, string, bool) {
	clones := r.registered(ctx)
	if len(clones) == 0 {
		return nil, "", "", false
	}
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{CommandTimeout: gitTimeout})
	if err != nil {
		return nil, "", "", false
	}
	common, err := filepath.EvalSymlinks(repo.CommonDir())
	if err == nil {
		for _, clone := range clones {
			if clone.Available && clone.CommonDir == common {
				return repo, strings.ToLower(clone.Repository), common, true
			}
		}
	}
	_ = repo.Close()
	return nil, "", "", false
}

// LinkCommit implements WorkLinker: the commit head made on branch in the
// working tree dir, with the remotes the clone has (prlink.At), not what dir
// has checked out since. It runs git and never fetches.
func (r *Resolver) LinkCommit(ctx context.Context, dir, branch, head string) (Link, bool) {
	if !filepath.IsAbs(dir) {
		return Link{}, false
	}
	repo, repository, _, ok := r.open(ctx, dir)
	if !ok {
		return Link{}, false
	}
	defer func() { _ = repo.Close() }()
	return r.resolve(ctx, repository, prlink.At(ctx, repo, repository, branch, head))
}

// LinkPush implements WorkLinker: the pushed branch and commit, in the
// repository the remote's push URL names (a fork's own branch is another
// branch), as refresh on hook events reads a push. It runs git and never
// fetches.
func (r *Resolver) LinkPush(ctx context.Context, dir, remote, branch, head string) (Link, bool) {
	if !filepath.IsAbs(dir) {
		return Link{}, false
	}
	repo, repository, _, ok := r.open(ctx, dir)
	if !ok {
		return Link{}, false
	}
	defer func() { _ = repo.Close() }()
	url, err := repo.RemotePushURL(ctx, remote)
	if err != nil || url == "" {
		return Link{}, false
	}
	pushed, err := github.RepositoryFromRemote(url)
	if err != nil {
		return Link{}, false
	}
	return r.resolve(ctx, repository, prlink.Work{Repository: strings.ToLower(pushed.String()), Branch: branch, Head: head})
}

// Relink uses recorded branch, upstream and HEAD evidence, never the checkout.
func (r *Resolver) Relink(ctx context.Context, s Session) (Link, bool) {
	if s.LinkStatus == "unknown_history" {
		return Link{Repository: s.Repository, Status: "unknown_history"}, true
	}
	return r.resolve(ctx, s.Repository, prlink.Work{Branch: s.Branch, Upstream: s.Upstream, Head: s.HeadSHA, Repository: s.HeadRepository, UpstreamRepository: s.UpstreamRepository})
}

// Generation implements GenerationLinker. It reads the repository's cache
// when its revision changed, so the links that follow in the same pass reuse
// it.
func (r *Resolver) Generation(ctx context.Context, repository string) (Generation, bool) {
	if r.Discovery == nil {
		return Generation{}, true
	}
	parsed, err := github.ParseRepository(repository)
	if err != nil {
		return Generation{}, false
	}
	_, generation, ok, _ := r.cached(ctx, parsed)
	return generation, ok
}

// cached is the repository's discovery cache as Cached reads it, read again
// only when its revision changed, with the generation it is; false when the
// revision cannot be told, and the cache is read every time.
func (r *Resolver) cached(ctx context.Context, repository github.Repository) (discovery.Snapshot, Generation, bool, error) {
	revisions, ok := r.Discovery.(Revisions)
	if !ok {
		snapshot, err := r.Discovery.Cached(ctx, repository)
		return snapshot, Generation{}, false, err
	}
	revision, err := revisions.Revision(ctx, repository)
	if err != nil {
		snapshot, err := r.Discovery.Cached(ctx, repository)
		return snapshot, Generation{}, false, err
	}
	name := strings.ToLower(repository.String())
	r.mu.Lock()
	read, hit := r.snapshots[name]
	r.mu.Unlock()
	if !hit || read.revision != revision {
		snapshot, err := r.Discovery.Cached(ctx, repository)
		// A repository not polled yet stays so until discovery writes its
		// cache; any other failure may pass, and is read again.
		if ctx.Err() != nil || (err != nil && !errors.Is(err, fs.ErrNotExist)) {
			return snapshot, Generation{}, false, err
		}
		read = readSnapshot{revision: revision, snapshot: snapshot, err: err}
		r.mu.Lock()
		if _, kept := r.snapshots[name]; !kept && (r.snapshots == nil || len(r.snapshots) >= maxSnapshots) {
			r.snapshots = map[string]readSnapshot{}
		}
		r.snapshots[name] = read
		r.mu.Unlock()
	}
	snapshot := read.snapshot
	// A cache whose next poll is due is stale, as Cached would read it now,
	// though nothing wrote it.
	snapshot.Stale = snapshot.Stale || !r.now().Before(snapshot.NextPoll)
	return snapshot, Generation{revision: revision, stale: snapshot.Stale}, true, read.err
}

func (r *Resolver) resolve(ctx context.Context, repository string, work prlink.Work) (Link, bool) {
	link := Link{Repository: repository, Branch: work.Branch, HeadSHA: work.Head, Upstream: work.Upstream, Status: "unavailable", HeadRepository: work.Repository, UpstreamRepository: work.UpstreamRepository}
	if r.Discovery == nil {
		return link, true
	}
	parsed, err := github.ParseRepository(repository)
	if err != nil {
		return Link{}, false
	}
	snapshot, _, _, err := r.cached(ctx, parsed)
	if err != nil {
		return link, true
	}
	selection := prlink.Resolve(snapshot, work)
	if selection.PR != 0 {
		link.PRState = "OPEN"
	}
	if selection.PR == 0 && selection.Status != "ambiguous" && work.Head != "" && strings.EqualFold(work.Repository, repository) {
		for _, observation := range snapshot.Observations {
			if (observation.State != "MERGED" && observation.State != "CLOSED") || !strings.EqualFold(observation.HeadSHA, work.Head) {
				continue
			}
			if selection.PR != 0 && selection.PR != observation.Number {
				selection = prlink.Selection{Status: "ambiguous"}
				link.PRState = ""
				break
			}
			status := "resolved"
			if snapshot.Stale {
				status = "stale"
			}
			selection = prlink.Selection{PR: observation.Number, Method: "head", Status: status}
			link.PRState = observation.State
		}
	}
	link.PR, link.Method, link.Status = selection.PR, selection.Method, selection.Status
	return link, true
}

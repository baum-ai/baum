package livesessions

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/prlink"
	"github.com/baum-ai/baum/internal/repos"
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
}

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
	repo, repository, ok := r.open(ctx, cwd)
	if !ok {
		return Link{}, false
	}
	defer func() { _ = repo.Close() }()
	return r.resolve(ctx, repository, prlink.Local(ctx, repo, repository))
}

// open opens the working tree dir when its git directory is an available
// registered clone's, and names the clone's repository in lower case.
func (r *Resolver) open(ctx context.Context, dir string) (*gitlocal.Repo, string, bool) {
	clones := r.registered(ctx)
	if len(clones) == 0 {
		return nil, "", false
	}
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{CommandTimeout: gitTimeout})
	if err != nil {
		return nil, "", false
	}
	common, err := filepath.EvalSymlinks(repo.CommonDir())
	if err == nil {
		for _, clone := range clones {
			if clone.Available && clone.CommonDir == common {
				return repo, strings.ToLower(clone.Repository), true
			}
		}
	}
	_ = repo.Close()
	return nil, "", false
}

// LinkCommit implements WorkLinker: the commit head made on branch in the
// working tree dir, with the remotes the clone has (prlink.At), not what dir
// has checked out since. It runs git and never fetches.
func (r *Resolver) LinkCommit(ctx context.Context, dir, branch, head string) (Link, bool) {
	if !filepath.IsAbs(dir) {
		return Link{}, false
	}
	repo, repository, ok := r.open(ctx, dir)
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
	repo, repository, ok := r.open(ctx, dir)
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

func (r *Resolver) resolve(ctx context.Context, repository string, work prlink.Work) (Link, bool) {
	link := Link{Repository: repository, Branch: work.Branch, HeadSHA: work.Head, Upstream: work.Upstream, Status: "unavailable", HeadRepository: work.Repository, UpstreamRepository: work.UpstreamRepository}
	if r.Discovery == nil {
		return link, true
	}
	parsed, err := github.ParseRepository(repository)
	if err != nil {
		return Link{}, false
	}
	snapshot, err := r.Discovery.Cached(ctx, parsed)
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

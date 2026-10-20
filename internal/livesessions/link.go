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

// Discovery reads the cached open pull requests of a repository without a
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
	clones := r.registered(ctx)
	if len(clones) == 0 {
		return Link{}, false
	}
	repo, err := gitlocal.Open(ctx, cwd, gitlocal.Options{CommandTimeout: gitTimeout})
	if err != nil {
		return Link{}, false
	}
	defer func() { _ = repo.Close() }()
	common, err := filepath.EvalSymlinks(repo.CommonDir())
	if err != nil {
		return Link{}, false
	}
	var clone *repos.Clone
	for i := range clones {
		if clones[i].Available && clones[i].CommonDir == common {
			clone = &clones[i]
			break
		}
	}
	if clone == nil {
		return Link{}, false
	}
	link := Link{Repository: strings.ToLower(clone.Repository)}
	work := prlink.Local(ctx, repo, link.Repository)
	link.Branch = work.Branch
	link.PR = r.pullRequest(ctx, link.Repository, work)
	return link, true
}

// pullRequest is the open pull request of repository holding the session's
// work (prlink.Find), 0 when the discovery cache has none.
func (r *Resolver) pullRequest(ctx context.Context, repository string, work prlink.Work) int {
	if r.Discovery == nil {
		return 0
	}
	parsed, err := github.ParseRepository(repository)
	if err != nil {
		return 0
	}
	snapshot, err := r.Discovery.Cached(ctx, parsed)
	if err != nil {
		return 0
	}
	return prlink.Find(snapshot, work)
}

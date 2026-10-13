package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
)

// Repository names a GitHub repository.
type Repository struct {
	Owner, Name string
}

func (r Repository) String() string { return r.Owner + "/" + r.Name }

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ParseRepository reads "owner/name".
func ParseRepository(value string) (Repository, error) {
	owner, name, found := strings.Cut(strings.TrimSpace(value), "/")
	if !found || !ownerPattern.MatchString(owner) || !namePattern.MatchString(name) || name == "." || name == ".." {
		return Repository{}, fmt.Errorf("repository %q is not owner/name", value)
	}
	return Repository{Owner: owner, Name: name}, nil
}

// ErrNotGitHubRemote reports a remote URL that names no GitHub repository.
var ErrNotGitHubRemote = errors.New("the remote is not a GitHub repository; pass --repo owner/name")

// RepositoryFromRemote reads the GitHub repository a git remote URL names, in
// the https, ssh and scp-like forms. The host must be github.com, one of its
// subdomains (such as ssh.github.com), or an SSH host alias naming GitHub
// (such as "github-work"); any other host is a different service.
func RepositoryFromRemote(remote string) (Repository, error) {
	host, repository, ok := gitlocal.RemoteRepository(remote)
	if !ok || !gitHubHost(host) {
		return Repository{}, ErrNotGitHubRemote
	}
	return ParseRepository(repository)
}

func gitHubHost(host string) bool {
	if host == "github.com" || strings.HasSuffix(host, ".github.com") {
		return true
	}
	return !strings.Contains(host, ".") && strings.Contains(host, "github")
}

// Clone is the local clone a pull request is resolved from; *gitlocal.Repo
// implements it.
type Clone interface {
	// RemoteFetchURL is the URL git fetches the named remote from, with
	// insteadOf rewrites applied; "" when there is no such remote.
	RemoteFetchURL(ctx context.Context, remote string) (string, error)
	// CurrentBranch is the branch HEAD is on, "" when detached.
	CurrentBranch(ctx context.Context) (string, error)
	// BranchUpstream is the branch's branch.<name>.remote and
	// branch.<name>.merge, each "" when unset.
	BranchUpstream(ctx context.Context, branch string) (remote, merge string, err error)
}

var _ Clone = (*gitlocal.Repo)(nil)

// ResolveOptions are the --repo and --pr flags; the zero value of each means
// it was not given.
type ResolveOptions struct {
	Repo string
	PR   int
}

// Target is the pull request a command works on.
type Target struct {
	Repository Repository
	Number     int
}

var (
	// ErrNoRemote reports a clone without an origin remote and no --repo.
	ErrNoRemote = errors.New("this clone has no origin remote; pass --repo owner/name")
	// ErrDetachedHead reports a detached HEAD and no --pr.
	ErrDetachedHead = errors.New("HEAD is detached, so there is no branch to find a pull request for; pass --pr")
	// ErrNoPullRequest reports a branch GitHub knows no pull request for.
	ErrNoPullRequest = errors.New("no pull request found for the current branch; pass --pr")
	// ErrAmbiguousPullRequest reports a branch with several open pull
	// requests.
	ErrAmbiguousPullRequest = errors.New("the current branch has several open pull requests; pass --pr")
)

// pullRef is the ref GitHub serves a pull request's head (or test merge)
// under, which `gh pr checkout` sets as a fork branch's upstream.
var pullRef = regexp.MustCompile(`^refs/pull/([1-9][0-9]*)/(?:head|merge)$`)

// ResolvePR finds the pull request to work on. With --pr it is that pull
// request on --repo or on the repository the clone's origin remote names,
// and no request is made. Otherwise it follows the current branch:
//
//   - When the branch's upstream is refs/pull/N/head (or /merge) of a GitHub
//     remote, it is pull request N there.
//   - When the upstream is a branch of a GitHub remote, it is the pull
//     request whose head is that branch, as GitHub's associatedPullRequests
//     lists it: the open one, or else the most recently updated. That pull
//     request may target another repository, such as the upstream of a fork.
//   - Failing those, it is the pull request whose head is the branch of the
//     same name on --repo or origin.
//
// With --repo, a pull request found through the upstream counts only when it
// is in that repository. Remote URLs are read with insteadOf applied.
func (c *Client) ResolvePR(ctx context.Context, clone Clone, opts ResolveOptions) (Target, error) {
	if opts.PR < 0 {
		return Target{}, fmt.Errorf("pull request number must be positive, got %d", opts.PR)
	}
	var flagRepo *Repository
	if opts.Repo != "" {
		repo, err := ParseRepository(opts.Repo)
		if err != nil {
			return Target{}, err
		}
		flagRepo = &repo
	}
	if opts.PR > 0 {
		repo, err := defaultRepository(ctx, clone, flagRepo)
		if err != nil {
			return Target{}, err
		}
		return Target{Repository: repo, Number: opts.PR}, nil
	}
	branch, err := clone.CurrentBranch(ctx)
	if err != nil {
		return Target{}, fmt.Errorf("reading the current branch: %w", err)
	}
	if branch == "" {
		return Target{}, ErrDetachedHead
	}

	accept := func(target Target) bool { return flagRepo == nil || sameRepository(target.Repository, *flagRepo) }
	// asked is the head branch already looked up through the upstream, so
	// the fallback does not ask GitHub the same question twice.
	var asked *Target
	remote, merge, err := clone.BranchUpstream(ctx, branch)
	if err != nil {
		return Target{}, fmt.Errorf("reading the upstream of branch %s: %w", branch, err)
	}
	if upstream, ok := upstreamRepository(ctx, clone, remote, merge); ok {
		if match := pullRef.FindStringSubmatch(merge); match != nil {
			number, _ := strconv.Atoi(match[1])
			if target := (Target{Repository: upstream, Number: number}); accept(target) {
				return target, nil
			}
		} else if name, ok := strings.CutPrefix(merge, "refs/heads/"); ok && name != "" {
			target, err := c.pullRequestForBranch(ctx, upstream, name)
			if err != nil && !errors.Is(err, ErrNoPullRequest) {
				return Target{}, err
			}
			if err == nil && accept(target) {
				return target, nil
			}
			if name == branch {
				asked = &Target{Repository: upstream}
			}
		}
	}

	repo, err := defaultRepository(ctx, clone, flagRepo)
	if err != nil {
		return Target{}, err
	}
	if asked != nil && sameRepository(asked.Repository, repo) {
		return Target{}, fmt.Errorf("%w (branch %s on %s)", ErrNoPullRequest, branch, repo)
	}
	return c.pullRequestForBranch(ctx, repo, branch)
}

// upstreamRepository is the GitHub repository a branch's upstream remote
// names. The remote may be a remote's name or, as git allows, a URL.
func upstreamRepository(ctx context.Context, clone Clone, remote, merge string) (Repository, bool) {
	if remote == "" || remote == "." || merge == "" {
		return Repository{}, false
	}
	url, err := clone.RemoteFetchURL(ctx, remote)
	if err != nil {
		return Repository{}, false
	}
	if url == "" {
		url = remote
	}
	repo, err := RepositoryFromRemote(url)
	return repo, err == nil
}

// defaultRepository is --repo when given, else the repository origin names.
func defaultRepository(ctx context.Context, clone Clone, flagRepo *Repository) (Repository, error) {
	if flagRepo != nil {
		return *flagRepo, nil
	}
	remote, err := clone.RemoteFetchURL(ctx, gitlocal.DefaultRemote)
	if err != nil {
		return Repository{}, fmt.Errorf("reading the origin remote: %w", err)
	}
	if remote == "" {
		return Repository{}, ErrNoRemote
	}
	return RepositoryFromRemote(remote)
}

func sameRepository(a, b Repository) bool {
	return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

func (c *Client) pullRequestForBranch(ctx context.Context, repo Repository, branch string) (Target, error) {
	var q branchPullRequestsQuery
	variables := map[string]any{"owner": repo.Owner, "name": repo.Name, "ref": "refs/heads/" + branch}
	if err := c.query(ctx, "BranchPullRequests", &q, variables); err != nil {
		return Target{}, fmt.Errorf("finding the pull request for branch %s: %w", branch, err)
	}
	if q.Repository.Ref == nil {
		return Target{}, fmt.Errorf("%w (branch %s is not on %s)", ErrNoPullRequest, branch, repo)
	}
	var open, other []Target
	for _, node := range q.Repository.Ref.AssociatedPullRequests.Nodes {
		if node.HeadRefName != branch || node.Number <= 0 {
			continue
		}
		target := Target{Repository: repo, Number: node.Number}
		if node.BaseRepository != nil {
			if base, err := ParseRepository(node.BaseRepository.NameWithOwner); err == nil {
				target.Repository = base
			}
		}
		if strings.EqualFold(node.State, "OPEN") {
			open = append(open, target)
		} else {
			other = append(other, target)
		}
	}
	switch {
	case len(open) == 1:
		return open[0], nil
	case len(open) > 1:
		numbers := make([]string, 0, len(open))
		for _, target := range open {
			numbers = append(numbers, target.Repository.String()+"#"+strconv.Itoa(target.Number))
		}
		return Target{}, fmt.Errorf("%w: %s", ErrAmbiguousPullRequest, strings.Join(numbers, ", "))
	case len(other) > 0:
		return other[0], nil
	}
	return Target{}, fmt.Errorf("%w (branch %s on %s)", ErrNoPullRequest, branch, repo)
}

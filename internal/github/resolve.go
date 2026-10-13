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
	// RemoteURL is the URL of the clone's origin remote, "" without one.
	RemoteURL(ctx context.Context) (string, error)
	// CurrentBranch is the branch HEAD is on, "" when detached.
	CurrentBranch(ctx context.Context) (string, error)
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

// ResolvePR finds the pull request to work on. The repository is --repo or
// the one the clone's origin remote names; the pull request is --pr or, by
// default, the pull request whose head is the current branch, as GitHub's
// associatedPullRequests lists it: the open one, or else the most recently
// updated. A branch pushed from a fork resolves to the pull request's base
// repository. With both flags given it makes no request.
func (c *Client) ResolvePR(ctx context.Context, clone Clone, opts ResolveOptions) (Target, error) {
	if opts.PR < 0 {
		return Target{}, fmt.Errorf("pull request number must be positive, got %d", opts.PR)
	}
	var repo Repository
	var err error
	if opts.Repo != "" {
		repo, err = ParseRepository(opts.Repo)
	} else {
		repo, err = originRepository(ctx, clone)
	}
	if err != nil {
		return Target{}, err
	}
	if opts.PR > 0 {
		return Target{Repository: repo, Number: opts.PR}, nil
	}
	branch, err := clone.CurrentBranch(ctx)
	if err != nil {
		return Target{}, fmt.Errorf("reading the current branch: %w", err)
	}
	if branch == "" {
		return Target{}, ErrDetachedHead
	}
	return c.pullRequestForBranch(ctx, repo, branch)
}

func originRepository(ctx context.Context, clone Clone) (Repository, error) {
	remote, err := clone.RemoteURL(ctx)
	if err != nil {
		return Repository{}, fmt.Errorf("reading the origin remote: %w", err)
	}
	if remote == "" {
		return Repository{}, ErrNoRemote
	}
	return RepositoryFromRemote(remote)
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

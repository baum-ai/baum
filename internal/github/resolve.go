package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/ssh"
	graphql "github.com/cli/shurcooL-graphql"

	"github.com/lastpersonlabs/baum/internal/config"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
)

// Repository names a repository on a GitHub host.
type Repository struct {
	Host, Owner, Name string
}

// String is "owner/name" on github.com and "host/owner/name" elsewhere.
func (r Repository) String() string {
	if r.Host == config.GitHubHost {
		return r.Owner + "/" + r.Name
	}
	return r.Host + "/" + r.Owner + "/" + r.Name
}

var (
	hostPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ErrInvalidRepository reports a --repo value that names no repository. It
// does not repeat the value, which may be a URL carrying credentials.
var ErrInvalidRepository = errors.New("--repo must be owner/name, or host/owner/name for another GitHub host")

// ParseRepository reads --repo: "owner/name" on github.com, or
// "host/owner/name" on another GitHub host such as an Enterprise Server,
// as gh reads it.
func ParseRepository(value string) (Repository, error) {
	parsed, err := repository.ParseWithHost(strings.TrimSpace(value), config.GitHubHost)
	if err != nil {
		return Repository{}, ErrInvalidRepository
	}
	repo, ok := checkedRepository(parsed.Host, parsed.Owner, parsed.Name)
	if !ok {
		return Repository{}, ErrInvalidRepository
	}
	return repo, nil
}

func checkedRepository(host, owner, name string) (Repository, bool) {
	host = auth.NormalizeHostname(host)
	if !hostPattern.MatchString(host) || !ownerPattern.MatchString(owner) ||
		!namePattern.MatchString(name) || name == "." || name == ".." {
		return Repository{}, false
	}
	return Repository{Host: host, Owner: owner, Name: name}, true
}

// ErrNotGitHubRemote reports a remote URL that names no repository on a
// GitHub host. It does not repeat the URL, which may carry credentials.
var ErrNotGitHubRemote = errors.New("the remote does not name a repository on a GitHub host; pass --repo")

// RepositoryFromRemote reads the repository a git remote URL names, in the
// https, ssh and scp-like forms, as gh reads it: an SSH host alias is
// resolved through `ssh -G`, and ssh.github.com is github.com. The host must
// be one gh treats as GitHub: github.com, a *.ghe.com tenant, a host gh is
// logged in to, or the one GH_HOST names. Any other host is another service,
// which must never be sent a GitHub token.
func RepositoryFromRemote(remote string) (Repository, error) {
	u, ok := remoteURL(strings.TrimSpace(remote))
	if !ok {
		return Repository{}, ErrNotGitHubRemote
	}
	if u.Scheme == "ssh" {
		u = ssh.NewTranslator().Translate(u)
	}
	parsed, err := repository.Parse(u.String())
	if err != nil {
		return Repository{}, ErrNotGitHubRemote
	}
	repo, ok := checkedRepository(parsed.Host, parsed.Owner, parsed.Name)
	if !ok || !gitHubHost(repo.Host) {
		return Repository{}, ErrNotGitHubRemote
	}
	return repo, nil
}

// remoteURL reads a remote as a URL, turning git's scp-like
// "[user@]host:path" form into the ssh URL it stands for; go-gh reads only
// the "git@host:path" spelling of that form.
func remoteURL(remote string) (*url.URL, bool) {
	if !strings.Contains(remote, "://") {
		// A slash before the colon makes it a local path, and a single
		// letter a Windows drive.
		host, path, found := strings.Cut(remote, ":")
		if !found || len(host) < 2 || strings.ContainsAny(host, `/\`) {
			return nil, false
		}
		remote = "ssh://" + host + "/" + path
	}
	u, err := url.Parse(remote)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git", "git+ssh", "git+https":
		return u, true
	}
	return nil, false
}

// gitHubHost reports a host gh treats as GitHub.
func gitHubHost(host string) bool {
	if host == config.GitHubHost || auth.IsTenancy(host) {
		return true
	}
	for _, known := range auth.KnownHosts() {
		if auth.NormalizeHostname(known) == host {
			return true
		}
	}
	return false
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
	return a.Host == b.Host && strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

// pullRequestForBranch reads every pull request GitHub associates with the
// branch, to the end of the list, before choosing: the head's open one, or
// else the most recently updated.
func (c *Client) pullRequestForBranch(ctx context.Context, repo Repository, branch string) (Target, error) {
	found := true
	nodes, err := paginate(nil, func(cursor *string) ([]associatedPullRequest, pageInfo, error) {
		var q branchPullRequestsQuery
		variables := repoVariables(repo)
		variables["ref"], variables["cursor"] = graphql.String("refs/heads/"+branch), (*graphql.String)(cursor)
		if err := c.query(ctx, repo.Host, "BranchPullRequests", &q, variables); err != nil {
			return nil, pageInfo{}, err
		}
		if q.Repository.Ref == nil {
			found = false
			return nil, pageInfo{}, nil
		}
		connection := q.Repository.Ref.AssociatedPullRequests
		return connection.Nodes, connection.PageInfo, nil
	})
	if err != nil {
		return Target{}, fmt.Errorf("finding the pull request for branch %s: %w", branch, err)
	}
	if !found {
		return Target{}, fmt.Errorf("%w (branch %s is not on %s)", ErrNoPullRequest, branch, repo)
	}
	var open, other []Target
	for _, node := range nodes {
		if node.HeadRefName != branch || node.Number <= 0 {
			continue
		}
		target := Target{Repository: repo, Number: node.Number}
		if node.BaseRepository != nil {
			owner, name, _ := strings.Cut(node.BaseRepository.NameWithOwner, "/")
			if base, ok := checkedRepository(repo.Host, owner, name); ok {
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

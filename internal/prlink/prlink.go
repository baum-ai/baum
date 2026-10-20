// Package prlink finds the open pull request that holds the work checked out
// in a local working tree, from the discovery cache. Live sessions use it to
// show an agent session on its pull request, and hook refresh to refresh the
// pull request an agent session ended on. It reads git configuration and the
// cache only: it never contacts GitHub and never fetches.
package prlink

import (
	"context"
	"strings"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// Work is what a working tree has checked out, as far as pull requests go.
type Work struct {
	// Branch is the current branch, "" when HEAD is detached.
	Branch string
	// Upstream is the branch of the same repository that Branch's upstream
	// names, "" when it has none there.
	Upstream string
	// Head is the commit checked out.
	Head string
}

// Local reads the work checked out in repo, a working tree of repository
// ("owner/name", as the registry names it). A part git cannot read stays
// empty.
func Local(ctx context.Context, repo *gitlocal.Repo, repository string) Work {
	var w Work
	parsed, parseErr := github.ParseRepository(repository)
	if branch, err := repo.CurrentBranch(ctx); err == nil {
		w.Branch = branch
	}
	if w.Branch != "" && parseErr == nil {
		w.Upstream = upstreamBranch(ctx, repo, parsed, w.Branch)
	}
	if head, err := repo.HeadCommit(ctx); err == nil {
		w.Head = head
	}
	return w
}

// upstreamBranch is the name of the branch of repository that branch's
// upstream is, "" when it has none there. The host counts: on an Enterprise
// host the same owner/name elsewhere is another repository.
func upstreamBranch(ctx context.Context, repo *gitlocal.Repo, repository github.Repository, branch string) string {
	remote, merge, err := repo.BranchUpstream(ctx, branch)
	name, ok := strings.CutPrefix(merge, "refs/heads/")
	if err != nil || !ok || name == "" || remote == "" || remote == "." {
		return ""
	}
	url, err := repo.RemoteFetchURL(ctx, remote)
	if err != nil {
		return ""
	}
	if url == "" {
		url = remote
	}
	if !github.RemoteNames(url, repository) {
		return ""
	}
	return name
}

// Find is the open pull request of the snapshot whose head is the work, in
// the repository itself (a fork's branch of the same name is another
// branch), the newest when several; 0 when there is none. It follows the
// branch's name, then the head branch the upstream names (a branch pushed
// under another name), then the commit checked out (a detached HEAD, or a
// push to a branch of another name without an upstream).
func Find(snapshot discovery.Snapshot, w Work) int {
	find := func(match func(discovery.PullRequest) bool) int {
		number := 0
		for _, pr := range snapshot.PullRequests {
			if !pr.Fork && match(pr) && pr.Number > number {
				number = pr.Number
			}
		}
		return number
	}
	if w.Branch != "" {
		if number := find(func(pr discovery.PullRequest) bool { return pr.HeadRef == w.Branch }); number > 0 {
			return number
		}
		if w.Upstream != "" && w.Upstream != w.Branch {
			if number := find(func(pr discovery.PullRequest) bool { return pr.HeadRef == w.Upstream }); number > 0 {
				return number
			}
		}
	}
	if w.Head == "" {
		return 0
	}
	return find(func(pr discovery.PullRequest) bool { return strings.EqualFold(pr.HeadSHA, w.Head) })
}

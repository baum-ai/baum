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
	Repository, UpstreamRepository string
	// Branch is the current branch, "" when HEAD is detached.
	Branch string
	// Upstream names the configured upstream branch on the same GitHub host;
	// UpstreamRepository distinguishes a fork from the base repository.
	Upstream string
	// Head is the commit checked out.
	Head string
}

// Local reads the work checked out in repo, a working tree of repository
// ("owner/name", as the registry names it). A part git cannot read stays
// empty.
func Local(ctx context.Context, repo *gitlocal.Repo, repository string) Work {
	w := Work{Repository: strings.ToLower(repository)}
	parsed, parseErr := github.ParseRepository(repository)
	if branch, err := repo.CurrentBranch(ctx); err == nil {
		w.Branch = branch
	}
	if parseErr == nil {
		if origin, err := repo.RemoteFetchURL(ctx, "origin"); err == nil {
			if source, err := github.RepositoryFromRemote(origin); err == nil && strings.EqualFold(source.Host, parsed.Host) {
				w.Repository = strings.ToLower(source.String())
			}
		}
	}
	if w.Branch != "" && parseErr == nil {
		w.Upstream, w.UpstreamRepository = upstreamWork(ctx, repo, parsed, w.Branch)
	}
	if head, err := repo.HeadCommit(ctx); err == nil {
		w.Head = head
	}
	return w
}

// upstreamWork keeps head identity, including a fork, but never crosses hosts.
func upstreamWork(ctx context.Context, repo *gitlocal.Repo, repository github.Repository, branch string) (string, string) {
	remote, merge, err := repo.BranchUpstream(ctx, branch)
	name, ok := strings.CutPrefix(merge, "refs/heads/")
	if err != nil || !ok || name == "" || remote == "" || remote == "." {
		return "", ""
	}
	url, err := repo.RemoteFetchURL(ctx, remote)
	if err != nil {
		return "", ""
	}
	if url == "" {
		url = remote
	}
	head, err := github.RepositoryFromRemote(url)
	if err != nil || !strings.EqualFold(head.Host, repository.Host) {
		return "", ""
	}
	return name, strings.ToLower(head.String())
}

// Selection preserves how a live PR association was established.
type Selection struct {
	PR             int
	Method, Status string
}

// Resolve refuses ambiguous or contradictory branch, upstream and HEAD evidence.
func Resolve(snapshot discovery.Snapshot, w Work) Selection {
	matches := map[int]string{}
	for _, pr := range snapshot.PullRequests {
		method := ""
		if sameHeadRepository(snapshot, pr, w.Repository) && w.Branch != "" && pr.HeadRef == w.Branch {
			method = "branch"
		}
		if sameHeadRepository(snapshot, pr, upstreamRepository(w)) && w.Upstream != "" && pr.HeadRef == w.Upstream {
			method = "upstream"
		}
		if sameHeadRepository(snapshot, pr, w.Repository) && w.Head != "" && strings.EqualFold(pr.HeadSHA, w.Head) {
			method = "head"
		}
		if method != "" {
			matches[pr.Number] = method
		}
	}
	if len(matches) > 1 {
		return Selection{Status: "ambiguous"}
	}
	for number, method := range matches {
		status := "resolved"
		if snapshot.Stale {
			status = "stale"
		}
		return Selection{PR: number, Method: method, Status: status}
	}
	if snapshot.Stale {
		return Selection{Status: "stale"}
	}
	return Selection{Status: "unmatched"}
}

// Find returns the unique matching open pull request, or zero when evidence
// is missing, conflicting or ambiguous.
func Find(snapshot discovery.Snapshot, w Work) int { return Resolve(snapshot, w).PR }

func upstreamRepository(w Work) string {
	if w.UpstreamRepository != "" {
		return w.UpstreamRepository
	}
	return w.Repository
}
func sameHeadRepository(snapshot discovery.Snapshot, pr discovery.PullRequest, want string) bool {
	head := pr.HeadRepository
	if head == "" {
		if pr.Fork {
			return false
		}
		head = snapshot.Repository
	}
	if want == "" {
		return !pr.Fork
	}
	return strings.EqualFold(head, want)
}

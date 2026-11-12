package console

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
)

// placement is where a conversation's agent works.
type placement struct {
	dir        string
	repository string
	number     int
	branch     string
	created    bool
}

// place chooses the directory of a new conversation opened from page. On a
// pull request's page of a repository with a registered, available clone it
// is a worktree with the pull request's head checked out: one that already
// exists in any clone of the repository (main checkouts included), or else
// a new one baum adds under <root>/worktrees/<id>/ (gitlocal.AddPullWorktree,
// which changes no other worktree). The person's checkouts are only read
// (git worktree list). Everywhere else, and for a pull request discovery has
// not seen or a repository with no clone, it is a new empty directory,
// <root>/sessions/<id>.
func (s *Service) place(ctx context.Context, id string, page *agentcontext.PageContext) (placement, error) {
	if page != nil && page.Page == agentcontext.PagePullRequest && page.Repository != "" && page.PullRequest > 0 && s.opts.Clones != nil && s.opts.Pull != nil {
		placed, ok, err := s.placePull(ctx, id, strings.TrimSpace(page.Repository), page.PullRequest)
		if err != nil || ok {
			return placed, err
		}
	}
	dir := filepath.Join(s.root, "sessions", id)
	if err := privatefs.MkdirAll(dir); err != nil {
		return placement{}, err
	}
	return placement{dir: dir}, nil
}

// placePull finds or adds the worktree of a pull request. ok is false when
// the pull request or the repository's clone is unknown.
func (s *Service) placePull(ctx context.Context, id, repository string, number int) (placement, bool, error) {
	pull, err := s.opts.Pull(ctx, repository, number)
	if err != nil {
		s.opts.Log.Info("conversation opened on a pull request baum cannot read; it gets an empty directory", "repository", repository, "pull_request", number, "error", err)
		return placement{}, false, nil
	}
	clones, err := s.opts.Clones(ctx)
	if err != nil {
		return placement{}, false, err
	}
	var mine []repos.Clone
	for _, c := range clones {
		if c.Available && strings.EqualFold(c.Repository, repository) {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return placement{}, false, nil
	}
	// A head branch whose name is unknown (a pull request discovery kept
	// before it recorded names) is fetched as a fork's is, by number.
	fork := pull.Fork || pull.HeadRef == ""
	for _, c := range mine {
		worktrees, err := gitlocal.Worktrees(ctx, c.Path)
		if err != nil {
			s.opts.Log.Warn("worktrees of a clone not listed", "clone", c.ID, "error", err)
			continue
		}
		for _, w := range worktrees {
			if w.Prunable || !headOf(w, pull, number, fork) {
				continue
			}
			if info, err := os.Stat(w.Path); err != nil || !info.IsDir() {
				continue
			}
			return placement{dir: w.Path, repository: repository, number: number, branch: w.Branch}, true, nil
		}
	}
	name := path.Base(filepath.ToSlash(repository))
	if name == "" || name == "." || name == "/" {
		name = "checkout"
	}
	dir := filepath.Join(s.root, "worktrees", id, name)
	repo, err := gitlocal.Open(ctx, mine[0].Path, gitlocal.Options{})
	if err != nil {
		return placement{}, false, fmt.Errorf("opening the clone of %s: %w", repository, err)
	}
	defer func() { _ = repo.Close() }()
	added, err := repo.AddPullWorktree(ctx, gitlocal.PullWorktreeOptions{Dir: dir, Number: number, HeadRef: pull.HeadRef, Fork: fork, Suffix: id[:8]})
	if added.Dir == "" {
		return placement{}, false, fmt.Errorf("checking out pull request %d of %s: %w", number, repository, err)
	}
	if err != nil {
		// The worktree is there; only its upstream is missing.
		s.opts.Log.Warn("conversation worktree has no upstream", "repository", repository, "pull_request", number, "error", err)
	}
	return placement{dir: added.Dir, repository: repository, number: number, branch: added.Branch, created: true}, true, nil
}

// headOf reports whether worktree w has the pull request's head checked out:
// its head branch by name (a fork's only while at the head commit discovery
// saw, since a fork's branch name may be anything, the base's included), or a
// branch baum names after the pull request.
func headOf(w gitlocal.Worktree, pull Pull, number int, fork bool) bool {
	if w.Branch == "" {
		return false
	}
	ours := gitlocal.PullBranch(number)
	if w.Branch == ours || strings.HasPrefix(w.Branch, ours+"-") {
		return true
	}
	if w.Branch != pull.HeadRef {
		return false
	}
	return !fork || (pull.HeadSHA != "" && w.Head == pull.HeadSHA)
}

package console

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

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
	// created is set when baum added the worktree, newBranch when it also
	// created its branch; an existing branch is the person's and is never
	// baum's to delete.
	created, newBranch bool
	// note says why a pull request's conversation got an empty directory
	// instead of a checkout.
	note string
}

// place chooses the directory of a new conversation opened from page. On a
// pull request's page of a repository with a registered, available clone it
// is a worktree with the pull request's head checked out: one that already
// exists in any clone of the repository (main checkouts included), or else
// a new one baum adds under <root>/worktrees/<id>/ (gitlocal.AddPullWorktree,
// which changes no other worktree). The person's checkouts are only read
// (git worktree list). Everywhere else, and for a pull request discovery has
// not seen, a repository with no clone or a head that cannot be checked out,
// it is a new empty directory, <root>/sessions/<id>; the last two say why in
// the note.
func (s *Service) place(ctx context.Context, id string, page *agentcontext.PageContext) (placement, error) {
	var note string
	if page != nil && page.Page == agentcontext.PagePullRequest && page.Repository != "" && page.PullRequest > 0 && s.opts.Clones != nil && s.opts.Pull != nil {
		repository := strings.TrimSpace(page.Repository)
		// Conversations on one repository are placed one at a time, so two
		// on the same pull request share the fetched ref, branch names and
		// worktree list in turn rather than racing for them.
		unlock := s.lockRepository(repository)
		placed, ok := s.placePull(ctx, id, repository, page.PullRequest)
		unlock()
		if ok {
			return placed, nil
		}
		note = placed.note
	}
	dir := filepath.Join(s.root, "sessions", id)
	if err := privatefs.MkdirAll(dir); err != nil {
		return placement{}, err
	}
	return placement{dir: dir, note: note}, nil
}

// lockRepository serializes placement on one repository; the returned
// function unlocks it.
func (s *Service) lockRepository(repository string) func() {
	key := strings.ToLower(repository)
	s.mu.Lock()
	m := s.placing[key]
	if m == nil {
		m = &sync.Mutex{}
		s.placing[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// placePull finds or adds the worktree of a pull request. ok is false when it
// cannot, and the placement's note then says why when the person should
// know: no clone, or a head that could not be checked out.
func (s *Service) placePull(ctx context.Context, id, repository string, number int) (placement, bool) {
	pull, err := s.opts.Pull(ctx, repository, number)
	if err != nil {
		s.opts.Log.Info("conversation opened on a pull request baum cannot read; it gets an empty directory", "repository", repository, "pull_request", number, "error", err)
		return placement{}, false
	}
	clones, err := s.opts.Clones(ctx)
	if err != nil {
		return placement{note: "The registered clones could not be read (" + bounded(err.Error(), 300) + "), so the agent works in an empty directory."}, false
	}
	var mine []repos.Clone
	for _, c := range clones {
		if c.Available && strings.EqualFold(c.Repository, repository) {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return placement{note: "No clone of " + repository + " is registered, so the agent works in an empty directory."}, false
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
			return placement{dir: w.Path, repository: repository, number: number, branch: w.Branch}, true
		}
	}
	name := path.Base(filepath.ToSlash(repository))
	if name == "" || name == "." || name == "/" {
		name = "checkout"
	}
	parent := filepath.Join(s.root, "worktrees", id)
	added, err := s.addWorktree(ctx, mine[0].Path, gitlocal.PullWorktreeOptions{Dir: filepath.Join(parent, name), Number: number, HeadRef: pull.HeadRef, Fork: fork, Suffix: id[:8]})
	// The head branch may be gone, or never fetchable under its name: the
	// pull request's own ref still has the head.
	if added.Dir == "" && !fork {
		s.opts.Log.Info("pull request head branch not checked out; trying the pull request ref", "repository", repository, "pull_request", number, "error", err)
		added, err = s.addWorktree(ctx, mine[0].Path, gitlocal.PullWorktreeOptions{Dir: filepath.Join(parent, name), Number: number, Fork: true, Suffix: id[:8]})
	}
	if added.Dir == "" {
		// Nothing of a failed checkout is left: AddPullWorktree adds no
		// worktree, and the conversation's own parent goes.
		_ = os.RemoveAll(parent)
		s.opts.Log.Warn("pull request not checked out for a conversation", "repository", repository, "pull_request", number, "error", err)
		return placement{note: fmt.Sprintf("Pull request %d could not be checked out (%s), so the agent works in an empty directory.", number, bounded(err.Error(), 300))}, false
	}
	if err != nil {
		// The worktree is there; only its upstream is missing.
		s.opts.Log.Warn("conversation worktree has no upstream", "repository", repository, "pull_request", number, "error", err)
	}
	return placement{dir: added.Dir, repository: repository, number: number, branch: added.Branch, created: true, newBranch: added.NewBranch}, true
}

// addWorktree opens the clone and adds the pull request's worktree.
func (s *Service) addWorktree(ctx context.Context, clone string, opts gitlocal.PullWorktreeOptions) (gitlocal.PullWorktree, error) {
	repo, err := gitlocal.Open(ctx, clone, gitlocal.Options{})
	if err != nil {
		return gitlocal.PullWorktree{}, fmt.Errorf("opening the clone: %w", err)
	}
	defer func() { _ = repo.Close() }()
	return repo.AddPullWorktree(ctx, opts)
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

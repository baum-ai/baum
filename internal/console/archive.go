package console

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// maxUncleanPaths bounds the paths an UncleanError names.
const maxUncleanPaths = 20

// UncleanError refuses an archive that would lose work, until the person
// says to discard it: uncommitted changes in the worktree baum added, commits
// only its detached HEAD holds, commits on the branch baum created for it that
// no remote has, or files in the empty directory baum made.
type UncleanError struct {
	// Changed counts the changed or untracked paths (the files, for an empty
	// directory baum made); Paths names the first of them.
	Changed int      `json:"changed"`
	Paths   []string `json:"paths,omitempty"`
	// Unpushed counts the commits on Branch that no remote has.
	Branch   string `json:"branch,omitempty"`
	Unpushed int    `json:"unpushed,omitempty"`
	// Detached counts the commits only the worktree's detached HEAD holds.
	Detached int `json:"detached,omitempty"`
	// Unreadable says why what the worktree holds could not be read: it may
	// hold work.
	Unreadable string `json:"unreadable,omitempty"`
	// Worktree is set for a worktree, unset for an empty directory.
	Worktree bool `json:"worktree"`
}

func (e *UncleanError) Error() string {
	what := "directory"
	if e.Worktree {
		what = "worktree"
	}
	var held []string
	if e.Changed > 0 {
		noun := "files"
		if e.Worktree {
			noun = "uncommitted changes"
		}
		held = append(held, fmt.Sprintf("%d %s", e.Changed, noun))
	}
	if e.Unpushed > 0 {
		held = append(held, fmt.Sprintf("%d commits on %s that no remote has", e.Unpushed, e.Branch))
	}
	if e.Detached > 0 {
		held = append(held, fmt.Sprintf("%d commits on no branch", e.Detached))
	}
	if e.Unreadable != "" {
		held = append(held, "work that could not be checked ("+e.Unreadable+")")
	}
	return fmt.Sprintf("Archiving removes this session's %s, which holds %s.", what, strings.Join(held, " and "))
}

// Archive closes the conversation (Close) and removes what baum made for it,
// then leaves it out of the list. The agent's own session, in its agent's
// store, stays. Removed are the empty directory baum made, or the worktree
// baum added unless another conversation not archived works there, with its
// branch when baum created that too. A worktree that was the person's own
// stays. Without discard, an archive that would lose work (UncleanError) is
// refused and nothing is removed. A conversation that was not running is
// checked before it is closed, so a refusal leaves it open for the person to
// have its agent commit or push; a running one is closed first (the app
// asks), since its agent may still be writing.
func (s *Service) Archive(ctx context.Context, id string, discard bool) (Conversation, error) {
	c, err := s.store.get(id)
	if err != nil || c.ArchivedAt != nil {
		return c, err
	}
	if c.State == StateRunning {
		if c, err = s.Close(ctx, id); err != nil {
			return Conversation{}, err
		}
	}
	// Archives and placements on one repository take turns: a placement
	// looking for the pull request's worktree must not find one going away,
	// and one that found it has saved its conversation before this looks for
	// conversations sharing it (Create holds the lock until then).
	key := c.Repository
	if key == "" {
		key = "conversation:" + c.ID
	}
	unlock := s.lockRepository(key)
	defer unlock()
	if c, err = s.store.get(id); err != nil || c.ArchivedAt != nil {
		return c, err
	}
	plan, err := s.cleanup(ctx, c)
	if plan.close != nil {
		defer plan.close()
	}
	if err != nil {
		return Conversation{}, err
	}
	if plan.unclean != nil && !discard {
		return Conversation{}, plan.unclean
	}
	if c.State != StateClosed {
		if _, err := s.Close(ctx, id); err != nil {
			return Conversation{}, err
		}
		// A turn sent since the check may have written more: checked again
		// now that none can run.
		if !discard {
			again, err := s.cleanup(ctx, c)
			if again.close != nil {
				defer again.close()
			}
			if err != nil {
				return Conversation{}, err
			}
			if again.unclean != nil {
				return Conversation{}, again.unclean
			}
		}
	}
	if plan.remove != nil {
		if err := plan.remove(ctx); err != nil {
			return Conversation{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, err = s.store.get(id); err != nil {
		return Conversation{}, err
	}
	// As the store keeps it.
	now := s.opts.Now().UTC().Truncate(time.Millisecond)
	c.ArchivedAt = &now
	if err := s.store.save(c); err != nil {
		return Conversation{}, err
	}
	s.publish(Event{Conversation: id, Kind: EventArchived, State: c.State})
	return c, nil
}

// cleanup is what archiving a conversation removes: remove does it, unclean
// is the work that would be lost, close releases the clone opened.
type cleanup struct {
	remove  func(context.Context) error
	unclean *UncleanError
	close   func()
}

func (s *Service) cleanup(ctx context.Context, c Conversation) (cleanup, error) {
	dir := filepath.Clean(c.Directory)
	if c.Repository == "" {
		// Only the empty directory baum made for this conversation goes.
		if dir != filepath.Join(s.root, "sessions", c.ID) {
			return cleanup{}, nil
		}
		return s.directoryCleanup(dir)
	}
	owner, ok := s.worktreeOwner(dir)
	if !ok {
		// The person's own worktree.
		return cleanup{}, nil
	}
	// Another conversation not archived that works there keeps it. Paths
	// are compared resolved: one conversation's comes from git's worktree
	// list, the adding one's from the root.
	others, err := s.store.list()
	if err != nil {
		return cleanup{}, err
	}
	for _, other := range others {
		if other.ID != c.ID && resolveExisting(other.Directory) == resolveExisting(dir) {
			return cleanup{}, nil
		}
	}
	// The conversation that added the worktree says whether baum created its
	// branch; a conversation that found it there did not.
	creator := c
	if owner != c.ID {
		if creator, err = s.store.get(owner); errors.Is(err, ErrNotFound) {
			creator, err = Conversation{}, nil
		}
		if err != nil {
			return cleanup{}, err
		}
	}
	branch := ""
	if creator.CreatedWorktree && creator.CreatedBranch && filepath.Clean(creator.Directory) == dir {
		branch = creator.Branch
	}
	plan := cleanup{}
	repo := s.cloneOf(ctx, c.Repository, dir)
	if repo != nil {
		plan.close = func() { _ = repo.Close() }
	} else {
		// Without the clone its branch cannot be read or deleted.
		branch = ""
	}
	unclean := &UncleanError{Worktree: true}
	if _, err := os.Lstat(dir); err == nil {
		changes, err := gitlocal.WorktreeChanges(ctx, dir)
		if err != nil {
			unclean.Unreadable = bounded(err.Error(), 300)
		}
		unclean.Changed, unclean.Paths, unclean.Detached = changes.Count, changes.Paths, changes.Detached
	}
	if branch != "" {
		n, err := repo.UnpushedCommits(ctx, branch)
		if err != nil {
			unclean.Unreadable = bounded(err.Error(), 300)
		}
		unclean.Branch, unclean.Unpushed = branch, n
	}
	if unclean.Changed > 0 || unclean.Unpushed > 0 || unclean.Detached > 0 || unclean.Unreadable != "" {
		plan.unclean = unclean
	}
	parent := filepath.Join(s.root, "worktrees", owner)
	plan.remove = func(ctx context.Context) error {
		if repo != nil {
			if err := repo.RemoveWorktree(ctx, dir); err != nil {
				s.opts.Log.Warn("conversation worktree not removed by git; removing its directory", "directory", dir, "error", err)
			}
		}
		if err := os.RemoveAll(parent); err != nil {
			return fmt.Errorf("removing the session's worktree: %w", err)
		}
		if repo == nil {
			return nil
		}
		// When git could not remove it, the directory is gone now: its own
		// bookkeeping entry goes, and no other worktree's.
		if err := repo.RemoveWorktree(ctx, dir); err != nil {
			s.opts.Log.Warn("conversation worktree's bookkeeping not removed", "directory", dir, "error", err)
		}
		if branch != "" {
			if err := repo.DeleteBranch(ctx, branch); err != nil {
				s.opts.Log.Warn("conversation branch not deleted", "repository", c.Repository, "branch", branch, "error", err)
			}
		}
		return nil
	}
	return plan, nil
}

// directoryCleanup removes the empty directory baum made; the files the agent
// left there are the work it would lose.
func (s *Service) directoryCleanup(dir string) (cleanup, error) {
	unclean := &UncleanError{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			unclean.Unreadable = bounded(err.Error(), 300)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		unclean.Changed++
		if len(unclean.Paths) < maxUncleanPaths {
			rel, _ := filepath.Rel(dir, path)
			unclean.Paths = append(unclean.Paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return cleanup{}, err
	}
	plan := cleanup{remove: func(context.Context) error {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing the session's directory: %w", err)
		}
		return nil
	}}
	if unclean.Changed > 0 || unclean.Unreadable != "" {
		plan.unclean = unclean
	}
	return plan, nil
}

// worktreeOwner names the conversation whose worktree directory baum added
// dir as: <root>/worktrees/<id>/<name>.
func (s *Service) worktreeOwner(dir string) (string, bool) {
	rel, err := filepath.Rel(filepath.Join(s.root, "worktrees"), dir)
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "" || parts[1] == ".." {
		return "", false
	}
	return parts[0], true
}

// cloneOf opens the registered clone of repository that lists dir among its
// worktrees, whether or not its directory is still there; nil when none
// does.
func (s *Service) cloneOf(ctx context.Context, repository, dir string) *gitlocal.Repo {
	if s.opts.Clones == nil {
		return nil
	}
	clones, err := s.opts.Clones(ctx)
	if err != nil {
		s.opts.Log.Warn("registered clones not read for an archive", "error", err)
		return nil
	}
	for _, c := range clones {
		if !c.Available || !strings.EqualFold(c.Repository, repository) {
			continue
		}
		worktrees, err := gitlocal.Worktrees(ctx, c.Path)
		if err != nil {
			continue
		}
		for _, w := range worktrees {
			if w.Main || resolveExisting(w.Path) != resolveExisting(dir) {
				continue
			}
			repo, err := gitlocal.Open(ctx, c.Path, gitlocal.Options{})
			if err != nil {
				s.opts.Log.Warn("clone not opened for an archive", "clone", c.ID, "error", err)
				return nil
			}
			return repo
		}
	}
	return nil
}

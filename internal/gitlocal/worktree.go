package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cleanupTimeout bounds worktree removal, which runs even after the caller's
// context is cancelled.
const cleanupTimeout = time.Minute

// Worktree is a detached checkout of one commit in a temporary directory
// outside the clone. Close removes it.
type Worktree struct {
	// Dir is the checkout's root.
	Dir string

	repo *Repo
	root string
	once sync.Once
	err  error
}

// AddWorktree checks commit rev out into a new temporary directory with
// git worktree add --detach. The clone's HEAD, branches, index and working
// tree are unchanged. Hooks and LFS smudge filters do not run, so the
// checkout cannot execute repository-supplied programs or reach the
// network. On any failure nothing is left behind. The caller must Close the
// result.
func (r *Repo) AddWorktree(ctx context.Context, rev string) (*Worktree, error) {
	if !validOID(rev) {
		return nil, ErrInvalidInput
	}
	root, err := os.MkdirTemp(r.opts.TempDir, "baum-worktree-*")
	if err != nil {
		return nil, fmt.Errorf("creating worktree directory: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		_ = removeAll(root)
		return nil, err
	}
	if inside(r.dir, root) {
		_ = removeAll(root)
		return nil, fmt.Errorf("%w: worktree directory %s is inside the repository; set TMPDIR elsewhere", ErrInvalidInput, root)
	}
	w := &Worktree{Dir: filepath.Join(root, "checkout"), repo: r, root: root}
	_, err = run(ctx, runSpec{
		dir: r.dir, mode: envHermetic, timeout: r.opts.FetchTimeout,
		config: []string{
			"core.hooksPath=/dev/null", "core.fsmonitor=false",
			"filter.lfs.smudge=", "filter.lfs.process=", "filter.lfs.required=false",
			"core.attributesFile=/dev/null",
		},
	}, "worktree", "add", "--detach", "--force", "--", w.Dir, rev)
	if err != nil {
		// The add may have registered the worktree before failing.
		cerr := w.Close()
		return nil, errors.Join(fmt.Errorf("adding worktree: %w", err), cerr)
	}
	return w, nil
}

func inside(parent, child string) bool {
	if p, err := filepath.EvalSymlinks(parent); err == nil {
		parent = p
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Close removes the worktree: git worktree remove --force, then a prune of
// stale registrations, then the temporary directory. It uses its own
// context so a cancelled run still cleans up, and is safe to call more than
// once.
func (w *Worktree) Close() error {
	w.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		spec := runSpec{dir: w.repo.dir, mode: envHermetic, timeout: cleanupTimeout, config: []string{"core.hooksPath=/dev/null"}}
		var errs []error
		if _, err := os.Stat(w.Dir); err == nil {
			if _, err := run(ctx, spec, "worktree", "remove", "--force", "--force", "--", w.Dir); err != nil {
				errs = append(errs, fmt.Errorf("removing worktree: %w", err))
			}
		}
		if err := removeAll(w.root); err != nil {
			errs = append(errs, fmt.Errorf("removing worktree directory: %w", err))
		}
		if _, err := run(ctx, spec, "worktree", "prune"); err != nil {
			errs = append(errs, fmt.Errorf("pruning worktrees: %w", err))
		}
		w.err = errors.Join(errs...)
	})
	return w.err
}

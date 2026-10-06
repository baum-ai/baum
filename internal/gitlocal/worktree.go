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

// worktreeConfig are extra -c settings for the checkout, and
// beforeWorktreeAdd runs just before it; tests set them.
var (
	worktreeConfig    []string
	beforeWorktreeAdd func()
)

// AddWorktree checks commit rev out into a new temporary directory with
// git worktree add --detach. The worktree belongs to the private
// repository, not the clone, so the clone's worktree registrations, HEAD,
// branches, index and working tree are unchanged. The checkout runs with
// default configuration: no hooks, and no clean, smudge or LFS filters, so
// it cannot execute repository-supplied or configured programs or reach the
// network. The private repository's info/attributes switches off the
// checkout conversions a commit's own .gitattributes could ask for (line
// endings, ident, working-tree encoding, filters), so the files are the
// committed bytes, as in the hosted mirror's export. On any failure nothing
// is left behind. Closing the Repo closes the worktree too.
func (r *Repo) AddWorktree(ctx context.Context, rev string) (*Worktree, error) {
	if !validOID(rev) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	if err := checkTempDir(r.dir, r.opts.TempDir); err != nil {
		return nil, err
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
	w := &Worktree{Dir: filepath.Join(root, "checkout"), repo: r, root: root}
	r.mu.Lock()
	r.worktrees[w] = struct{}{}
	r.mu.Unlock()
	if beforeWorktreeAdd != nil {
		beforeWorktreeAdd()
	}
	_, err = run(ctx, runSpec{
		dir: r.mirror, env: mirrorEnv(r.mirror), timeout: r.opts.FetchTimeout,
		config: append([]string{"core.hooksPath=" + filepath.Join(r.mirror, "hooks")}, worktreeConfig...),
	}, "worktree", "add", "--detach", "--force", "--", w.Dir, rev)
	if err != nil {
		// The add may have registered the worktree before failing.
		cerr := w.Close()
		return nil, errors.Join(fmt.Errorf("adding worktree: %w", r.missingObjects(err)), cerr)
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

// Close removes the worktree: git worktree remove --force, the temporary
// directory, then a prune of the private repository's registrations, which
// are only ever baum's own. It uses its own context so a cancelled run
// still cleans up, and is safe to call more than once.
func (w *Worktree) Close() error {
	w.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		spec := runSpec{dir: w.repo.mirror, env: mirrorEnv(w.repo.mirror), timeout: cleanupTimeout}
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
		w.repo.mu.Lock()
		delete(w.repo.worktrees, w)
		w.repo.mu.Unlock()
		w.err = errors.Join(errs...)
	})
	return w.err
}

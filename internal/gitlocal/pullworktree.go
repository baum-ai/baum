package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/baum-ai/baum/internal/privatefs"
)

// PullWorktreeOptions say which pull request AddPullWorktree checks out and
// where.
type PullWorktreeOptions struct {
	// Dir is the new worktree's directory: an absolute path outside the
	// clone that does not exist yet. Its parent is created, private.
	Dir    string
	Number int
	// HeadRef is the pull request's head branch. Fork is set when the head
	// branch is in another repository, so the clone's remote has the head
	// only as refs/pull/<Number>/head.
	HeadRef string
	Fork    bool
	// Suffix names the branch used when every other name is checked out
	// elsewhere: baum/pr-<Number>-<Suffix>.
	Suffix string
}

// PullWorktree is the worktree AddPullWorktree added.
type PullWorktree struct {
	Dir    string
	Branch string
	// NewBranch is set when the branch was created for it; otherwise an
	// existing branch that no worktree had checked out was checked out as
	// it was.
	NewBranch bool
}

var suffixPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

// PullBranch is the branch name baum gives a pull request whose head branch
// cannot be used under its own name: a fork's, or one checked out elsewhere.
func PullBranch(number int) string { return fmt.Sprintf("baum/pr-%d", number) }

// AddPullWorktree adds a linked worktree of the clone at opts.Dir on a local
// branch for the pull request, for a person's agent session to work in. It
// fetches the head from the Repo's remote into refs/baum/pull/<N>/head (the
// head branch, or refs/pull/<N>/head for a fork), then takes the first branch
// name that no worktree has checked out: the head branch's own name (not for
// a fork, whose branch name may be anything, including the base's), then
// PullBranch, then PullBranch with opts.Suffix. A name that exists already is
// checked out as it is, never moved, since it may hold the person's work. A
// new one starts at the fetched head and tracks the head on the remote:
// branch.<name>.remote and branch.<name>.merge, refs/heads/<head> or, for a
// fork, refs/pull/<N>/head, so git pull brings the pull request's new
// commits.
//
// No other worktree's HEAD, index or files change, and no branch but the new
// one is written. Commands run with the user's configuration, as a fetch does,
// so filters and credentials work, but with no hooks: core.hooksPath names
// nothing, so a post-checkout hook from the clone or from a hooks directory
// in the pull request's own files does not run.
func (r *Repo) AddPullWorktree(ctx context.Context, opts PullWorktreeOptions) (PullWorktree, error) {
	if opts.Number <= 0 || !filepath.IsAbs(opts.Dir) || !suffixPattern.MatchString(opts.Suffix) || (!opts.Fork && !validRefName(opts.HeadRef)) {
		return PullWorktree{}, ErrInvalidInput
	}
	if inside(r.dir, opts.Dir) || inside(r.common, opts.Dir) {
		return PullWorktree{}, fmt.Errorf("%w: the worktree directory is inside the repository", ErrInvalidInput)
	}
	if _, err := os.Lstat(opts.Dir); err == nil {
		return PullWorktree{}, fmt.Errorf("%w: the worktree directory exists already", ErrInvalidInput)
	}
	done, err := r.begin()
	if err != nil {
		return PullWorktree{}, err
	}
	defer done()
	fetched := fmt.Sprintf("refs/baum/pull/%d/head", opts.Number)
	merge := "refs/heads/" + opts.HeadRef
	if opts.Fork {
		merge = fmt.Sprintf("refs/pull/%d/head", opts.Number)
	}
	if err := r.fetch(ctx, r.opts.Remote, []string{"+" + merge + ":" + fetched}); err != nil {
		return PullWorktree{}, err
	}
	head, err := r.verifyCommit(ctx, fetched)
	if err != nil {
		return PullWorktree{}, err
	}
	worktrees, err := Worktrees(ctx, r.dir)
	if err != nil {
		return PullWorktree{}, err
	}
	taken := map[string]bool{}
	for _, w := range worktrees {
		if w.Branch != "" {
			taken[w.Branch] = true
		}
	}
	var names []string
	if !opts.Fork {
		names = append(names, opts.HeadRef)
	}
	names = append(names, PullBranch(opts.Number), PullBranch(opts.Number)+"-"+opts.Suffix)
	if err := privatefs.MkdirAll(filepath.Dir(opts.Dir)); err != nil {
		return PullWorktree{}, err
	}
	for _, name := range names {
		if taken[name] {
			continue
		}
		exists, err := r.branchExists(ctx, name)
		if err != nil {
			return PullWorktree{}, err
		}
		if exists {
			if err := r.inClone(ctx, "worktree", "add", "--", opts.Dir, name); err != nil {
				return PullWorktree{}, err
			}
			return PullWorktree{Dir: opts.Dir, Branch: name}, nil
		}
		if err := r.inClone(ctx, "worktree", "add", "-b", name, "--", opts.Dir, head); err != nil {
			return PullWorktree{}, err
		}
		// The worktree is there whether or not tracking can be set up, so a
		// failure here is returned with it.
		added := PullWorktree{Dir: opts.Dir, Branch: name, NewBranch: true}
		if err := r.inClone(ctx, "config", "branch."+name+".remote", r.opts.Remote); err != nil {
			return added, fmt.Errorf("setting the branch's upstream: %w", err)
		}
		if err := r.inClone(ctx, "config", "branch."+name+".merge", merge); err != nil {
			return added, fmt.Errorf("setting the branch's upstream: %w", err)
		}
		return added, nil
	}
	return PullWorktree{}, errors.New("every branch name for the pull request is checked out in another worktree")
}

// inClone runs a command in the clone with the user's configuration and no
// hooks.
func (r *Repo) inClone(ctx context.Context, args ...string) error {
	_, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), timeout: r.opts.CommandTimeout,
		config: []string{"core.hooksPath=" + os.DevNull, "protocol.ext.allow=never"}}, args...)
	return err
}

// verifyCommit returns the commit ref points at in the clone.
func (r *Repo) verifyCommit(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: r.opts.CommandTimeout},
		"rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", fmt.Errorf("%w: %s", ErrObjectNotFound, ref)
	}
	return oid, nil
}

// branchExists reports whether the clone has a local branch of this name.
func (r *Repo) branchExists(ctx context.Context, name string) (bool, error) {
	_, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: r.opts.CommandTimeout},
		"rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+name)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

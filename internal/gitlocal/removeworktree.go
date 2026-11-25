package gitlocal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// maxStatusBytes bounds the output of git status in WorktreeChanges.
const maxStatusBytes = 4 << 20

// maxChangesListed bounds the paths WorktreeChanges names.
const maxChangesListed = 20

// Changes is the work in a worktree that removing it would lose.
type Changes struct {
	// Count is how many paths git status reports changed, staged or
	// untracked (ignored ones are not counted); Paths names the first of
	// them, at most maxChangesListed.
	Count int
	Paths []string
	// Detached counts the commits HEAD has that no branch, tag,
	// remote-tracking ref or ref under refs/baum/ has: commits made on a
	// detached HEAD, which only the worktree's HEAD holds.
	Detached int
}

// WorktreeChanges reads the work in the worktree at dir: every changed,
// staged or untracked path, untracked directories listed file by file,
// ignored files left out, and the commits only its HEAD holds. It runs git
// with the user's configuration, as the person running it there would, but
// never in a repository above dir: a worktree whose .git file is gone is an
// error, not the parent repository's status.
func WorktreeChanges(ctx context.Context, dir string) (Changes, error) {
	spec := runSpec{dir: dir, env: append(userEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(filepath.Clean(dir))),
		limit: maxStatusBytes, timeout: DefaultCommandTimeout, config: []string{"core.hooksPath=" + os.DevNull}}
	out, err := run(ctx, spec, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return Changes{}, err
	}
	var changes Changes
	entries := bytes.Split(out, []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		// A rename or copy is followed by its source path, which is part of
		// the same change.
		if entry[0] == 'R' || entry[0] == 'C' {
			i++
		}
		changes.Count++
		if len(changes.Paths) < maxChangesListed {
			changes.Paths = append(changes.Paths, string(entry[3:]))
		}
	}
	spec.limit = maxOIDOutputBytes
	out, err = run(ctx, spec, "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes", "--glob=refs/baum/")
	if err != nil {
		return Changes{}, err
	}
	if changes.Detached, err = strconv.Atoi(strings.TrimSpace(string(out))); err != nil {
		return Changes{}, err
	}
	return changes, nil
}

// UnpushedCommits counts the commits on the clone's local branch that no
// remote-tracking ref has and no ref baum fetched a pull request's head into
// (refs/baum/): the commits deleting the branch would lose. A branch that
// does not exist has none.
func (r *Repo) UnpushedCommits(ctx context.Context, branch string) (int, error) {
	if !validRefName(branch) {
		return 0, ErrInvalidInput
	}
	at, err := r.branchCommit(ctx, branch)
	if err != nil || at == "" {
		return 0, err
	}
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: r.opts.CommandTimeout},
		"rev-list", "--count", at, "--not", "--remotes", "--glob=refs/baum/")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// RemoveWorktree removes the clone's linked worktree at dir, whatever it
// holds: its changes and untracked files go with it. For a worktree whose
// directory is gone, only its own bookkeeping entry is removed: no other
// worktree's, as git worktree prune would (one on a drive not mounted now is
// still the person's). The clone's main worktree is never removed.
func (r *Repo) RemoveWorktree(ctx context.Context, dir string) error {
	done, err := r.begin()
	if err != nil {
		return err
	}
	defer done()
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		entry, ok := worktreeEntries(r.common)[canonical(filepath.Join(dir, ".git"))]
		if !ok {
			return nil
		}
		return os.RemoveAll(entry)
	}
	// Twice forced: a locked worktree goes too.
	return r.inClone(ctx, "worktree", "remove", "--force", "--force", "--", dir)
}

// DeleteBranch deletes the clone's local branch, and its configuration, even
// when it is not merged. A branch a worktree has checked out is refused by
// git; one that does not exist is no error.
func (r *Repo) DeleteBranch(ctx context.Context, branch string) error {
	if !validRefName(branch) {
		return ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return err
	}
	defer done()
	at, err := r.branchCommit(ctx, branch)
	if err != nil || at == "" {
		return err
	}
	return r.inClone(ctx, "branch", "-D", "--", branch)
}

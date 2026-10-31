package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// maxConflictOutputBytes bounds the output of the merge, which lists every
// conflicted path.
const maxConflictOutputBytes = 1 << 20

// ErrMergeTreeUnsupported: this git cannot merge two commits without a
// working tree (git 2.38 added merge-tree --write-tree).
var ErrMergeTreeUnsupported = errors.New("this git is too old to find merge conflicts: it needs git 2.38 or newer")

// Conflicts merges the pull request's head into the tip of its base branch
// in memory, as GitHub's merge would, and lists the files that conflict. The
// head is fetched from refs/pull/<N>/head when missing, and the base branch
// tip always, as it moves. remote "" is Options.Remote. It returns the base
// commit the answer is for, and no paths when the merge is clean. Nothing is
// written to the clone's working tree or branches.
func (r *Repo) Conflicts(ctx context.Context, remote string, number int, headSHA, baseRef string) (baseSHA string, paths []string, err error) {
	if remote == "" {
		remote = r.opts.Remote
	}
	baseRef = strings.TrimPrefix(baseRef, "refs/heads/")
	if number <= 0 || !validOID(headSHA) || !validRefName(baseRef) || !validRemoteName(remote) {
		return "", nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return "", nil, err
	}
	defer done()
	headRef := fmt.Sprintf("refs/baum/pull/%d/head", number)
	baseTip := fmt.Sprintf("refs/baum/pull/%d/base", number)
	refspecs := []string{"+refs/heads/" + baseRef + ":" + baseTip}
	if !r.hasCommit(ctx, headSHA) {
		refspecs = append(refspecs, fmt.Sprintf("+refs/pull/%d/head:%s", number, headRef))
	}
	if err := r.fetch(ctx, remote, refspecs); err != nil {
		return "", nil, err
	}
	if !r.hasCommit(ctx, headSHA) {
		return "", nil, fmt.Errorf("%w: head commit %s is not available from %s", ErrObjectNotFound, headSHA, sanitize([]byte(remote)))
	}
	// The fetch wrote the clone's refs, which the private mirror reads
	// through its alternates by id only.
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: r.opts.CommandTimeout},
		"rev-parse", "--verify", "--end-of-options", baseTip+"^{commit}")
	if err != nil {
		return "", nil, err
	}
	baseSHA = strings.TrimSpace(string(out))
	if !validOID(baseSHA) {
		return "", nil, fmt.Errorf("%w: %s", ErrObjectNotFound, baseTip)
	}
	out, err = run(ctx, runSpec{dir: r.mirror, env: mirrorEnv(r.mirror), limit: maxConflictOutputBytes, timeout: r.opts.CommandTimeout, keepOutput: true},
		"merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", baseSHA, headSHA)
	if err == nil {
		return baseSHA, nil, nil
	}
	if gitErr, ok := errors.AsType[*Error](err); ok {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
			return baseSHA, conflictPaths(out), nil
		}
		// git before 2.38 does not know --write-tree.
		if strings.Contains(gitErr.Stderr, "unknown option") || strings.Contains(gitErr.Stderr, "usage:") {
			return "", nil, ErrMergeTreeUnsupported
		}
	}
	return "", nil, r.missingObjects(err)
}

// conflictPaths reads the output of merge-tree -z --name-only: the merged
// tree's id, then each conflicted path once, every one ended by a NUL. A
// path with several conflicting stages is listed once.
func conflictPaths(out []byte) []string {
	fields := bytes.Split(out, []byte{0})
	var paths []string
	seen := map[string]bool{}
	// The first field is the tree.
	for _, field := range fields[min(1, len(fields)):] {
		path := strings.ToValidUTF8(string(field), "")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

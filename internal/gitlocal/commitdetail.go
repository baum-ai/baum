package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

// maxCommitMessageBytes bounds the message CommitChanges reads. A larger one
// is bundle.ErrOutputTooLarge rather than read in part.
const maxCommitMessageBytes = 1 << 20

// CommitChanges is one commit's message and the files it changed against its
// first parent, or against the empty tree for a root commit.
type CommitChanges struct {
	OID string
	// Parent is the commit's first parent; "" for a root commit.
	Parent string
	// Message is the whole commit message, as git prints %B without its final
	// newline.
	Message string
	// Files are the changed files with rename detection (-M, -l1000), each with
	// its added and deleted line counts (nil for a binary file).
	Files []bundle.FileChange
}

// CommitChanges reads commit oid's message and the files it changed, diffed
// against its first parent (a merge commit too) or, for a root commit, the
// empty tree. It reads only objects the clone holds and never fetches:
// objects it lacks are ErrObjectNotFound, or ErrPartialClone in a partial
// clone. A message over 1 MiB or a file list over MaxComparisonBytes is
// bundle.ErrOutputTooLarge.
func (r *Repo) CommitChanges(ctx context.Context, oid string) (CommitChanges, error) {
	if !validOID(oid) {
		return CommitChanges{}, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return CommitChanges{}, err
	}
	defer done()
	if !r.hasCommit(ctx, oid) {
		return CommitChanges{}, fmt.Errorf("%w: commit %s is not in this clone", ErrObjectNotFound, oid)
	}
	out, err := r.read(ctx, maxCommitMessageBytes, "log", "-1", "--no-color", "--format=%P"+commitFieldSep+"%B", oid, "--")
	if err != nil {
		return CommitChanges{}, fmt.Errorf("reading commit: %w", rangeError(err))
	}
	parents, message, ok := strings.Cut(string(out), commitFieldSep)
	if !ok {
		return CommitChanges{}, errors.New("reading commit: unexpected git log output")
	}
	c := CommitChanges{OID: oid, Message: strings.TrimRight(message, "\n")}
	if fields := strings.Fields(parents); len(fields) > 0 {
		if !validOID(fields[0]) {
			return CommitChanges{}, errors.New("reading commit: unexpected git log output")
		}
		c.Parent = fields[0]
	}
	limit := r.opts.MaxComparisonBytes
	raw, err := r.read(ctx, limit, commitDiffArgs(c, "--raw")...)
	if err != nil {
		return CommitChanges{}, fmt.Errorf("listing the commit's files: %w", rangeError(err))
	}
	files, err := parseRawDiff(raw)
	if err != nil {
		return CommitChanges{}, fmt.Errorf("listing the commit's files: %w", err)
	}
	numstat, err := r.read(ctx, limit, commitDiffArgs(c, "--numstat")...)
	if err != nil {
		return CommitChanges{}, fmt.Errorf("reading the commit's statistics: %w", rangeError(err))
	}
	applyNumstat(files, numstat)
	c.Files = files
	return c, nil
}

// commitDiffArgs is diff-tree over a commit's change with format, against its
// first parent or, for a root commit, the empty tree.
func commitDiffArgs(c CommitChanges, format string) []string {
	args := []string{"diff-tree", "-r", "--no-commit-id", format, "-z", "-M", "-l1000"}
	if c.Parent == "" {
		return append(args, "--root", c.OID)
	}
	return append(args, c.Parent, c.OID)
}

// CommitFilePatch is the diff of one file, or of a renamed file's old and new
// path, in commit oid against parent (its first parent; "" for a root
// commit, which is diffed against the empty tree). It is read as FilePatch
// reads a range's file: three lines of context, the myers algorithm, -M and
// -l1000 with a literal pathspec for each path, no external diff driver, text
// conversion or colour, and git's file header lines included. The patch is
// at most maxBytes (MaxPatchBytes when zero), else bundle.ErrOutputTooLarge.
func (r *Repo) CommitFilePatch(ctx context.Context, oid, parent string, maxBytes int64, paths ...string) ([]byte, error) {
	if !validOID(oid) || (parent != "" && !validOID(parent)) || len(paths) == 0 || len(paths) > 2 {
		return nil, ErrInvalidInput
	}
	for _, p := range paths {
		if !validGitPath(p) {
			return nil, ErrInvalidInput
		}
	}
	if maxBytes <= 0 {
		maxBytes = r.opts.MaxPatchBytes
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	args := []string{"diff-tree", "-r", "-p", "--no-commit-id", "--no-ext-diff", "--no-textconv", "--no-color",
		"--unified=3", "--diff-algorithm=myers", "-M", "-l1000"}
	if parent == "" {
		args = append(args, "--root", oid)
	} else {
		args = append(args, parent, oid)
	}
	args = append(args, "--")
	for _, p := range paths {
		args = append(args, ":(literal)"+p)
	}
	out, err := r.read(ctx, maxBytes, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the commit's file patch: %w", rangeError(err))
	}
	return out, nil
}

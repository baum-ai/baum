package gitlocal

import (
	"context"
	"fmt"
)

// ChangedFile is one file a range changes, as git lists it with rename
// detection.
type ChangedFile struct {
	// Path is the file's path at the head (at the base for a deletion).
	Path string
	// OldPath is the path the file had at the base, when the range renamed it.
	OldPath string
	// Submodule is a path that is a submodule (a gitlink) on either side.
	Submodule bool
	// TypeChanged is a path whose type changed, a file to a symlink say.
	TypeChanged bool
}

const gitlinkMode = "160000"

// ChangedFiles lists the files that differ between two commits, with the
// rename detection a pull request page uses (-M, with -l1000 bounding the
// work), by the new path, or by the path a deleted file had. It reads only objects the clone holds and never
// fetches: objects it lacks are ErrObjectNotFound, or ErrPartialClone in a
// partial clone. A list over MaxComparisonBytes is bundle.ErrOutputTooLarge.
func (r *Repo) ChangedFiles(ctx context.Context, baseOID, headOID string) (map[string]ChangedFile, error) {
	if !validOID(baseOID) || !validOID(headOID) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := r.read(ctx, r.opts.MaxComparisonBytes, "diff", "--no-ext-diff", "--no-textconv", "--no-color",
		"--raw", "-z", "-M", "-l1000", baseOID, headOID)
	if err != nil {
		return nil, fmt.Errorf("listing changed files: %w", rangeError(err))
	}
	changes, err := parseRawDiff(out)
	if err != nil {
		return nil, fmt.Errorf("listing changed files: %w", err)
	}
	files := make(map[string]ChangedFile, len(changes))
	for _, change := range changes {
		file := ChangedFile{
			Path: change.NewPath, OldPath: change.OldPath,
			Submodule:   change.OldMode == gitlinkMode || change.NewMode == gitlinkMode,
			TypeChanged: change.Status == "type_changed",
		}
		if change.NewPath == "" { // a deletion is listed by the path it had
			file.Path, file.OldPath = change.OldPath, ""
		}
		if change.Status == "added" || change.OldPath == change.NewPath {
			file.OldPath = ""
		}
		files[file.Path] = file
	}
	return files, nil
}

// FilePatch is the diff of one file, or of a renamed file's old and new path,
// between two commits, read the way a pull request page reads it: -M and
// -l1000 with a literal pathspec for each path, so a rename is detected when
// both names are given. It is git's own output, file header lines included,
// with three lines of context and the myers algorithm fixed rather than read
// from configuration, and no external diff driver, text conversion or colour.
// A binary file has no "@@" hunks. The patch is at most maxBytes
// (MaxPatchBytes when zero), else bundle.ErrOutputTooLarge. It reads only
// objects the clone holds and never fetches: objects it lacks are
// ErrObjectNotFound, or ErrPartialClone in a partial clone.
func (r *Repo) FilePatch(ctx context.Context, baseOID, headOID string, maxBytes int64, paths ...string) ([]byte, error) {
	if !validOID(baseOID) || !validOID(headOID) || len(paths) == 0 || len(paths) > 2 {
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
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3", "--diff-algorithm=myers",
		"-M", "-l1000", baseOID, headOID, "--"}
	for _, p := range paths {
		args = append(args, ":(literal)"+p)
	}
	out, err := r.read(ctx, maxBytes, args...)
	if err != nil {
		return nil, fmt.Errorf("reading file patch: %w", rangeError(err))
	}
	return out, nil
}

package gitlocal

import (
	"context"
	"fmt"
	"strings"
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
}

const gitlinkMode = "160000"

// ChangedFiles lists the files that differ between two commits, with the
// rename detection a pull request page uses (-M, with -l1000 bounding the
// work), by the new path. It reads only objects the clone holds and never
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
	files := map[string]ChangedFile{}
	tokens := strings.Split(string(out), "\x00")
	for i := 0; i < len(tokens); i++ {
		meta, ok := strings.CutPrefix(tokens[i], ":")
		if !ok {
			continue
		}
		// ":<old mode> <new mode> <old id> <new id> <status>", then the path,
		// or the old and the new path for a rename or a copy.
		fields := strings.Fields(meta)
		if len(fields) != 5 || i+1 >= len(tokens) {
			return nil, fmt.Errorf("listing changed files: unexpected git diff output")
		}
		file := ChangedFile{Submodule: fields[0] == gitlinkMode || fields[1] == gitlinkMode}
		i++
		file.Path = tokens[i]
		if strings.HasPrefix(fields[4], "R") || strings.HasPrefix(fields[4], "C") {
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("listing changed files: unexpected git diff output")
			}
			i++
			file.OldPath, file.Path = file.Path, tokens[i]
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

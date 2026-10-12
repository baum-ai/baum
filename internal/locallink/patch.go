package locallink

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lastpersonlabs/baum/bundle"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
)

// MaxPatchBytes is the largest file patch Patch returns. A larger one is
// unavailable (CodeTooLarge) rather than sent in part.
const MaxPatchBytes = 1 << 20

// Codes a PatchResult can carry, besides those the sessions answer shares.
const (
	// CodeNoTextPatch is a file whose change has no text hunks: a binary file,
	// or a change of mode alone.
	CodeNoTextPatch = "no_text_patch"
	// CodeUnchanged is a path the range does not change.
	CodeUnchanged = "unchanged"
	// CodeTooLarge is a patch over MaxPatchBytes.
	CodeTooLarge = "too_large"
)

// PatchRequest says which file's patch to read and where.
type PatchRequest struct {
	// Dir is a directory inside the clone; "" means the current directory.
	Dir string
	// Repository is the pull request's "owner/name".
	Repository string
	// Head, Base and MergeBase bound the pull request. The patch is from the
	// merge base to the head, or from the base when the document records no
	// merge base. A merge base the clone lacks makes the patch unavailable:
	// the base would give a different diff.
	Head, Base, MergeBase string
	// Path is the repository-relative path of the file.
	Path string
}

// PatchResult is the outcome of Patch.
type PatchResult struct {
	// Available is true when Patch holds a text patch.
	Available bool
	// Patch is the file's unified diff from its first "@@" hunk header on,
	// with git's "diff --git", "index", "---" and "+++" lines left out: the
	// form the hosted app's patches have.
	Patch string
	// Code says why Patch is empty, when it is.
	Code string
}

// Stable reports whether the answer holds until the document changes: a patch,
// or a file that has no text patch. The other unavailable answers (not a
// clone, no matching remote, missing commits) can change when a person fixes
// the clone, so they are asked again.
func (r PatchResult) Stable() bool {
	switch r.Code {
	case "", CodeNoTextPatch, CodeUnchanged, CodeTooLarge:
		return true
	}
	return false
}

func noPatch(code string) (PatchResult, error) { return PatchResult{Code: code}, nil }

// Patch reads one file's patch for a pull request from the clone containing
// req.Dir, once the clone is matched to the pull request's repository as
// Resolve matches it. It never fetches. A patch that cannot be read from this
// machine is an unavailable PatchResult, not an error; an error is a failure
// to read something that should have been readable.
func Patch(ctx context.Context, req PatchRequest) (PatchResult, error) {
	start := req.MergeBase
	if start == "" {
		start = req.Base
	}
	if req.Repository == "" || req.Head == "" || start == "" {
		return noPatch(CodeNoHead)
	}
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{MaxPatchBytes: MaxPatchBytes})
	if err != nil {
		var gitErr *gitlocal.Error
		if errors.As(err, &gitErr) && gitErr.Command == "rev-parse" {
			return noPatch(CodeNotAClone)
		}
		return PatchResult{}, fmt.Errorf("opening the clone: %w", err)
	}
	defer func() { _ = repo.Close() }()

	remotes, err := repo.RemoteURLs(ctx)
	if err != nil {
		return PatchResult{}, fmt.Errorf("reading the clone's remotes: %w", err)
	}
	if !anyRemoteIs(remotes, req.Repository) {
		return noPatch(CodeRemoteMismatch)
	}
	var oids [2]string
	for i, id := range []string{start, req.Head} {
		oids[i], err = repo.ResolveCommit(ctx, id)
		switch {
		case err == nil:
		case errors.Is(err, gitlocal.ErrObjectNotFound):
			return noPatch(CodeCommitsMissing)
		case errors.Is(err, gitlocal.ErrAmbiguous):
			return noPatch(CodeAmbiguousCommit)
		case errors.Is(err, gitlocal.ErrInvalidInput):
			return noPatch(CodeNoHead)
		default:
			return PatchResult{}, fmt.Errorf("resolving commit %s: %w", id, err)
		}
	}
	out, err := repo.FilePatch(ctx, oids[0], oids[1], req.Path, MaxPatchBytes)
	switch {
	case errors.Is(err, bundle.ErrOutputTooLarge):
		return noPatch(CodeTooLarge)
	case errors.Is(err, gitlocal.ErrPartialClone):
		return noPatch(CodePartialClone)
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		return noPatch(CodeCommitsMissing)
	case errors.Is(err, gitlocal.ErrInvalidInput):
		return noPatch(CodeError)
	case err != nil:
		return PatchResult{}, err
	}
	if len(out) == 0 {
		return noPatch(CodeUnchanged)
	}
	text := string(out)
	if !strings.HasPrefix(text, "@@ ") {
		i := strings.Index(text, "\n@@ ")
		if i < 0 {
			return noPatch(CodeNoTextPatch)
		}
		text = text[i+1:]
	}
	return PatchResult{Available: true, Patch: text}, nil
}

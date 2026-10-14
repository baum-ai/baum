package locallink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/baumai/baum/bundle"
	"github.com/baumai/baum/internal/gitlocal"
)

// MaxPatchBytes is the largest file patch Patch returns. A larger one is
// unavailable (CodeTooLarge) rather than sent in part.
const MaxPatchBytes = 1 << 20

// Codes a PatchResult can carry, besides those the sessions answer shares.
const (
	// CodeNoTextPatch is a file whose change has no text hunks (a binary file,
	// or a change of mode alone), a submodule, or a diff that is not one file's.
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
	// Path is the repository-relative path of the file at the head.
	Path string
}

// PatchResult is the outcome of Patch.
type PatchResult struct {
	// Available is true when Patch holds a text patch.
	Available bool
	// Patch is the file's diff from its first "@@" hunk header on. git's header
	// lines before it ("diff --git", "index", the rename lines, "---" and
	// "+++") are left out; the viewer reads only the hunks.
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

// Patcher reads file patches for a pull request. It finds the files the
// pull request changes, and so the old name of each renamed one, once for a
// merge base and head, and keeps that while the answer holds; the zero value is
// ready. It is safe for concurrent use.
type Patcher struct {
	mu      sync.Mutex
	changes *changeSet
}

// changeSet is one computation of a range's changed files, shared by every
// request that arrives while it runs and, when it succeeded, by later ones.
type changeSet struct {
	key   string
	done  chan struct{}
	files map[string]gitlocal.ChangedFile
	err   error
}

// Patch is Patcher.Patch with nothing kept between calls.
func Patch(ctx context.Context, req PatchRequest) (PatchResult, error) {
	return new(Patcher).Patch(ctx, req)
}

// Patch reads one file's patch for a pull request from the clone containing
// req.Dir, once the clone is matched to the pull request's repository as
// Resolve matches it. It never fetches. A patch that cannot be read from this
// machine is an unavailable PatchResult, not an error; an error is a failure
// to read something that should have been readable.
//
// The diff is the one the hosted service reads: -M and -l1000 over the file's
// path, and over its old path too when the range renamed it, so a renamed file
// shows only what changed in it. A path the range does not change (a directory
// is one) is unchanged, and a submodule or a diff that is
// not one file's (a type change has two sections of its path) has no
// text patch.
func (p *Patcher) Patch(ctx context.Context, req PatchRequest) (PatchResult, error) {
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
	files, err := p.changedFiles(ctx, oids[0], oids[1], func(ctx context.Context) (map[string]gitlocal.ChangedFile, error) {
		return repo.ChangedFiles(ctx, oids[0], oids[1])
	})
	if result, handled, err := unreadable(err); handled {
		return result, err
	}
	// Only a path the range changes has a patch. Any other, a directory with
	// one changed file in it included, is unchanged: its diff would be that of
	// the files under it.
	change, changed := files[req.Path]
	if !changed {
		return noPatch(CodeUnchanged)
	}
	if change.Submodule {
		return noPatch(CodeNoTextPatch)
	}
	paths := []string{req.Path}
	if change.OldPath != "" && change.OldPath != req.Path {
		paths = []string{change.OldPath, req.Path}
	}
	out, err := repo.FilePatch(ctx, oids[0], oids[1], MaxPatchBytes, paths...)
	if result, handled, err := unreadable(err); handled {
		return result, err
	}
	return patchOf(string(out), change.TypeChanged)
}

// unreadable turns a failure to read objects into the unavailable answer it
// means. handled is false for no error at all.
func unreadable(err error) (result PatchResult, handled bool, _ error) {
	switch {
	case err == nil:
		return PatchResult{}, false, nil
	case errors.Is(err, bundle.ErrOutputTooLarge):
		result, err = noPatch(CodeTooLarge)
	case errors.Is(err, gitlocal.ErrPartialClone):
		result, err = noPatch(CodePartialClone)
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		result, err = noPatch(CodeCommitsMissing)
	case errors.Is(err, gitlocal.ErrInvalidInput):
		result, err = noPatch(CodeError)
	default:
		return PatchResult{}, true, err
	}
	return result, true, err
}

// patchOf cuts git's output down to a patch. typeChanged says the file changed
// type (a file to a symlink, say), which git diffs as a deletion and an
// addition of the same path, as the hosted service shows it.
func patchOf(out string, typeChanged bool) (PatchResult, error) {
	if out == "" {
		return noPatch(CodeUnchanged)
	}
	// One file's diff, a rename's old and new name included, has one
	// "diff --git" line, and a type change two for the same path. Any more is
	// several files, which is no file's patch.
	headers := strings.Split("\n"+out, "\ndiff --git ")[1:]
	for i := range headers {
		headers[i], _, _ = strings.Cut(headers[i], "\n")
	}
	oneFile := len(headers) == 1 || typeChanged && len(headers) == 2 && headers[0] == headers[1]
	if !strings.HasPrefix(out, "diff --git ") || !oneFile {
		return noPatch(CodeNoTextPatch)
	}
	// Each section's header lines go, so that a second section's "---" line is
	// not read as a removed line.
	var patch strings.Builder
	for _, section := range strings.Split("\n"+out, "\ndiff --git ")[1:] {
		if i := strings.Index("\n"+section, "\n@@ "); i >= 0 {
			patch.WriteString(strings.TrimSuffix(section[i:], "\n"))
			patch.WriteString("\n")
		}
	}
	if patch.Len() == 0 {
		return noPatch(CodeNoTextPatch)
	}
	return PatchResult{Available: true, Patch: patch.String()}, nil
}

// changedFiles is the range's changed files, found once and kept while the
// commits stay the same. A failure is not kept.
func (p *Patcher) changedFiles(ctx context.Context, base, head string, load func(context.Context) (map[string]gitlocal.ChangedFile, error)) (map[string]gitlocal.ChangedFile, error) {
	key := base + ".." + head
	p.mu.Lock()
	set := p.changes
	if set == nil || set.key != key {
		set = &changeSet{key: key, done: make(chan struct{})}
		p.changes = set
		p.mu.Unlock()
		set.files, set.err = load(ctx)
		if set.err != nil {
			p.mu.Lock()
			if p.changes == set {
				p.changes = nil
			}
			p.mu.Unlock()
		}
		close(set.done)
		return set.files, set.err
	}
	p.mu.Unlock()
	select {
	case <-set.done:
		if set.err != nil {
			// The computation that failed was another request's: ask again.
			return p.changedFiles(ctx, base, head, load)
		}
		return set.files, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

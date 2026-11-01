package locallink

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
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

// maxChangeSets bounds the ranges whose changed files a Patcher keeps, so
// the versions of a pull request, each its own range, can be read in turn.
const maxChangeSets = 8

// patchRedactor masks every patch Patch returns, with the configuration the
// bundle masks stored hunks with.
var patchRedactor = sync.OnceValues(bundle.NewRedactor)

// rangeIdle is how long a Patcher keeps a range's opened clone after its
// last read, and rangeMaxAge how long after it was opened, however busy:
// the next read after either opens and matches the clone again, so a remote
// edited in the meantime is seen. Tests shorten them.
var (
	rangeIdle   = 30 * time.Second
	rangeMaxAge = 5 * time.Minute
)

// Patcher reads file patches for pull requests. It finds the files a range
// changes, and so the old name of each renamed one, once for a merge base
// and head, and keeps that for the last few ranges while the answer holds.
// It also keeps, for the last few clones and ranges it read, the opened clone,
// its matched remote and the range's resolved commits, for rangeIdle after
// the last read, so a further file of a known range costs one git diff.
// Requests for the same patch of the same clone that arrive while one is
// read share that read, whichever document or version asked. The zero value
// is ready. It is safe for concurrent use; Close releases what it keeps.
type Patcher struct {
	mu      sync.Mutex
	changes []*changeSet
	ranges  []*openRange
	reads   map[string]*patchRead
	// read reads one patch; nil is Patcher.read. Tests count reads with it.
	read func(context.Context, PatchRequest) (PatchResult, error)
}

// openRange is a clone opened and matched for one repository, with one
// range's commits resolved. Only a resolution that succeeded is kept; an
// unavailable answer (no clone, no matching remote, missing commits) is
// computed again on the next request.
type openRange struct {
	key   string
	ready chan struct{}
	repo  *gitlocal.Repo
	oids  [2]string
	// result and err are a resolution's unavailable answer or failure; repo is
	// nil then.
	result PatchResult
	err    error
	// users counts the reads using repo; a range dropped while in use is
	// closed by its last user.
	users   int
	dropped bool
	idle    *time.Timer
	// opened is when the resolution succeeded; zero before.
	opened time.Time
}

// changeSet is one computation of a range's changed files, shared by every
// request that arrives while it runs and, when it succeeded, by later ones.
type changeSet struct {
	key   string
	done  chan struct{}
	files map[string]gitlocal.ChangedFile
	err   error
}

// patchRead is one read of a patch, shared by the requests that arrive
// while it runs.
type patchRead struct {
	done   chan struct{}
	result PatchResult
	err    error
}

// Patch is Patcher.Patch with nothing kept between calls.
func Patch(ctx context.Context, req PatchRequest) (PatchResult, error) {
	p := new(Patcher)
	defer p.Close()
	return p.Patch(ctx, req)
}

// Close closes every clone the Patcher keeps open, once the reads using it
// end. The Patcher stays usable: a later read opens the clone again.
func (p *Patcher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	ranges := p.ranges
	p.ranges = nil
	var idle []*openRange
	for _, r := range ranges {
		r.dropped = true
		if r.idle != nil {
			r.idle.Stop()
		}
		if r.users == 0 && r.repo != nil {
			idle = append(idle, r)
		}
	}
	p.mu.Unlock()
	for _, r := range idle {
		_ = r.repo.Close()
	}
}

// openRange returns the kept or newly resolved range for req, with a release
// to call once the read is done, or the unavailable answer or failure of
// resolving it, which is not kept.
func (p *Patcher) openRange(ctx context.Context, req PatchRequest, start string) (*openRange, func(), PatchResult, error) {
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	key := strings.Join([]string{dir, req.Repository, start, req.Head}, "\x00")
	for {
		p.mu.Lock()
		var r *openRange
		var evicted []*openRange
		for i, kept := range p.ranges {
			if kept.key != key {
				continue
			}
			p.ranges = slices.Delete(p.ranges, i, i+1)
			if !kept.opened.IsZero() && time.Since(kept.opened) >= rangeMaxAge {
				// Kept long enough: this read opens the clone again.
				if kept.drop() {
					evicted = append(evicted, kept)
				}
			} else {
				r = kept
				p.ranges = append(p.ranges, r)
			}
			break
		}
		if r == nil {
			r = &openRange{key: key, ready: make(chan struct{}), users: 1}
			for len(p.ranges) >= maxChangeSets {
				old := p.ranges[0]
				p.ranges = p.ranges[1:]
				if old.drop() {
					evicted = append(evicted, old)
				}
			}
			p.ranges = append(p.ranges, r)
			p.mu.Unlock()
			for _, old := range evicted {
				_ = old.repo.Close()
			}
			repo, oids, result, err := resolveRange(ctx, dir, req.Repository, start, req.Head)
			p.mu.Lock()
			r.repo, r.oids, r.result, r.err = repo, oids, result, err
			if repo != nil {
				r.opened = time.Now()
			}
			if repo == nil {
				r.users--
				r.dropped = true
				if i := slices.Index(p.ranges, r); i >= 0 {
					p.ranges = slices.Delete(p.ranges, i, i+1)
				}
			}
			p.mu.Unlock()
			close(r.ready)
			if r.repo == nil {
				return nil, nil, r.result, r.err
			}
			return r, func() { p.release(r) }, PatchResult{}, nil
		}
		r.users++
		if r.idle != nil {
			r.idle.Stop()
		}
		p.mu.Unlock()
		select {
		case <-r.ready:
		case <-ctx.Done():
			p.release(r)
			return nil, nil, PatchResult{}, ctx.Err()
		}
		if r.repo != nil {
			return r, func() { p.release(r) }, PatchResult{}, nil
		}
		// Another request's resolution failed or was unavailable; its answer
		// is not kept, so this one resolves again.
		p.mu.Lock()
		r.users--
		p.mu.Unlock()
	}
}

// drop takes r out of use and reports whether it is unused and open, so
// its caller closes it. p.mu is held.
func (r *openRange) drop() bool {
	r.dropped = true
	if r.idle != nil {
		r.idle.Stop()
	}
	return r.users == 0 && r.repo != nil
}

// forget drops r, once a read found its clone no longer answers for the
// range; the next request resolves it again.
func (p *Patcher) forget(r *openRange) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i := slices.Index(p.ranges, r); i >= 0 {
		p.ranges = slices.Delete(p.ranges, i, i+1)
	}
	r.dropped = true
}

// release ends one read of r. The last read closes a dropped range, or
// starts its idle time.
func (p *Patcher) release(r *openRange) {
	p.mu.Lock()
	r.users--
	closing := r.users == 0 && r.dropped && r.repo != nil
	if r.users == 0 && !r.dropped && r.repo != nil {
		if r.idle == nil {
			r.idle = time.AfterFunc(rangeIdle, func() { p.expire(r) })
		} else {
			r.idle.Reset(rangeIdle)
		}
	}
	p.mu.Unlock()
	if closing {
		_ = r.repo.Close()
	}
}

// expire closes r once it has been idle for rangeIdle.
func (p *Patcher) expire(r *openRange) {
	p.mu.Lock()
	if r.users > 0 || r.dropped {
		p.mu.Unlock()
		return
	}
	r.dropped = true
	if i := slices.Index(p.ranges, r); i >= 0 {
		p.ranges = slices.Delete(p.ranges, i, i+1)
	}
	p.mu.Unlock()
	_ = r.repo.Close()
}

// resolveRange opens the clone containing dir, matches its remotes to
// repository and resolves the range's commits. Without an error, either repo
// is the open clone or result is the unavailable answer.
func resolveRange(ctx context.Context, dir, repository, start, head string) (repo *gitlocal.Repo, oids [2]string, result PatchResult, err error) {
	repo, err = gitlocal.Open(ctx, dir, gitlocal.Options{MaxPatchBytes: MaxPatchBytes})
	if err != nil {
		var gitErr *gitlocal.Error
		if errors.As(err, &gitErr) && gitErr.Command == "rev-parse" {
			result, err = noPatch(CodeNotAClone)
			return nil, oids, result, err
		}
		return nil, oids, PatchResult{}, fmt.Errorf("opening the clone: %w", err)
	}
	fail := func(r PatchResult, e error) (*gitlocal.Repo, [2]string, PatchResult, error) {
		_ = repo.Close()
		return nil, oids, r, e
	}
	remotes, err := repo.RemoteURLs(ctx)
	if err != nil {
		return fail(PatchResult{}, fmt.Errorf("reading the clone's remotes: %w", err))
	}
	if !anyRemoteIs(remotes, repository) {
		return fail(noPatch(CodeRemoteMismatch))
	}
	for i, id := range []string{start, head} {
		oids[i], err = repo.ResolveCommit(ctx, id)
		switch {
		case err == nil:
		case errors.Is(err, gitlocal.ErrObjectNotFound):
			return fail(noPatch(CodeCommitsMissing))
		case errors.Is(err, gitlocal.ErrAmbiguous):
			return fail(noPatch(CodeAmbiguousCommit))
		case errors.Is(err, gitlocal.ErrInvalidInput):
			return fail(noPatch(CodeNoHead))
		default:
			return fail(PatchResult{}, fmt.Errorf("resolving commit %s: %w", id, err))
		}
	}
	return repo, oids, PatchResult{}, nil
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
// text patch. The patch is then masked as the bundle masks a stored hunk's
// text (bundle.RedactFilePatch), so it holds no secret a document would
// have hidden.
//
// Requests for the same clone, repository, range and path share a read that
// is running. A request whose shared read failed, its context ended
// included, reads again.
func (p *Patcher) Patch(ctx context.Context, req PatchRequest) (PatchResult, error) {
	start := req.MergeBase
	if start == "" {
		start = req.Base
	}
	key := strings.Join([]string{req.Dir, req.Repository, start, req.Head, req.Path}, "\x00")
	p.mu.Lock()
	if running := p.reads[key]; running != nil {
		p.mu.Unlock()
		select {
		case <-running.done:
			if running.err != nil && ctx.Err() == nil {
				return p.Patch(ctx, req)
			}
			return running.result, running.err
		case <-ctx.Done():
			return PatchResult{}, ctx.Err()
		}
	}
	if p.reads == nil {
		p.reads = map[string]*patchRead{}
	}
	running := &patchRead{done: make(chan struct{})}
	p.reads[key] = running
	read := p.read
	p.mu.Unlock()
	if read == nil {
		read = p.readPatch
	}
	running.result, running.err = read(ctx, req)
	p.mu.Lock()
	delete(p.reads, key)
	p.mu.Unlock()
	close(running.done)
	return running.result, running.err
}

func (p *Patcher) readPatch(ctx context.Context, req PatchRequest) (PatchResult, error) {
	start := req.MergeBase
	if start == "" {
		start = req.Base
	}
	if req.Repository == "" || req.Head == "" || start == "" {
		return noPatch(CodeNoHead)
	}
	r, release, result, err := p.openRange(ctx, req, start)
	if r == nil {
		return result, err
	}
	defer release()
	// An answer the clone may give otherwise later, such as objects gone
	// missing or git failing, drops the kept range, so the next request
	// opens and resolves it again.
	unavailable := func(err error) (PatchResult, bool, error) {
		result, handled, err := unreadable(err)
		// Only what says the clone changed drops the range: not this
		// request's own cancellation, nor an answer about its path.
		if handled && ctx.Err() == nil && (err != nil || result.Code == CodeCommitsMissing || result.Code == CodePartialClone) {
			p.forget(r)
		}
		return result, handled, err
	}
	repo, oids := r.repo, r.oids
	files, err := p.changedFiles(ctx, oids[0], oids[1], func(ctx context.Context) (map[string]gitlocal.ChangedFile, error) {
		return repo.ChangedFiles(ctx, oids[0], oids[1])
	})
	if result, handled, err := unavailable(err); handled {
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
	if result, handled, err := unavailable(err); handled {
		return result, err
	}
	result, err = patchOf(string(out), change.TypeChanged)
	if err != nil || !result.Available {
		return result, err
	}
	return redactPatch(ctx, req.Path, result.Patch)
}

// redactPatch masks a file's patch as bundle.Redact masks the stored hunks
// diff parsing takes from it. A patch diff parsing reads as textless has no
// text patch.
func redactPatch(ctx context.Context, path, patch string) (PatchResult, error) {
	redactor, err := patchRedactor()
	if err != nil {
		return PatchResult{}, fmt.Errorf("building the redactor: %w", err)
	}
	masked, ok, err := bundle.RedactFilePatch(ctx, redactor, path, patch)
	if err != nil {
		return PatchResult{}, fmt.Errorf("redacting the patch: %w", err)
	}
	if !ok {
		return noPatch(CodeNoTextPatch)
	}
	return PatchResult{Available: true, Patch: masked}, nil
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

// changedFiles is the range's changed files, found once and kept for the
// last maxChangeSets ranges asked for. A failure is not kept.
func (p *Patcher) changedFiles(ctx context.Context, base, head string, load func(context.Context) (map[string]gitlocal.ChangedFile, error)) (map[string]gitlocal.ChangedFile, error) {
	key := base + ".." + head
	p.mu.Lock()
	var set *changeSet
	for i, kept := range p.changes {
		if kept.key == key {
			set = kept
			p.changes = append(slices.Delete(p.changes, i, i+1), set)
			break
		}
	}
	if set == nil {
		set = &changeSet{key: key, done: make(chan struct{})}
		if len(p.changes) >= maxChangeSets {
			p.changes = slices.Delete(p.changes, 0, 1)
		}
		p.changes = append(p.changes, set)
		p.mu.Unlock()
		set.files, set.err = load(ctx)
		if set.err != nil {
			p.mu.Lock()
			if i := slices.Index(p.changes, set); i >= 0 {
				p.changes = slices.Delete(p.changes, i, i+1)
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

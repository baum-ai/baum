// Package gitlocal reads a pull request out of a local clone with git, for
// a run on the user's machine. It resolves the pull request's commits,
// computes the comparison and per-file patches that bundle.BuildDiffIndex
// reads, reads single files at a revision, and checks a revision out into a
// temporary worktree.
//
// The git invocations (diff flags, rename detection, status mapping, patch
// formats and size limits) are the ones the hosted service's git mirror
// uses, and they run in the same kind of repository: a private temporary
// bare repository with default configuration and no refs, which borrows the
// clone's objects through objects/info/alternates. So the clone's own
// configuration (diff order files, submodule display, big-file threshold),
// attributes (working tree, info/attributes), .gitmodules, replace refs,
// grafts and promisor remote do not reach a read, nor do the user's global
// and system configuration or GIT_ environment variables, and the same pull
// request produces the same comparison, patches and blobs locally and
// hosted. A partial clone missing objects a read needs is reported as
// ErrPartialClone; nothing is fetched on demand.
//
// Only two operations run in the clone itself, with the user's
// configuration so credentials, URL rewrites and safe.directory work:
// finding the repository, and fetching a missing pull request commit, which
// lands under refs/baum/ and runs no hooks. The clone's branches, HEAD,
// index, working tree, configuration and worktree registrations are never
// changed. A checkout is a detached worktree of the private repository in a
// temporary directory outside the clone.
//
// Every git process runs without a shell, under a timeout, with its output
// capped, and with prompts, pagers and editors disabled. Errors carry a short
// sanitised stderr snippet and never the command line.
//
// It needs git 2.29 or newer (fetch --no-write-fetch-head and
// --no-auto-maintenance).
package gitlocal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lastpersonlabs/baum/bundle"
)

// Defaults match the hosted git mirror's limits.
const (
	DefaultRemote          = "origin"
	DefaultMaxComparison   = 20 << 20
	DefaultMaxPatchBytes   = 20 << 20
	DefaultMaxBlobBytes    = 10 << 20
	DefaultCommandTimeout  = 2 * time.Minute
	DefaultFetchTimeout    = 10 * time.Minute
	maxOIDOutputBytes      = 256
	maxSingleTreeEntrySize = 1 << 20
)

var (
	errOutputTooLarge = bundle.ErrOutputTooLarge
	// ErrInvalidInput reports an argument that is not a full commit ID, a
	// plain repository path or a usable ref name.
	ErrInvalidInput = errors.New("invalid git input")
	// ErrObjectNotFound reports a commit, merge base or file that does not
	// exist.
	ErrObjectNotFound = errors.New("git object not found")
	// ErrPartialClone reports a partial clone that lacks objects a read
	// needs. They are not fetched on demand.
	ErrPartialClone = errors.New("the partial clone is missing objects this pull request needs; use a full clone, or let git fetch them first, for example with git diff --stat <base> <head> in the clone")
)

// Options configure a Repo. The zero value uses the defaults.
type Options struct {
	// Remote is fetched from when a pull request commit is missing.
	Remote string
	// TempDir holds the private repository and worktrees; empty uses the
	// system temporary directory. It must be outside the clone.
	TempDir string
	// MaxComparisonBytes caps the output of each comparison command.
	MaxComparisonBytes int64
	// MaxPatchBytes caps one file's patch.
	MaxPatchBytes int64
	// MaxBlobBytes caps one file read by Blob.
	MaxBlobBytes int64
	// CommandTimeout bounds each local git command; FetchTimeout a fetch.
	CommandTimeout time.Duration
	FetchTimeout   time.Duration
}

// Repo is a local clone. Close removes its private repository.
type Repo struct {
	dir     string
	objects string
	shallow string
	mirror  string
	partial bool
	opts    Options

	mu        sync.Mutex
	closed    bool
	worktrees map[*Worktree]struct{}
}

// Open checks that dir is inside a git repository with a working tree and
// sets up the private repository reads run in. The caller must Close the
// result.
func Open(ctx context.Context, dir string, opts Options) (*Repo, error) {
	if opts.Remote == "" {
		opts.Remote = DefaultRemote
	}
	if strings.HasPrefix(opts.Remote, "-") || strings.ContainsAny(opts.Remote, " \t\r\n\x00") {
		return nil, fmt.Errorf("%w: remote name", ErrInvalidInput)
	}
	opts.MaxComparisonBytes = orDefault(opts.MaxComparisonBytes, DefaultMaxComparison)
	opts.MaxPatchBytes = orDefault(opts.MaxPatchBytes, DefaultMaxPatchBytes)
	opts.MaxBlobBytes = orDefault(opts.MaxBlobBytes, DefaultMaxBlobBytes)
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = DefaultCommandTimeout
	}
	if opts.FetchTimeout <= 0 {
		opts.FetchTimeout = DefaultFetchTimeout
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, runSpec{dir: abs, env: userEnv(), limit: 64 << 10, timeout: opts.CommandTimeout},
		"rev-parse", "--show-toplevel", "--git-path", "objects", "--git-path", "shallow", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}
	fields := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(fields) != 4 {
		return nil, errors.New("opening repository: unexpected rev-parse output")
	}
	absolute := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(abs, path)
	}
	r := &Repo{
		dir: filepath.Clean(fields[0]), objects: absolute(fields[1]), shallow: absolute(fields[2]),
		opts: opts, worktrees: map[*Worktree]struct{}{},
	}
	r.mirror, err = newMirror(opts.TempDir, r.objects, fields[3])
	if err != nil {
		return nil, err
	}
	if inside(r.dir, r.mirror) {
		_ = removeAll(r.mirror)
		return nil, fmt.Errorf("%w: temporary directory %s is inside the repository; set TMPDIR elsewhere", ErrInvalidInput, r.mirror)
	}
	if err := syncShallow(r.mirror, r.shallow); err != nil {
		_ = removeAll(r.mirror)
		return nil, fmt.Errorf("copying the shallow file: %w", err)
	}
	r.partial = isPartial(r.objects)
	return r, nil
}

// Close removes the worktrees still open and the private repository. It is
// safe to call more than once.
func (r *Repo) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	open := make([]*Worktree, 0, len(r.worktrees))
	for w := range r.worktrees {
		open = append(open, w)
	}
	r.mu.Unlock()
	var errs []error
	for _, w := range open {
		errs = append(errs, w.Close())
	}
	if err := removeAll(r.mirror); err != nil {
		errs = append(errs, fmt.Errorf("removing private repository: %w", err))
	}
	return errors.Join(errs...)
}

func orDefault(v, d int64) int64 {
	if v <= 0 {
		return d
	}
	return v
}

// Dir is the clone's top-level directory.
func (r *Repo) Dir() string { return r.dir }

// read runs a read-only command in the private repository. In a partial
// clone, a failure to read an object is ErrPartialClone.
func (r *Repo) read(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	out, err := run(ctx, runSpec{dir: r.mirror, env: mirrorEnv(r.mirror), limit: limit, timeout: r.opts.CommandTimeout}, args...)
	return out, r.missingObjects(err)
}

func (r *Repo) missingObjects(err error) error {
	var gitErr *Error
	if !r.partial || !errors.As(err, &gitErr) || isContextError(err) {
		return err
	}
	stderr := strings.ToLower(gitErr.Stderr)
	for _, marker := range []string{"unable to read", "bad object", "bad file", "missing", "could not read"} {
		if strings.Contains(stderr, marker) {
			return fmt.Errorf("%w: %w", ErrPartialClone, err)
		}
	}
	return err
}

func isContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

var oidPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validOID(value string) bool { return oidPattern.MatchString(value) }

func validGitPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(value)) != value || value == "." || strings.ContainsRune(value, 0) {
		return false
	}
	return !slices.Contains(strings.Split(value, "/"), "..")
}

func validRefName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") || strings.Contains(value, "//") || strings.Contains(value, "@{") {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x21 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r)
	})
}

// PullRequest identifies a pull request and its commits as GitHub reports
// them.
type PullRequest struct {
	Number  int
	BaseSHA string
	HeadSHA string
	// BaseRef is the base branch name (for example "main"); it is fetched
	// only when the base commit is missing locally.
	BaseRef string
}

// Pull is a pull request whose commits all exist locally.
type Pull struct {
	BaseOID      string
	HeadOID      string
	MergeBaseOID string
}

// ResolvePull makes sure the base and head commits exist locally and
// computes the merge base. A missing head is fetched from refs/pull/<N>/head
// and a missing base from the base branch, into refs/baum/pull/<N>/; a
// commit that is still missing (the pull request or branch has moved on
// since the commit was recorded) is then fetched by its ID, as the hosted
// mirror recovers one. A clone with both commits makes no network call.
func (r *Repo) ResolvePull(ctx context.Context, pr PullRequest) (Pull, error) {
	if pr.Number <= 0 || !validOID(pr.BaseSHA) || !validOID(pr.HeadSHA) {
		return Pull{}, ErrInvalidInput
	}
	type want struct{ name, oid, ref string }
	wants := []want{
		{"head", pr.HeadSHA, fmt.Sprintf("refs/baum/pull/%d/head", pr.Number)},
		{"base", pr.BaseSHA, fmt.Sprintf("refs/baum/pull/%d/base", pr.Number)},
	}
	missing := func() []want {
		var out []want
		for _, w := range wants {
			if !r.hasCommit(ctx, w.oid) {
				out = append(out, w)
			}
		}
		return out
	}
	var fetchErrs []error
	if absent := missing(); len(absent) > 0 {
		var refspecs []string
		for _, w := range absent {
			if w.name == "head" {
				refspecs = append(refspecs, fmt.Sprintf("+refs/pull/%d/head:%s", pr.Number, w.ref))
				continue
			}
			baseRef := strings.TrimPrefix(pr.BaseRef, "refs/heads/")
			if !validRefName(baseRef) {
				return Pull{}, fmt.Errorf("%w: base commit %s is missing and the base branch name is not usable", ErrInvalidInput, pr.BaseSHA)
			}
			refspecs = append(refspecs, fmt.Sprintf("+refs/heads/%s:%s", baseRef, w.ref))
		}
		if err := r.fetch(ctx, refspecs); err != nil {
			fetchErrs = append(fetchErrs, err)
		}
		if absent = missing(); len(absent) > 0 {
			refspecs = refspecs[:0]
			for _, w := range absent {
				refspecs = append(refspecs, "+"+w.oid+":"+w.ref)
			}
			if err := r.fetch(ctx, refspecs); err != nil {
				fetchErrs = append(fetchErrs, err)
			}
		}
		if absent = missing(); len(absent) > 0 {
			remote := sanitize([]byte(r.opts.Remote))
			err := fmt.Errorf("%w: %s commit %s is not available from %s", ErrObjectNotFound, absent[0].name, absent[0].oid, remote)
			if len(fetchErrs) > 0 {
				err = fmt.Errorf("%w: fetching pull request %d: %w", err, pr.Number, errors.Join(fetchErrs...))
			}
			return Pull{}, err
		}
	}
	mergeBase, err := r.mergeBase(ctx, pr.BaseSHA, pr.HeadSHA)
	if err != nil {
		return Pull{}, err
	}
	return Pull{BaseOID: pr.BaseSHA, HeadOID: pr.HeadSHA, MergeBaseOID: mergeBase}, nil
}

func (r *Repo) hasCommit(ctx context.Context, oid string) bool {
	_, err := r.read(ctx, 0, "cat-file", "-e", oid+"^{commit}")
	return err == nil
}

// fetch brings refspecs into the private refs/baum/ namespace of the clone.
// It keeps the user's configuration for credentials, URL rewrites and
// safe.directory, and runs no hooks and writes no FETCH_HEAD, tags,
// remote-tracking branches, commit-graph or auto-maintenance. In a partial
// clone the fetch keeps the clone's filter.
func (r *Repo) fetch(ctx context.Context, refspecs []string) error {
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-prune", "--no-auto-gc", "--no-auto-maintenance", "--refmap=", "--", r.opts.Remote}
	_, err := run(ctx, runSpec{
		dir: r.dir, env: userEnv(), timeout: r.opts.FetchTimeout,
		config: []string{
			"core.hooksPath=" + filepath.Join(r.mirror, "hooks"), "protocol.ext.allow=never", "gc.auto=0",
			"maintenance.auto=false", "fetch.recurseSubmodules=false", "fetch.writeCommitGraph=false",
		},
	}, append(args, refspecs...)...)
	if err != nil {
		return err
	}
	// A fetch into a shallow clone can add shallow commits.
	return syncShallow(r.mirror, r.shallow)
}

func (r *Repo) mergeBase(ctx context.Context, base, head string) (string, error) {
	out, err := r.read(ctx, maxOIDOutputBytes, "merge-base", base, head)
	if err != nil && (isContextError(err) || errors.Is(err, ErrPartialClone)) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("%w: no merge base for %s and %s (a shallow clone may lack history)", ErrObjectNotFound, base, head)
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", fmt.Errorf("%w: merge base", ErrObjectNotFound)
	}
	return oid, nil
}

// Compare lists the files that differ between the merge base of base and
// head and head, like the hosted comparison in its merge-base mode. Rename
// detection is on (-M, limit 1000). Each file carries added and deleted line
// counts (nil for binary files) and a patch hash that ignores line numbers,
// index lines and file names. The result's BaseOID is the merge base.
func (r *Repo) Compare(ctx context.Context, base, head string) (bundle.Comparison, error) {
	if !validOID(base) || !validOID(head) {
		return bundle.Comparison{}, ErrInvalidInput
	}
	mergeBase, err := r.mergeBase(ctx, base, head)
	if err != nil {
		return bundle.Comparison{}, err
	}
	limit := r.opts.MaxComparisonBytes
	raw, err := r.read(ctx, limit, "diff-tree", "-r", "--no-commit-id", "--raw", "-z", "-M", "-l1000", mergeBase, head)
	if err != nil {
		return bundle.Comparison{}, fmt.Errorf("comparing trees: %w", err)
	}
	files, err := parseRawDiff(raw)
	if err != nil {
		return bundle.Comparison{}, err
	}
	numstat, err := r.read(ctx, limit, "diff-tree", "-r", "--no-commit-id", "--numstat", "-z", "-M", "-l1000", mergeBase, head)
	if err != nil {
		return bundle.Comparison{}, fmt.Errorf("reading diff statistics: %w", err)
	}
	applyNumstat(files, numstat)
	var patchHashes []string
	full, err := r.read(ctx, limit, "diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", mergeBase, head)
	objectFallback := errors.Is(err, bundle.ErrOutputTooLarge)
	if err != nil && !objectFallback {
		return bundle.Comparison{}, fmt.Errorf("reading comparison patches: %w", err)
	}
	if !objectFallback {
		patchHashes, err = canonicalComparisonPatchHashesReader(bytes.NewReader(full))
		if err != nil {
			return bundle.Comparison{}, fmt.Errorf("hashing comparison patches: %w", err)
		}
		if len(patchHashes) != len(files) {
			return bundle.Comparison{}, fmt.Errorf("hashing comparison patches: got %d patches for %d files", len(patchHashes), len(files))
		}
	}
	for i := range files {
		if objectFallback || files[i].Additions == nil || files[i].Deletions == nil {
			files[i].PatchHash = objectPatchHash(files[i])
		} else {
			files[i].PatchHash = patchHashes[i]
		}
	}
	return bundle.Comparison{BaseOID: mergeBase, HeadOID: head, MergeBaseOID: mergeBase, Files: files}, nil
}

// PatchFetcher reads one file's text patch for a comparison Compare
// returned. A binary change reads as git's "Binary files ... differ" line.
// A patch over MaxPatchBytes is bundle.ErrOutputTooLarge; a git failure on
// the file itself (exit status, not cancellation or timeout) is
// bundle.ErrPatchUnreadable, except that missing objects of a partial clone
// are ErrPartialClone.
func (r *Repo) PatchFetcher(ctx context.Context, comparison bundle.Comparison) bundle.PatchFetcher {
	return func(paths ...string) ([]byte, error) {
		return r.TextPatch(ctx, comparison.BaseOID, comparison.HeadOID, paths...)
	}
}

// TextPatch diffs one file (or a rename's old and new path) between two
// commits exactly as given, without computing a merge base.
func (r *Repo) TextPatch(ctx context.Context, baseOID, headOID string, paths ...string) ([]byte, error) {
	if !validOID(baseOID) || !validOID(headOID) || len(paths) == 0 || len(paths) > 2 {
		return nil, ErrInvalidInput
	}
	for _, p := range paths {
		if !validGitPath(p) {
			return nil, ErrInvalidInput
		}
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", baseOID, headOID, "--"}
	for _, p := range paths {
		args = append(args, ":(literal)"+p)
	}
	out, err := r.read(ctx, r.opts.MaxPatchBytes, args...)
	var exit *exec.ExitError
	if err != nil && !errors.Is(err, ErrPartialClone) && ctx.Err() == nil && errors.As(err, &exit) && exit.Exited() {
		return nil, fmt.Errorf("%w: %v", bundle.ErrPatchUnreadable, err)
	}
	if err != nil {
		return nil, fmt.Errorf("reading file patch: %w", err)
	}
	return out, nil
}

// Blob is a file read at a revision.
type Blob struct {
	OID     string
	Mode    string
	Content []byte
}

// ReadBlob reads the blob at path in commit rev: a regular or executable
// file, or a symbolic link (its content is the link target), as the hosted
// mirror reads one. A missing path, or a directory or submodule, is
// ErrObjectNotFound; a file over MaxBlobBytes is bundle.ErrOutputTooLarge.
// A timeout or cancellation is returned as the context's error, where the
// hosted mirror reports ErrObjectNotFound, so a slow read is not mistaken
// for a missing file.
func (r *Repo) ReadBlob(ctx context.Context, rev, path string) (Blob, error) {
	if !validOID(rev) || !validGitPath(path) {
		return Blob{}, ErrInvalidInput
	}
	out, err := r.read(ctx, maxSingleTreeEntrySize, "ls-tree", "-z", "-l", rev, "--", ":(literal)"+path)
	if err != nil && (isContextError(err) || errors.Is(err, ErrPartialClone)) {
		return Blob{}, err
	}
	if err != nil || len(out) == 0 {
		return Blob{}, ErrObjectNotFound
	}
	record := string(bytes.TrimSuffix(out, []byte{0}))
	meta, name, ok := strings.Cut(record, "\t")
	fields := strings.Fields(meta)
	if !ok || len(fields) != 4 || name != path || fields[1] != "blob" {
		return Blob{}, ErrObjectNotFound
	}
	size, _ := strconv.ParseInt(fields[3], 10, 64)
	if size > r.opts.MaxBlobBytes {
		return Blob{}, bundle.ErrOutputTooLarge
	}
	content, err := r.read(ctx, r.opts.MaxBlobBytes, "cat-file", "blob", fields[2])
	if err != nil {
		return Blob{}, fmt.Errorf("reading blob: %w", err)
	}
	return Blob{OID: fields[2], Mode: fields[0], Content: content}, nil
}

func objectPatchHash(file bundle.FileChange) string {
	digest := sha256.New()
	for _, value := range []string{file.Status, file.OldMode, file.NewMode, file.OldOID, file.NewOID} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func canonicalComparisonPatchHashesReader(source io.Reader) ([]string, error) {
	reader := bufio.NewReaderSize(source, 64<<10)
	var hashes []string
	var digest hash.Hash
	lineStart := true
	skipLine := false
	inHeader := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) == 0 && errors.Is(err, io.EOF) {
			break
		}
		if lineStart && bytes.HasPrefix(fragment, []byte("diff --git ")) {
			if digest != nil {
				hashes = append(hashes, hex.EncodeToString(digest.Sum(nil)))
			}
			digest = sha256.New()
			skipLine = true
			inHeader = true
		} else if lineStart {
			hunkHeader := bytes.HasPrefix(fragment, []byte("@@ "))
			skipLine = hunkHeader || (inHeader && (bytes.HasPrefix(fragment, []byte("index ")) ||
				bytes.HasPrefix(fragment, []byte("--- ")) || bytes.HasPrefix(fragment, []byte("+++ "))))
			if hunkHeader {
				inHeader = false
			}
		}
		if digest != nil && !skipLine {
			_, _ = digest.Write(fragment)
		}
		lineStart = !errors.Is(err, bufio.ErrBufferFull)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return nil, err
		}
	}
	if digest != nil {
		hashes = append(hashes, hex.EncodeToString(digest.Sum(nil)))
	}
	return hashes, nil
}

func parseRawDiff(output []byte) ([]bundle.FileChange, error) {
	reader := bufio.NewReader(bytes.NewReader(output))
	var files []bundle.FileChange
	for {
		header, err := reader.ReadString(0)
		if errors.Is(err, io.EOF) && header == "" {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing raw diff: %w", err)
		}
		header = strings.TrimSuffix(header, "\x00")
		fields := strings.Fields(header)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return nil, errors.New("parsing raw diff header")
		}
		statusCode := fields[4]
		firstPath, err := reader.ReadString(0)
		if err != nil {
			return nil, fmt.Errorf("parsing raw diff path: %w", err)
		}
		firstPath = strings.TrimSuffix(firstPath, "\x00")
		change := bundle.FileChange{
			OldMode: strings.TrimPrefix(fields[0], ":"), NewMode: fields[1],
			OldOID: fields[2], NewOID: fields[3], Status: diffStatus(statusCode),
		}
		if strings.HasPrefix(statusCode, "R") || strings.HasPrefix(statusCode, "C") {
			secondPath, err := reader.ReadString(0)
			if err != nil {
				return nil, fmt.Errorf("parsing renamed diff path: %w", err)
			}
			change.OldPath = firstPath
			change.NewPath = strings.TrimSuffix(secondPath, "\x00")
		} else {
			change.OldPath, change.NewPath = firstPath, firstPath
			if strings.HasPrefix(statusCode, "A") {
				change.OldPath = ""
			}
			if strings.HasPrefix(statusCode, "D") {
				change.NewPath = ""
			}
		}
		files = append(files, change)
	}
	return files, nil
}

func applyNumstat(files []bundle.FileChange, output []byte) {
	index := make(map[string]*bundle.FileChange, len(files))
	for i := range files {
		index[files[i].NewPath] = &files[i]
		if files[i].NewPath == "" {
			index[files[i].OldPath] = &files[i]
		}
	}
	parts := bytes.Split(output, []byte{0})
	for i := 0; i < len(parts); i++ {
		record := string(parts[i])
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		pathName := fields[2]
		if pathName == "" && i+2 < len(parts) {
			i += 2
			pathName = string(parts[i])
		}
		change := index[pathName]
		if change == nil || fields[0] == "-" {
			continue
		}
		additions, addErr := strconv.ParseInt(fields[0], 10, 64)
		deletions, deleteErr := strconv.ParseInt(fields[1], 10, 64)
		if addErr == nil && deleteErr == nil {
			change.Additions, change.Deletions = &additions, &deletions
		}
	}
}

func diffStatus(value string) string {
	switch value[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "type_changed"
	default:
		return "unknown"
	}
}

// removeAll is os.RemoveAll, a seam for cleanup.
var removeAll = os.RemoveAll

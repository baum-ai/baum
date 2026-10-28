// Package gitlocal reads a pull request out of a local clone with git, for
// a run on the user's machine. It resolves the pull request's commits,
// computes the comparison and per-file patches that bundle.BuildDiffIndex
// reads, reads single files at a revision, and exports a commit's tree, or
// one path of it, as a tar archive.
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
// changed, except by a Fix: the temporary worktree, refs and push of a fix a
// person confirmed (see Fix). An export is the hosted mirror's own git archive command run in
// the private repository, so it is byte for byte the archive the hosted
// service produces, including whatever attributes that archive applies: it
// still applies working-tree-encoding from the tree.
//
// Every git process runs without a shell, under a timeout, with its output
// capped, and with prompts, pagers and editors disabled. Errors carry a short
// sanitised stderr snippet and never the command line.
//
// It needs git 2.29 or newer (fetch --no-write-fetch-head and
// --no-auto-maintenance); Open refuses an older one.
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
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/bundle"
)

// Defaults match the hosted git mirror's limits.
const (
	DefaultRemote          = "origin"
	DefaultMaxComparison   = 20 << 20
	DefaultMaxPatchBytes   = 20 << 20
	DefaultMaxBlobBytes    = 10 << 20
	DefaultMaxArchiveBytes = 1 << 30
	DefaultArchiveTimeout  = 10 * time.Minute
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
	// It satisfies errors.Is(err, bundle.ErrNotFound), the contract of
	// bundle.PinnedExport and bundle.BlobFetcher.
	ErrObjectNotFound error = objectNotFoundError{}
	// ErrPartialClone reports a partial clone that lacks objects a read
	// needs. They are not fetched on demand.
	// ErrClosed reports an operation on a Repo after Close.
	ErrClosed       = errors.New("git repository is closed")
	ErrPartialClone = errors.New("the partial clone is missing objects this pull request needs; use a full clone, or let git fetch them first, for example with git diff --stat <base> <head> in the clone")
)

// objectNotFoundError is ErrObjectNotFound's type; it matches
// bundle.ErrNotFound as well as itself.
type objectNotFoundError struct{}

func (objectNotFoundError) Error() string { return "git object not found" }

func (objectNotFoundError) Is(target error) bool { return target == bundle.ErrNotFound }

var (
	_ bundle.PinnedExport = (*Repo)(nil)
	_ bundle.BlobFetcher  = (*Repo)(nil)
)

// Options configure a Repo. The zero value uses the defaults.
type Options struct {
	// Remote is fetched from when a pull request commit is missing.
	Remote string
	// TempDir holds the private repository; empty uses the
	// system temporary directory. It must be outside the clone.
	TempDir string
	// MaxComparisonBytes caps the output of each comparison command.
	MaxComparisonBytes int64
	// MaxPatchBytes caps one file's patch.
	MaxPatchBytes int64
	// MaxBlobBytes caps one file read by Blob.
	MaxBlobBytes int64
	// MaxArchiveBytes caps the tar archive of one export.
	MaxArchiveBytes int64
	// ArchiveTimeout bounds one export from start to Close, however slowly
	// the archive is read.
	ArchiveTimeout time.Duration
	// CommandTimeout bounds each local git command; FetchTimeout a fetch.
	CommandTimeout time.Duration
	FetchTimeout   time.Duration
}

// Repo is a local clone. Close removes its private repository.
type Repo struct {
	dir     string
	common  string
	objects string
	shallow string
	mirror  string
	partial bool
	opts    Options

	// mu guards closed; ops counts operations in flight, which Close waits
	// for. ops.Add runs only under mu with closed false.
	// readers are the exports still open, which Close cancels.
	mu      sync.Mutex
	closed  bool
	readers map[*archiveReader]struct{}
	ops     sync.WaitGroup
}

// MinGitVersion is the oldest git this package supports, as shown to users.
const MinGitVersion = "2.29"

// minGitMajor and minGitMinor are the oldest git this package supports:
// fetch --no-write-fetch-head and --no-auto-maintenance arrived in 2.29.
const (
	minGitMajor = 2
	minGitMinor = 29
)

var gitVersionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)`)

// checkGitVersion fails clearly on a git too old for this package. An
// unrecognised version string is let through.
func checkGitVersion(ctx context.Context, dir string, timeout time.Duration) error {
	out, err := run(ctx, runSpec{dir: dir, env: userEnv(), limit: 4096, timeout: timeout}, "version")
	if err != nil {
		return fmt.Errorf("running git: %w", err)
	}
	return SupportedGitVersion(string(out))
}

// SupportedGitVersion fails when the output of "git version" names a git older
// than this package supports. An unrecognised string is let through.
func SupportedGitVersion(version string) error {
	m := gitVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return nil
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git %d.%d is too old: baum needs git %d.%d or newer", major, minor, minGitMajor, minGitMinor)
	}
	return nil
}

// begin registers an operation in flight, or fails once the Repo is
// closed. The caller must call the returned function when done.
func (r *Repo) begin() (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	r.ops.Add(1)
	return r.ops.Done, nil
}

// checkTempDir refuses a temporary directory inside the clone before
// anything is created there.
func checkTempDir(clone, tempDir string) error {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	abs, err := filepath.Abs(tempDir)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if inside(clone, abs) {
		return fmt.Errorf("%w: temporary directory %s is inside the repository; set TMPDIR elsewhere", ErrInvalidInput, abs)
	}
	return nil
}

// Open checks that dir is inside a git repository with a working tree and
// sets up the private repository reads run in. The caller must Close the
// result.
func Open(ctx context.Context, dir string, opts Options) (*Repo, error) {
	if opts.Remote == "" {
		opts.Remote = DefaultRemote
	}
	if !validRemoteName(opts.Remote) {
		return nil, fmt.Errorf("%w: remote name", ErrInvalidInput)
	}
	opts.MaxComparisonBytes = orDefault(opts.MaxComparisonBytes, DefaultMaxComparison)
	opts.MaxPatchBytes = orDefault(opts.MaxPatchBytes, DefaultMaxPatchBytes)
	opts.MaxBlobBytes = orDefault(opts.MaxBlobBytes, DefaultMaxBlobBytes)
	opts.MaxArchiveBytes = orDefault(opts.MaxArchiveBytes, DefaultMaxArchiveBytes)
	if opts.ArchiveTimeout <= 0 {
		opts.ArchiveTimeout = DefaultArchiveTimeout
	}
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
	if err := checkGitVersion(ctx, abs, opts.CommandTimeout); err != nil {
		return nil, err
	}
	out, err := run(ctx, runSpec{dir: abs, env: userEnv(), limit: 64 << 10, timeout: opts.CommandTimeout},
		"rev-parse", "--show-toplevel", "--git-path", "objects", "--git-path", "shallow", "--show-object-format", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}
	fields := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(fields) != 5 {
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
		common: absolute(fields[4]), opts: opts, readers: map[*archiveReader]struct{}{},
	}
	if err := checkTempDir(r.dir, opts.TempDir); err != nil {
		return nil, err
	}
	r.mirror, err = newMirror(opts.TempDir, r.objects, fields[3])
	if err != nil {
		return nil, err
	}
	if err := syncShallow(r.mirror, r.shallow); err != nil {
		_ = removeAll(r.mirror)
		return nil, fmt.Errorf("copying the shallow file: %w", err)
	}
	r.partial = isPartial(r.objects) || hasPromisorConfig(ctx, abs, opts.CommandTimeout)
	return r, nil
}

// Close cancels exports still open, waits for operations in flight, then
// removes the private repository. Later operations fail
// with ErrClosed. It is safe to call more than once.
func (r *Repo) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	open := make([]*archiveReader, 0, len(r.readers))
	for a := range r.readers {
		open = append(open, a)
	}
	r.mu.Unlock()
	// An export nobody closed must not hold Close up: stop git, and the
	// export's own goroutine reaps it and frees its slot.
	for _, a := range open {
		a.cancel()
	}
	r.ops.Wait()
	var errs []error
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

// CommonDir is the shared git directory, identical across linked worktrees.
func (r *Repo) CommonDir() string { return r.common }

// Dir is the clone's top-level directory.
func (r *Repo) Dir() string { return r.dir }

// read runs a read-only command in the private repository. It does not
// register an operation: every public method that calls it does, through
// begin, for its whole duration, so Close waits for it. In a partial
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
	for _, marker := range []string{"unable to read", "bad object", "bad file", "missing", "could not read", "cannot read"} {
		if strings.Contains(stderr, marker) {
			return fmt.Errorf("%w: %w", ErrPartialClone, err)
		}
	}
	return err
}

// passThrough reports an error that is not about the object asked for, so
// it must not be reported as ErrObjectNotFound.
func passThrough(err error) bool {
	return err != nil && (isContextError(err) || errors.Is(err, ErrPartialClone) || errors.Is(err, ErrClosed))
}

func isContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

var oidPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func validOID(value string) bool { return oidPattern.MatchString(value) }

func validGitPath(value string) bool {
	// Git paths are slash-separated on every OS, so validate with path, not
	// filepath: on Windows filepath.IsAbs("/etc/passwd") is false.
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.ContainsRune(value, 0) {
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
	// Remote is the remote a missing commit is fetched from; "" is the
	// Repo's Options.Remote.
	Remote string
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
	remote := pr.Remote
	if remote == "" {
		remote = r.opts.Remote
	}
	if pr.Number <= 0 || !validOID(pr.BaseSHA) || !validOID(pr.HeadSHA) || !validRemoteName(remote) {
		return Pull{}, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return Pull{}, err
	}
	defer done()
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
		if err := r.fetch(ctx, remote, refspecs); err != nil {
			fetchErrs = append(fetchErrs, err)
		}
		if absent = missing(); len(absent) > 0 {
			refspecs = refspecs[:0]
			for _, w := range absent {
				refspecs = append(refspecs, "+"+w.oid+":"+w.ref)
			}
			if err := r.fetch(ctx, remote, refspecs); err != nil {
				fetchErrs = append(fetchErrs, err)
			}
		}
		if absent = missing(); len(absent) > 0 {
			err := fmt.Errorf("%w: %s commit %s is not available from %s", ErrObjectNotFound, absent[0].name, absent[0].oid, sanitize([]byte(remote)))
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

// maxFetchRefspecs bounds the refspecs of one fetch.
const maxFetchRefspecs = 100

// FetchPulls fetches what the clone lacks of the pull requests' commits, as
// ResolvePull fetches them: a missing head from refs/pull/<N>/head into
// refs/baum/pull/<N>/head and a missing base by its id into
// refs/baum/pull/<N>/base of the first pull request it is the base of, up to
// 100 refspecs in one fetch. A clone with every commit makes no network call,
// and a pull request without a valid number and ids is skipped. When a fetch
// fails, the remote is asked for its HEAD: out of reach, the error is
// returned at once; answering, it lacks some of the refs, and those of the
// failed fetch are fetched one at a time so they keep out no other. A pull
// request whose commits are still missing is left for the caller to find
// so. A deadline or cancellation is returned at once.
func (r *Repo) FetchPulls(ctx context.Context, remote string, prs []PullRequest) error {
	if remote == "" {
		remote = r.opts.Remote
	}
	if !validRemoteName(remote) {
		return ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return err
	}
	defer done()
	var refspecs []string
	// A base several pull requests share is fetched once.
	bases := map[string]bool{}
	for _, pr := range prs {
		if pr.Number <= 0 || !validOID(pr.HeadSHA) || !validOID(pr.BaseSHA) {
			continue
		}
		if !r.hasCommit(ctx, pr.HeadSHA) {
			refspecs = append(refspecs, fmt.Sprintf("+refs/pull/%d/head:refs/baum/pull/%d/head", pr.Number, pr.Number))
		}
		if !bases[pr.BaseSHA] && !r.hasCommit(ctx, pr.BaseSHA) {
			bases[pr.BaseSHA] = true
			refspecs = append(refspecs, fmt.Sprintf("+%s:refs/baum/pull/%d/base", pr.BaseSHA, pr.Number))
		}
	}
	reachable := false
	for start := 0; start < len(refspecs); start += maxFetchRefspecs {
		chunk := refspecs[start:min(start+maxFetchRefspecs, len(refspecs))]
		err := r.fetch(ctx, remote, chunk)
		if err == nil {
			continue
		}
		if isContextError(err) {
			return err
		}
		if !reachable {
			if probe := r.lsRemote(ctx, remote); probe != nil {
				return errors.Join(err, probe)
			}
			reachable = true
		}
		if len(chunk) == 1 {
			continue
		}
		for _, refspec := range chunk {
			if err := r.fetch(ctx, remote, []string{refspec}); err != nil && isContextError(err) {
				return err
			}
		}
	}
	return nil
}

// DefaultRef is the ref FetchBranch fetches a branch into.
const DefaultRef = "refs/baum/default"

// FetchBranch fetches branch from remote into DefaultRef and returns the
// commit it points at.
func (r *Repo) FetchBranch(ctx context.Context, remote, branch string) (string, error) {
	if remote == "" {
		remote = r.opts.Remote
	}
	if !validRemoteName(remote) || !validRefName(branch) {
		return "", ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return "", err
	}
	defer done()
	if err := r.fetch(ctx, remote, []string{"+refs/heads/" + branch + ":" + DefaultRef}); err != nil {
		return "", err
	}
	// The fetch writes the clone's refs, which the private mirror reads
	// through its alternates by id only.
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: r.opts.CommandTimeout},
		"rev-parse", "--verify", "--end-of-options", DefaultRef+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", fmt.Errorf("%w: %s", ErrObjectNotFound, DefaultRef)
	}
	return oid, nil
}

// lsRemote asks the remote for its HEAD with the credentials and limits a
// fetch has, to tell a remote out of reach from refs it lacks.
func (r *Repo) lsRemote(ctx context.Context, remote string) error {
	env, authenticated, err := r.fetchCredentials(ctx, remote, r.fetchEnv(ctx, remote))
	if err != nil {
		return err
	}
	_, err = run(ctx, runSpec{dir: r.dir, env: env, limit: 64 << 10, timeout: r.opts.FetchTimeout,
		config: []string{"protocol.ext.allow=never"}}, "ls-remote", remote, "HEAD")
	if err != nil && authenticated {
		return httpsFailure("GitHub HTTPS request failed", err)
	}
	return err
}

// httpsFailure replaces the error of an authenticated command, whose output
// may name the credential, keeping a deadline or cancellation in it.
func httpsFailure(what string, err error) error {
	message := what + "; check repository access, organization SSO authorization and GitHub connection"
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled} {
		if errors.Is(err, cause) {
			return fmt.Errorf("%s: %w", message, cause)
		}
	}
	return errors.New(message)
}

func (r *Repo) hasCommit(ctx context.Context, oid string) bool {
	_, err := r.read(ctx, 0, "cat-file", "-e", oid+"^{commit}")
	return err == nil
}

// fetch brings refspecs from remote into the private refs/baum/ namespace of
// the clone.
// It keeps the user's configuration for credentials, URL rewrites and
// safe.directory, and runs no hooks and writes no FETCH_HEAD, tags,
// remote-tracking branches, commit-graph or auto-maintenance. In a partial
// clone the fetch keeps the clone's filter.
func (r *Repo) fetch(ctx context.Context, remote string, refspecs []string) error {
	env, authenticated, err := r.fetchCredentials(ctx, remote, r.fetchEnv(ctx, remote))
	if err != nil {
		return err
	}
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-prune", "--no-auto-gc", "--no-auto-maintenance", "--refmap=", "--", remote}
	_, err = run(ctx, runSpec{
		dir: r.dir, env: env, timeout: r.opts.FetchTimeout,
		config: []string{
			"core.hooksPath=" + filepath.Join(r.mirror, "hooks"), "protocol.ext.allow=never", "gc.auto=0",
			"maintenance.auto=false", "fetch.recurseSubmodules=false", "fetch.writeCommitGraph=false",
		},
	}, append(args, refspecs...)...)
	if onFetch != nil {
		onFetch(refspecs)
	}
	if err != nil {
		if authenticated {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return httpsFailure("GitHub HTTPS fetch failed", err)
		}
		return err
	}
	// A fetch into a shallow clone can add shallow commits.
	return syncShallow(r.mirror, r.shallow)
}

func (r *Repo) mergeBase(ctx context.Context, base, head string) (string, error) {
	out, err := r.read(ctx, maxOIDOutputBytes, "merge-base", base, head)
	if passThrough(err) {
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

// onFetch sees the refspecs of every fetch; tests set it.
var onFetch func(refspecs []string)

// afterMergeBase runs between Compare's first and second git command; tests
// set it.
var afterMergeBase func()

// Compare lists the files that differ between the merge base of base and
// head and head, like the hosted comparison in its merge-base mode. Rename
// detection is on (-M, limit 1000). Each file carries added and deleted line
// counts (nil for binary files) and a patch hash that ignores line numbers,
// index lines and file names. The result's BaseOID is the merge base.
func (r *Repo) Compare(ctx context.Context, base, head string) (bundle.Comparison, error) {
	if !validOID(base) || !validOID(head) {
		return bundle.Comparison{}, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return bundle.Comparison{}, err
	}
	defer done()
	mergeBase, err := r.mergeBase(ctx, base, head)
	if err != nil {
		return bundle.Comparison{}, err
	}
	if afterMergeBase != nil {
		afterMergeBase()
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
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
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
	done, err := r.begin()
	if err != nil {
		return Blob{}, err
	}
	defer done()
	out, err := r.read(ctx, maxSingleTreeEntrySize, "ls-tree", "-z", "-l", rev, "--", ":(literal)"+path)
	if passThrough(err) {
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

// Blob is ReadBlob's content, for bundle.BlobFetcher (LoadReviewConfig): a
// missing file is an error that satisfies errors.Is(err, bundle.ErrNotFound).
func (r *Repo) Blob(ctx context.Context, commitOID, path string) ([]byte, error) {
	blob, err := r.ReadBlob(ctx, commitOID, path)
	if err != nil {
		return nil, err
	}
	return blob.Content, nil
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
	// git writes a type change (file to symlink or gitlink) as a deletion
	// block and a creation block under the same header, which raw diff
	// reports as one file, so the repeated header continues the digest.
	var previousHeader []byte
	previousDeleted := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) == 0 && errors.Is(err, io.EOF) {
			break
		}
		if lineStart && bytes.HasPrefix(fragment, []byte("diff --git ")) {
			continuation := digest != nil && previousDeleted && bytes.Equal(fragment, previousHeader)
			if !continuation {
				if digest != nil {
					hashes = append(hashes, hex.EncodeToString(digest.Sum(nil)))
				}
				digest = sha256.New()
			}
			previousHeader = append(previousHeader[:0], fragment...)
			previousDeleted = false
			skipLine = true
			inHeader = true
		} else if lineStart {
			if inHeader && bytes.HasPrefix(fragment, []byte("deleted file mode ")) {
				previousDeleted = true
			}
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

// inside reports whether child is parent or below it.
func inside(parent, child string) bool {
	if p, err := filepath.EvalSymlinks(parent); err == nil {
		parent = p
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

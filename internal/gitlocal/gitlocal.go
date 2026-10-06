// Package gitlocal reads a pull request out of a local clone with git, for
// a run on the user's machine. It resolves the pull request's commits,
// computes the comparison and per-file patches that bundle.BuildDiffIndex
// reads, reads single files at a revision, and checks a revision out into a
// temporary worktree.
//
// The git invocations (diff flags, rename detection, status mapping, patch
// formats and size limits) are the ones the hosted service's git mirror uses,
// so the same pull request produces the same comparison and hunks locally and
// hosted. The one deliberate difference: a clone has a working tree and
// configuration, so every read runs with the user's configuration ignored and
// no working-tree attributes.
//
// Nothing here touches the user's branches, HEAD, index or working tree.
// Fetched pull request commits land under refs/baum/, and a checkout is a
// detached worktree in a temporary directory outside the clone.
//
// Every git process runs without a shell, under a timeout, with its output
// capped, and with prompts, pagers and editors disabled. Errors carry a short
// sanitised stderr snippet and never the command line.
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
)

// Options configure a Repo. The zero value uses the defaults.
type Options struct {
	// Remote is fetched from when a pull request commit is missing.
	Remote string
	// TempDir holds worktrees; empty uses the system temporary directory.
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

// Repo is a local clone.
type Repo struct {
	dir  string
	opts Options
}

// Open checks that dir is inside a git repository and returns it.
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
	r := &Repo{dir: abs, opts: opts}
	out, err := r.read(ctx, maxOIDOutputBytes+4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}
	r.dir = strings.TrimSpace(string(out))
	return r, nil
}

func orDefault(v, d int64) int64 {
	if v <= 0 {
		return d
	}
	return v
}

// Dir is the clone's top-level directory.
func (r *Repo) Dir() string { return r.dir }

// read runs a read-only command with the hermetic configuration.
func (r *Repo) read(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	return run(ctx, runSpec{
		dir: r.dir, limit: limit, mode: envHermetic, timeout: r.opts.CommandTimeout,
		config: hermeticConfig, global: []string{"--attr-source=" + emptyTreeOID},
	}, args...)
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

// ResolvePull makes sure the base and head commits exist locally, fetching
// refs/pull/<N>/head and the base branch from the remote into
// refs/baum/pull/<N>/ when one is missing, and computes the merge base. A
// clone with both commits makes no network call.
func (r *Repo) ResolvePull(ctx context.Context, pr PullRequest) (Pull, error) {
	if pr.Number <= 0 || !validOID(pr.BaseSHA) || !validOID(pr.HeadSHA) {
		return Pull{}, ErrInvalidInput
	}
	var refspecs []string
	if !r.hasCommit(ctx, pr.HeadSHA) {
		refspecs = append(refspecs, fmt.Sprintf("+refs/pull/%d/head:refs/baum/pull/%d/head", pr.Number, pr.Number))
	}
	if !r.hasCommit(ctx, pr.BaseSHA) {
		baseRef := strings.TrimPrefix(pr.BaseRef, "refs/heads/")
		if !validRefName(baseRef) {
			return Pull{}, fmt.Errorf("%w: base commit %s is missing and the base branch name is not usable", ErrInvalidInput, pr.BaseSHA)
		}
		refspecs = append(refspecs, fmt.Sprintf("+refs/heads/%s:refs/baum/pull/%d/base", baseRef, pr.Number))
	}
	if len(refspecs) > 0 {
		if err := r.fetch(ctx, refspecs); err != nil {
			return Pull{}, fmt.Errorf("fetching pull request %d from %s: %w", pr.Number, r.opts.Remote, err)
		}
	}
	for name, oid := range map[string]string{"head": pr.HeadSHA, "base": pr.BaseSHA} {
		if !r.hasCommit(ctx, oid) {
			return Pull{}, fmt.Errorf("%w: %s commit %s is not available from %s", ErrObjectNotFound, name, oid, r.opts.Remote)
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

// fetch brings refspecs into the private refs/baum/ namespace. It keeps the
// user's configuration for credentials and URL rewrites, and writes no
// FETCH_HEAD, tags, remote-tracking branches or auto-maintenance.
func (r *Repo) fetch(ctx context.Context, refspecs []string) error {
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-gc", "--no-auto-maintenance", "--refmap=", "--", r.opts.Remote}
	_, err := run(ctx, runSpec{
		dir: r.dir, mode: envUser, timeout: r.opts.FetchTimeout,
		config: []string{"protocol.ext.allow=never", "gc.auto=0", "maintenance.auto=false", "fetch.recurseSubmodules=false"},
	}, append(args, refspecs...)...)
	return err
}

func (r *Repo) mergeBase(ctx context.Context, base, head string) (string, error) {
	out, err := r.read(ctx, maxOIDOutputBytes, "merge-base", base, head)
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
// bundle.ErrPatchUnreadable.
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
	if err != nil && ctx.Err() == nil && errors.As(err, &exit) && exit.Exited() {
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

// ReadBlob reads the regular file at path in commit rev. A missing path, or
// one that is not a file, is ErrObjectNotFound; a file over MaxBlobBytes is
// bundle.ErrOutputTooLarge.
func (r *Repo) ReadBlob(ctx context.Context, rev, path string) (Blob, error) {
	if !validOID(rev) || !validGitPath(path) {
		return Blob{}, ErrInvalidInput
	}
	out, err := r.read(ctx, maxSingleTreeEntrySize, "ls-tree", "-z", "-l", rev, "--", ":(literal)"+path)
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

// removeAll is os.RemoveAll, a seam for worktree cleanup.
var removeAll = os.RemoveAll

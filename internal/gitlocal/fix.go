package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A Fix is a temporary linked worktree of the clone in which an agent fixes a
// pull request, and the git around it: fetching the pull request's branch,
// starting a merge of its base, committing what the agent left, checking the
// result descends from the head it started from, and pushing it. It is the
// one place this package changes the clone: it adds a worktree registration
// and temporary refs under refs/baum/fix/, and Close removes both.
//
// Every command runs with the user's configuration, as a fetch does, so
// identity, signing, credentials and URL rewrites work as they do for the
// user, but with core.hooksPath pointing at an empty directory outside the
// checkout, so no hook runs, from the clone or from the pull request. After the
// agent has run, commands name the worktree's git directory explicitly
// (GIT_DIR and GIT_WORK_TREE), so a .git file the agent rewrote is ignored.
type Fix struct {
	repo    *Repo
	dir     string
	tree    string
	admin   string
	hooks   string
	remote  string
	headRef string
	refs    []string
	// Head is the pull request's head commit the fix starts from; Base the
	// base branch's commit, empty when the base was not fetched.
	Head, Base string
}

// FixOptions say where a Fix starts.
type FixOptions struct {
	// Dir is a new directory outside the clone that holds the fix: its
	// checkout and its empty hooks directory. Close removes it.
	Dir string
	// ID names the temporary refs, refs/baum/fix/<ID>/head and .../base.
	ID string
	// HeadRef is the pull request's branch and HeadSHA the commit it must
	// still point at.
	HeadRef, HeadSHA string
	// BaseRef, when set, is the base branch, fetched for a merge.
	BaseRef string
}

// ErrHeadMoved reports a pull request branch that no longer points at the
// commit the fix was confirmed for.
var ErrHeadMoved = errors.New("the pull request's branch has moved on since the fix was confirmed")

// ErrPushRejected reports a push the remote refused, typically because the
// branch moved on while the fix ran.
var ErrPushRejected = errors.New("the remote refused the push")

var fixID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// StartFix fetches the pull request's branch (and base branch) from the
// Repo's remote into refs/baum/fix/<ID>/, refuses a branch that moved on from
// HeadSHA, and checks HeadSHA out, detached, in a new linked worktree at
// <Dir>/checkout. The caller must Close the result, also on failure later on.
func (r *Repo) StartFix(ctx context.Context, opts FixOptions) (*Fix, error) {
	if !fixID.MatchString(opts.ID) || !validRefName(opts.HeadRef) || !validOID(opts.HeadSHA) || (opts.BaseRef != "" && !validRefName(opts.BaseRef)) || !filepath.IsAbs(opts.Dir) {
		return nil, ErrInvalidInput
	}
	if inside(r.dir, opts.Dir) || inside(r.common, opts.Dir) {
		return nil, fmt.Errorf("%w: the fix directory is inside the repository", ErrInvalidInput)
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	f := &Fix{repo: r, dir: opts.Dir, tree: filepath.Join(opts.Dir, "checkout"), hooks: filepath.Join(opts.Dir, "hooks"), remote: r.opts.Remote, headRef: opts.HeadRef, Head: opts.HeadSHA}
	if err := os.MkdirAll(f.hooks, 0o700); err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = f.Close()
		}
	}()
	// A checkout an earlier, interrupted fix left registered is forgotten.
	_ = f.clone(ctx, r.opts.CommandTimeout, "worktree", "prune")
	prefix := "refs/baum/fix/" + opts.ID + "/"
	refspecs := []string{"+refs/heads/" + opts.HeadRef + ":" + prefix + "head"}
	f.refs = []string{prefix + "head"}
	if opts.BaseRef != "" {
		refspecs = append(refspecs, "+refs/heads/"+opts.BaseRef+":"+prefix+"base")
		f.refs = append(f.refs, prefix+"base")
	}
	if err := r.fetch(ctx, f.remote, refspecs); err != nil {
		return nil, err
	}
	head, err := f.resolve(ctx, prefix+"head")
	if err != nil {
		return nil, err
	}
	if head != opts.HeadSHA {
		return nil, ErrHeadMoved
	}
	if opts.BaseRef != "" {
		if f.Base, err = f.resolve(ctx, prefix+"base"); err != nil {
			return nil, err
		}
	}
	if err := f.clone(ctx, r.opts.CommandTimeout, "worktree", "add", "--detach", f.tree, opts.HeadSHA); err != nil {
		return nil, err
	}
	out, err := run(ctx, runSpec{dir: f.tree, env: userEnv(), limit: 64 << 10, timeout: r.opts.CommandTimeout}, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	f.admin = filepath.Clean(strings.TrimSpace(string(out)))
	if !inside(r.common, f.admin) {
		return nil, errors.New("the fix checkout's git directory is not in the clone")
	}
	started = true
	return f, nil
}

// Dir is the checkout the agent works in.
func (f *Fix) Dir() string { return f.tree }

// config are the settings every command of a fix runs with: no hooks, no
// file-system monitor left running for a temporary checkout, no automatic
// maintenance, no external transport, and no git run inside a nested
// repository the agent may have created.
func (f *Fix) config() []string {
	return []string{"core.hooksPath=" + f.hooks, "core.fsmonitor=false", "gc.auto=0", "maintenance.auto=false", "protocol.ext.allow=never",
		"submodule.recurse=false", "diff.ignoreSubmodules=all", "status.submoduleSummary=false"}
}

// clone runs a command in the clone itself.
func (f *Fix) clone(ctx context.Context, timeout time.Duration, args ...string) error {
	_, err := run(ctx, runSpec{dir: f.repo.dir, env: userEnv(), timeout: timeout, config: f.config()}, args...)
	return err
}

// git runs a command in the checkout, naming its git directory explicitly.
func (f *Fix) git(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	env := append(userEnv(), "GIT_DIR="+f.admin, "GIT_WORK_TREE="+f.tree)
	return run(ctx, runSpec{dir: f.tree, env: env, limit: limit, timeout: f.repo.opts.CommandTimeout, config: f.config()}, args...)
}

func (f *Fix) resolve(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, runSpec{dir: f.repo.dir, env: userEnv(), limit: maxOIDOutputBytes, timeout: f.repo.opts.CommandTimeout},
		"rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrObjectNotFound, ref)
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", fmt.Errorf("%w: %s", ErrObjectNotFound, ref)
	}
	return oid, nil
}

// Merge starts merging the base branch into the checkout with message. It
// returns the conflicted paths when git stopped at conflicts, leaving the
// merge in progress for the agent to resolve, and none when the merge
// committed or there was nothing to merge.
func (f *Fix) Merge(ctx context.Context, message string) ([]string, error) {
	if f.Base == "" {
		return nil, errors.New("no base branch was fetched")
	}
	_, mergeErr := f.git(ctx, 0, "merge", "--no-ff", "--no-edit", "-m", message, f.Base)
	if mergeErr == nil {
		return nil, nil
	}
	out, err := f.git(ctx, 4<<20, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var conflicts []string
	for path := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path != "" {
			conflicts = append(conflicts, path)
		}
	}
	if len(conflicts) == 0 {
		return nil, mergeErr
	}
	return conflicts, nil
}

// Changed lists the paths commit changes against the fix's starting head,
// a rename as its old and new paths.
func (f *Fix) Changed(ctx context.Context, commit string) ([]string, error) {
	if !validOID(commit) {
		return nil, ErrInvalidInput
	}
	out, err := f.git(ctx, 4<<20, "diff", "--name-only", "--no-renames", "-z", f.Head, commit, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for path := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// HEAD is the commit the checkout is at.
func (f *Fix) HEAD(ctx context.Context) (string, error) {
	out, err := f.git(ctx, maxOIDOutputBytes, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", errors.New("unexpected HEAD")
	}
	return oid, nil
}

// Commit stages everything the agent left in the checkout, ignored files
// excepted, and commits it with message, completing a merge in progress. With
// nothing to commit it changes nothing. It returns the checkout's HEAD.
func (f *Fix) Commit(ctx context.Context, message string) (string, error) {
	if _, err := f.git(ctx, 0, "add", "--all"); err != nil {
		return "", err
	}
	_, err := os.Lstat(filepath.Join(f.admin, "MERGE_HEAD"))
	merging := err == nil
	_, err = f.git(ctx, 0, "diff", "--cached", "--quiet")
	var exit *exec.ExitError
	staged := errors.As(err, &exit) && exit.ExitCode() == 1
	if err != nil && !staged {
		return "", err
	}
	if merging || staged {
		if _, err := f.git(ctx, 0, "commit", "--no-verify", "--quiet", "-m", message); err != nil {
			return "", err
		}
	}
	return f.HEAD(ctx)
}

// Descends reports whether commit has the fix's starting head among its
// ancestors (or is it): a fix may only add commits on top of it.
func (f *Fix) Descends(ctx context.Context, commit string) (bool, error) {
	if !validOID(commit) {
		return false, ErrInvalidInput
	}
	_, err := f.git(ctx, 0, "merge-base", "--is-ancestor", f.Head, commit)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// Push updates the pull request's branch to commit with a plain push to the
// Repo's remote, never forced: the remote refuses it (ErrPushRejected) unless
// the branch still points at an ancestor of commit. It uses the user's own
// git credentials (credential helper, SSH agent), never baum's GitHub token,
// and pushes nothing else: no tags, no submodules.
func (f *Fix) Push(ctx context.Context, commit string) error {
	if !validOID(commit) {
		return ErrInvalidInput
	}
	env := userEnv()
	if b := background.Load(); b != nil {
		url, err := f.repo.RemotePushURL(ctx, f.remote)
		env = b.env(ctx, f.repo.dir, env, err != nil || sshRemote(url))
	}
	config := append(f.config(), "push.followTags=false", "push.recurseSubmodules=no", "push.gpgSign=false")
	_, err := run(ctx, runSpec{dir: f.repo.dir, env: env, timeout: f.repo.opts.FetchTimeout, config: config},
		"push", "--no-verify", "--porcelain", "--", f.remote, commit+":refs/heads/"+f.headRef)
	var gitErr *Error
	// --porcelain reports the ref's "[rejected]" on stdout; stderr keeps the
	// summary.
	if errors.As(err, &gitErr) && (strings.Contains(gitErr.Stderr, "Updates were rejected") || strings.Contains(gitErr.Stderr, "non-fast-forward") || strings.Contains(gitErr.Stderr, "[rejected]")) {
		return fmt.Errorf("%w: %w", ErrPushRejected, err)
	}
	return err
}

// Close removes the checkout, its registration and the temporary refs, and
// then the fix's directory. It runs even after the context of the fix ended,
// and is safe to call more than once.
func (f *Fix) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), f.repo.opts.CommandTimeout)
	defer cancel()
	var errs []error
	if _, err := os.Lstat(f.tree); err == nil {
		if err := f.clone(ctx, f.repo.opts.CommandTimeout, "worktree", "remove", "--force", "--force", f.tree); err != nil {
			errs = append(errs, err)
		}
	}
	if err := forceRemove(f.dir); err != nil {
		errs = append(errs, fmt.Errorf("removing the fix checkout: %w", err))
	}
	if err := f.clone(ctx, f.repo.opts.CommandTimeout, "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}
	for _, ref := range f.refs {
		_ = f.clone(ctx, f.repo.opts.CommandTimeout, "update-ref", "-d", ref)
	}
	f.refs = nil
	if _, err := os.Lstat(f.dir); err == nil {
		return errors.Join(errs...)
	}
	// The checkout is gone: a failed worktree remove was made good by the
	// removal and the prune.
	return nil
}

// forceRemove removes dir, making read-only directories in it writable first
// when a plain removal fails, as one a build tool left read-only would.
func forceRemove(dir string) error {
	if err := removeAll(dir); err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return removeAll(dir)
}

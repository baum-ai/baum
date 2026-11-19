package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// A Fix is the directory in which an agent fixes a pull request, and the git
// around it: fetching the pull request's branch and its base branch, giving
// the agent a repository of its own, committing what the agent left,
// checking the result descends from the head it started from, and pushing
// it. In the clone it adds only temporary refs under refs/baum/fix/, which
// Close removes, and the objects of baum's commit.
//
// The fix's directory holds three directories the agent may write to and two
// it may not:
//
//   - checkout, the work tree, whose .git file names git;
//   - git, the agent's own repository: its objects borrow the clone's through
//     alternates, the pull request's branch is checked out at the head and
//     the base branch is origin/<base>. The agent may use git freely there.
//   - tmp, the agent's scratch directory;
//   - hooks, an empty directory every command of baum's names as
//     core.hooksPath;
//   - index, the index of baum's own commands.
//
// baum never runs git with the agent's repository as its git directory once
// the agent has run, nor reads its configuration, hooks or attributes: its
// own commands name the clone's common git directory (GIT_DIR), the checkout
// (GIT_WORK_TREE) and its own index (GIT_INDEX_FILE), with no hook, no file
// system monitor and no sparse checkout. Of the agent's repository it reads
// only HEAD, MERGE_HEAD and the refs they name, as files, and the commits
// they name, to tell whether the agent merged the base branch.
type Fix struct {
	repo    *Repo
	dir     string
	tree    string
	gitDir  string
	hooks   string
	index   string
	remote  string
	headRef string
	refs    []string
	// env are the user's agent variables, set over the user's environment.
	env []string
	// Head is the pull request's head commit the fix starts from; Base the
	// base branch's commit, empty when the base was not fetched.
	Head, Base string
	// merged is whether the agent's result merges the base branch, once
	// Merged has looked.
	merged *bool
}

// FixOptions say where a Fix starts.
type FixOptions struct {
	// Dir is a new directory outside the clone that holds the fix (see Fix).
	// Close removes it.
	Dir string
	// ID names the temporary refs, refs/baum/fix/<ID>/head and .../base.
	ID string
	// HeadRef is the pull request's branch and HeadSHA the commit it must
	// still point at.
	HeadRef, HeadSHA string
	// BaseRef, when set, is the base branch, fetched and given to the agent
	// as origin/<BaseRef>.
	BaseRef string
	// Env are the user's agent variables ("NAME=value", config.Config.AgentEnv)
	// for every command of the fix and the push, set over the user's
	// environment; one that would redirect git to another repository is
	// left out.
	Env []string
}

// ErrHeadMoved reports a pull request branch that no longer points at the
// commit the fix was confirmed for.
var ErrHeadMoved = errors.New("the pull request's branch has moved on since the fix was started")

// ErrPushRejected reports a push the remote refused, typically because the
// branch moved on while the fix ran.
var ErrPushRejected = errors.New("the remote refused the push")

// ErrWorkflowScope reports a push GitHub refused because it changes a
// workflow and the credential may not: an OAuth App or personal access
// token without the workflow scope, or a GitHub App without the workflows
// permission.
var ErrWorkflowScope = errors.New("the remote refused a workflow change from this credential")

// workflowRefusal matches GitHub's reason for refusing a workflow change,
// "refusing to allow an OAuth App to create or update workflow
// `.github/workflows/ci.yml` without `workflow` scope" and its personal
// access token and GitHub App forms.
var workflowRefusal = regexp.MustCompile("refusing to allow an? .{1,40} to create or update workflow .{0,512}without `workflows?` (scope|permission)")

var fixID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// StartFix fetches the pull request's branch (and base branch) from the
// Repo's remote into refs/baum/fix/<ID>/, refuses a branch that moved on from
// HeadSHA, and makes the agent's repository at <Dir>/git with the branch
// checked out at HeadSHA in <Dir>/checkout. The caller must Close the
// result, also on failure later on.
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
	f := &Fix{repo: r, dir: opts.Dir, tree: filepath.Join(opts.Dir, "checkout"), gitDir: filepath.Join(opts.Dir, "git"),
		hooks: filepath.Join(opts.Dir, "hooks"), index: filepath.Join(opts.Dir, "index"),
		remote: r.opts.Remote, headRef: opts.HeadRef, Head: opts.HeadSHA, env: opts.Env}
	if err := os.MkdirAll(f.hooks, 0o700); err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = f.Close()
		}
	}()
	// A checkout an earlier version of baum registered is forgotten.
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
	if err := f.makeAgentRepository(ctx, opts.BaseRef); err != nil {
		return nil, err
	}
	started = true
	return f, nil
}

// makeAgentRepository makes the agent's repository: no template (so no
// hook), the clone's object format, the clone's objects as alternates, the
// branch at the head and checked out, and the base as origin/<baseRef>.
// Commits the agent makes there are not signed: the agent has no access to
// the user's signing keys, and baum makes the commit it pushes itself.
func (f *Fix) makeAgentRepository(ctx context.Context, baseRef string) error {
	timeout := f.repo.opts.CommandTimeout
	out, err := run(ctx, runSpec{dir: f.repo.dir, env: f.userEnv(), limit: 64, timeout: timeout}, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	format := strings.TrimSpace(string(out))
	if format != "sha1" && format != "sha256" {
		return fmt.Errorf("unexpected object format %q", format)
	}
	if _, err := run(ctx, runSpec{dir: f.dir, env: f.userEnv(), timeout: timeout, config: f.config()},
		"init", "--quiet", "--template=", "--object-format="+format, "--separate-git-dir="+f.gitDir, f.tree); err != nil {
		return err
	}
	alternates := filepath.Join(f.gitDir, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternates), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(alternates, []byte(filepath.ToSlash(filepath.Join(f.repo.common, "objects"))+"\n"), 0o644); err != nil {
		return err
	}
	// baum's own git writes the files, with the clone's configuration, so a
	// smudge filter (Git LFS) finds the clone's store and remote; the agent's
	// repository then only records them.
	if _, err := f.git(ctx, 0, "read-tree", "-u", "--reset", f.Head); err != nil {
		return err
	}
	steps := [][]string{
		{"config", "commit.gpgSign", "false"},
		{"config", "tag.gpgSign", "false"},
		{"config", "gc.auto", "0"},
		{"update-ref", "refs/heads/" + f.headRef, f.Head},
	}
	if f.Base != "" {
		steps = append(steps, []string{"update-ref", "refs/remotes/origin/" + baseRef, f.Base})
	}
	steps = append(steps, []string{"symbolic-ref", "HEAD", "refs/heads/" + f.headRef})
	// Filters the clone alone defines (git lfs install --local, git-crypt)
	// apply in the agent's repository too, so the files baum wrote do not
	// look changed there.
	filters, _ := run(ctx, runSpec{dir: f.repo.dir, env: f.userEnv(), limit: 1 << 20, timeout: f.repo.opts.CommandTimeout},
		"config", "--local", "--includes", "-z", "--get-regexp", `^filter\.`)
	for entry := range strings.SplitSeq(strings.TrimRight(string(filters), "\x00"), "\x00") {
		if key, value, ok := strings.Cut(entry, "\n"); ok && strings.HasPrefix(key, "filter.") {
			steps = append(steps, []string{"config", "--add", key, value})
		}
	}
	steps = append(steps, []string{"reset", "--quiet", "--mixed", "HEAD"})
	for _, args := range steps {
		if _, err := f.agentGit(ctx, 0, args...); err != nil {
			return err
		}
	}
	return nil
}

// Dir is the checkout the agent works in.
func (f *Fix) Dir() string { return f.tree }

// GitDir is the agent's own repository, outside the checkout, which the
// agent may write to.
func (f *Fix) GitDir() string { return f.gitDir }

// userEnv is userEnv with the user's agent variables set over it. The fixed
// variables come last again, so those win.
func (f *Fix) userEnv() []string {
	env := userEnv()
	if len(f.env) == 0 {
		return env
	}
	for _, kv := range f.env {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strippedEnv[upper] || slices.ContainsFunc(strippedEnvPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) }) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, fixedEnv...)
}

// CheckSigning makes sure baum's commit can be made at all when the user's
// configuration signs commits (commit.gpgSign): it creates a signed commit
// object of the head's tree that nothing refers to, so a key that needs a
// passphrase prompt or an agent that is not running fails now, before the
// agent has run, instead of at the commit.
func (f *Fix) CheckSigning(ctx context.Context) error {
	sign, err := f.signs(ctx)
	if err != nil || !sign {
		return err
	}
	// commit-tree reads no commit.gpgSign; -S signs as a commit would.
	_, err = f.git(ctx, maxOIDOutputBytes, "commit-tree", "-S", "-p", f.Head, "-m", "baum signing check", f.Head+"^{tree}")
	return err
}

// signs reports whether the user's configuration signs commits.
func (f *Fix) signs(ctx context.Context) (bool, error) {
	out, err := f.git(ctx, 64, "config", "--type=bool", "--get", "commit.gpgsign")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// config are the settings every command of a fix runs with: no hooks, no
// file-system monitor left running for a temporary checkout, no automatic
// maintenance, no external transport, no sparse checkout of the clone's
// applied to the checkout, and no git run inside a nested repository the
// agent may have created.
func (f *Fix) config() []string {
	return []string{"core.hooksPath=" + f.hooks, "core.fsmonitor=false", "core.untrackedCache=false", "core.sparseCheckout=false", "index.sparse=false",
		"gc.auto=0", "maintenance.auto=false", "protocol.ext.allow=never",
		"submodule.recurse=false", "diff.ignoreSubmodules=all", "status.submoduleSummary=false"}
}

// clone runs a command in the clone itself.
func (f *Fix) clone(ctx context.Context, timeout time.Duration, args ...string) error {
	_, err := run(ctx, runSpec{dir: f.repo.dir, env: f.userEnv(), timeout: timeout, config: f.config()}, args...)
	return err
}

// git runs one of baum's own commands over the checkout: the clone's common
// git directory, so the clone's configuration and objects apply and the
// objects it writes are the clone's, the checkout as the work tree, and
// baum's own index. It never changes the clone's HEAD, index or refs.
func (f *Fix) git(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	env := append(f.userEnv(), "GIT_DIR="+f.repo.common, "GIT_WORK_TREE="+f.tree, "GIT_INDEX_FILE="+f.index)
	return run(ctx, runSpec{dir: f.tree, env: env, limit: limit, timeout: f.repo.opts.CommandTimeout, config: f.config()}, args...)
}

// agentGit runs a command in the agent's repository. Only StartFix and Merge
// use it, before the agent has run, while every file in that repository is
// baum's.
func (f *Fix) agentGit(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	// Git LFS cannot download from a repository without a remote; the files
	// are baum's to write (makeAgentRepository).
	env := append(f.userEnv(), "GIT_DIR="+f.gitDir, "GIT_WORK_TREE="+f.tree, "GIT_LFS_SKIP_SMUDGE=1")
	return run(ctx, runSpec{dir: f.tree, env: env, limit: limit, timeout: f.repo.opts.CommandTimeout, config: f.config()}, args...)
}

func (f *Fix) resolve(ctx context.Context, ref string) (string, error) {
	out, err := run(ctx, runSpec{dir: f.repo.dir, env: f.userEnv(), limit: maxOIDOutputBytes, timeout: f.repo.opts.CommandTimeout},
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

// Merge starts merging the base branch into the agent's repository with
// message, before the agent runs. It returns the conflicted paths when git
// stopped at conflicts, leaving the merge in progress for the agent to
// resolve, and none when the merge committed or there was nothing to merge.
func (f *Fix) Merge(ctx context.Context, message string) ([]string, error) {
	if f.Base == "" {
		return nil, errors.New("no base branch was fetched")
	}
	_, mergeErr := f.agentGit(ctx, 0, "-c", "user.name=baum", "-c", "user.email=baum@localhost", "merge", "--no-ff", "--no-edit", "-m", message, f.Base)
	if mergeErr == nil {
		return nil, nil
	}
	out, err := f.agentGit(ctx, 4<<20, "diff", "--name-only", "--diff-filter=U", "-z")
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

// Merged reports whether the agent's result merges the base branch: the
// commit its repository's HEAD names, or one MERGE_HEAD names (a merge it
// left uncommitted), has the base among its ancestors, and the head does
// not already. The files are read as files, never with git in the agent's
// repository; the ancestry is read with baum's own git, with the agent's
// objects borrowed for that one command and no commit-graph file trusted.
// A HEAD that cannot be read counts as no merge.
func (f *Fix) Merged(ctx context.Context) (bool, error) {
	if f.merged != nil {
		return *f.merged, nil
	}
	merged, err := f.mergedBase(ctx)
	if err != nil {
		return false, err
	}
	f.merged = &merged
	return merged, nil
}

func (f *Fix) mergedBase(ctx context.Context) (bool, error) {
	if f.Base == "" {
		return false, nil
	}
	if inHead, err := f.isAncestor(ctx, f.Base, f.Head, false); err != nil || inHead {
		return false, err
	}
	candidates := agentMergeHeads(f.gitDir)
	if head := agentHEAD(f.gitDir); head != "" {
		candidates = append(candidates, head)
	}
	for _, candidate := range candidates {
		if ok, err := f.isAncestor(ctx, f.Base, candidate, true); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// isAncestor reports whether ancestor is commit or one of its ancestors.
// With agent, the agent's objects are borrowed for the command; a commit
// that is not there is no descendant.
func (f *Fix) isAncestor(ctx context.Context, ancestor, commit string, agent bool) (bool, error) {
	env := append(f.userEnv(), "GIT_DIR="+f.repo.common)
	if agent {
		env = append(env, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(f.gitDir, "objects"))
	}
	config := append(f.config(), "core.commitGraph=false")
	_, err := run(ctx, runSpec{dir: f.dir, env: env, timeout: f.repo.opts.CommandTimeout, config: config}, "merge-base", "--is-ancestor", ancestor, commit)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && agent:
		// 1 is no; 128 a commit missing from what the agent left.
		return false, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// maxRefFileBytes bounds a ref file of the agent's that baum reads.
const maxRefFileBytes = 1 << 20

// readAgentFile reads a file of the agent's repository, at most
// maxRefFileBytes of it, following no symbolic link.
func readAgentFile(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return readRegular(path, maxRefFileBytes)
}

// readRegular reads at most limit bytes of the regular file at path, or
// returns "". It opens without blocking and checks what it opened, so a FIFO
// or device a process swapped in after a check cannot stall the read.
func readRegular(path string, limit int64) string {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return ""
	}
	return string(data)
}

// agentHEAD resolves the agent's HEAD from its files: a commit id, or a
// symbolic ref to a loose or packed ref, followed at most five times.
// Anything else is "".
func agentHEAD(gitDir string) string {
	value := strings.TrimSpace(readAgentFile(filepath.Join(gitDir, "HEAD")))
	for range 5 {
		ref, symbolic := strings.CutPrefix(value, "ref: ")
		if !symbolic {
			if validOID(value) {
				return value
			}
			return ""
		}
		ref = strings.TrimSpace(ref)
		if !strings.HasPrefix(ref, "refs/") || !validRefName(ref) || !validGitPath(ref) {
			return ""
		}
		loose := strings.TrimSpace(readAgentFile(filepath.Join(gitDir, filepath.FromSlash(ref))))
		if loose != "" {
			value = loose
			continue
		}
		value = packedRef(readAgentFile(filepath.Join(gitDir, "packed-refs")), ref)
	}
	return ""
}

// packedRef is ref's commit in a packed-refs file's text.
func packedRef(packed, ref string) string {
	for line := range strings.SplitSeq(packed, "\n") {
		oid, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == ref && validOID(oid) {
			return oid
		}
	}
	return ""
}

// agentMergeHeads are the commits the agent's MERGE_HEAD names.
func agentMergeHeads(gitDir string) []string {
	var heads []string
	for line := range strings.SplitSeq(readAgentFile(filepath.Join(gitDir, "MERGE_HEAD")), "\n") {
		if oid := strings.TrimSpace(line); validOID(oid) {
			heads = append(heads, oid)
		}
	}
	return heads
}

// Git modes of a tree entry a Change reports.
const (
	ModeDeleted = "000000"
	ModeFile    = "100644"
	ModeExec    = "100755"
	ModeSymlink = "120000"
	// ModeGitlink is a commit of another repository recorded in the tree, as
	// a submodule is: what git add makes of a nested repository.
	ModeGitlink = "160000"
)

// MaxChangeBytes bounds the content of a file Changes reads.
const MaxChangeBytes = 8 << 20

// Change is a path a fix's result changes beyond what the pull request's head
// and a merge of its base already held (see Changes).
type Change struct {
	Path string
	// Mode is the path's mode in the result, ModeDeleted when it is gone.
	Mode string
	// Content is a regular file's text in the result; Added are the
	// [start, end) byte ranges of its lines that none of the versions it
	// changed from has. A binary file (a NUL byte in its first 8000 bytes)
	// or one over MaxChangeBytes has no Content, and Binary or TooLarge set.
	Content          string
	Added            [][2]int
	Binary, TooLarge bool
}

// Changes lists what commit changes that the fix itself made, as git's tree
// entries, submodules included: the paths it changes against the fix's
// starting head. When the result merges the base branch (Merged), they are
// measured from the tree git's own merge makes when it makes one without
// conflicts; otherwise a path counts only when it also differs from the base
// branch, and a line only when neither side has it, so what the merge
// brought in is not taken for the fix's own work. A rename is its old and
// new paths.
func (f *Fix) Changes(ctx context.Context, commit string) ([]Change, error) {
	if !validOID(commit) {
		return nil, ErrInvalidInput
	}
	from := []string{f.Head}
	merged, err := f.Merged(ctx)
	if err != nil {
		return nil, err
	}
	if merged {
		from = append(from, f.Base)
		if tree := f.cleanMerge(ctx); tree != "" {
			from = []string{tree}
		}
	}
	var paths []string
	entries := map[string]rawEntry{}
	olds := map[string][]string{}
	for i, ref := range from {
		diff, err := f.rawDiff(ctx, ref, commit)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, e := range diff {
			if _, known := entries[e.path]; i > 0 && !known {
				continue
			}
			if i == 0 {
				paths = append(paths, e.path)
				entries[e.path] = e
			}
			seen[e.path] = true
			if regularMode(e.oldMode) {
				olds[e.path] = append(olds[e.path], e.oldOID)
			}
		}
		if i > 0 {
			paths = slices.DeleteFunc(paths, func(path string) bool { return !seen[path] })
			for path := range entries {
				if !seen[path] {
					delete(entries, path)
				}
			}
		}
	}
	changes := make([]Change, 0, len(paths))
	for _, path := range paths {
		e := entries[path]
		change := Change{Path: path, Mode: e.newMode}
		if regularMode(e.newMode) {
			if err := f.readChange(ctx, &change, e.newOID, olds[path]); err != nil {
				return nil, err
			}
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// cleanMerge is the tree git's own merge of the head and the base makes,
// when it makes one without conflicts (merge-tree --write-tree, git 2.38 and
// later); "" otherwise.
func (f *Fix) cleanMerge(ctx context.Context) string {
	// No work tree: attributes the agent wrote in the checkout choose no
	// merge driver.
	env := append(f.userEnv(), "GIT_DIR="+f.repo.common)
	out, err := run(ctx, runSpec{dir: f.dir, env: env, limit: 64 << 10, timeout: f.repo.opts.CommandTimeout, config: f.config()},
		"merge-tree", "--write-tree", "--no-messages", f.Head, f.Base)
	if err != nil {
		return ""
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !validOID(tree) {
		return ""
	}
	return tree
}

// rawEntry is one path of git's raw diff output.
type rawEntry struct {
	oldMode, newMode, oldOID, newOID, path string
}

// rawDiff is the raw diff of two commits' trees, every path, submodules
// included whatever the user's configuration says, renames as a deletion and
// an addition.
func (f *Fix) rawDiff(ctx context.Context, from, to string) ([]rawEntry, error) {
	out, err := f.git(ctx, 16<<20, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev", "--ignore-submodules=none", from, to, "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var entries []rawEntry
	for i := 0; i+1 < len(fields); i += 2 {
		// ":<old mode> <new mode> <old oid> <new oid> <status>", then the path.
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("unexpected git diff-tree output")
		}
		entries = append(entries, rawEntry{oldMode: meta[0], newMode: meta[1], oldOID: meta[2], newOID: meta[3], path: fields[i+1]})
	}
	return entries, nil
}

func regularMode(mode string) bool { return mode == ModeFile || mode == ModeExec }

// readChange reads the file blob oid into change, and marks the lines none
// of the blobs olds has as added. An old version too large to read has no
// lines, so every line counts as added.
func (f *Fix) readChange(ctx context.Context, change *Change, oid string, olds []string) error {
	content, err := f.git(ctx, MaxChangeBytes, "cat-file", "blob", oid)
	if errors.Is(err, errOutputTooLarge) {
		change.TooLarge = true
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
		change.Binary = true
		return nil
	}
	known := map[string]bool{}
	for _, old := range olds {
		body, err := f.git(ctx, MaxChangeBytes, "cat-file", "blob", old)
		if errors.Is(err, errOutputTooLarge) {
			continue
		}
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(body), "\n") {
			known[line] = true
		}
	}
	change.Content = string(content)
	start := 0
	for line := range strings.SplitSeq(change.Content, "\n") {
		end := start + len(line)
		if !known[line] && line != "" {
			if n := len(change.Added); n > 0 && change.Added[n-1][1]+1 == start {
				change.Added[n-1][1] = end
			} else {
				change.Added = append(change.Added, [2]int{start, end})
			}
		}
		start = end + 1
	}
	return nil
}

// Commit makes baum's own commit of everything the agent left in the
// checkout, ignored files excepted, with message: a commit on the head, or,
// when the result merges the base branch (Merged), a merge commit of the
// head and the base. The tree is the checkout's as baum's own index records
// it; nothing the agent committed or staged is used. The commit is signed
// when the user's configuration signs commits. With nothing changed and no
// merge it makes nothing and returns the head.
func (f *Fix) Commit(ctx context.Context, message string) (string, error) {
	merged, err := f.Merged(ctx)
	if err != nil {
		return "", err
	}
	if _, err := f.git(ctx, 0, "read-tree", f.Head); err != nil {
		return "", err
	}
	if _, err := f.git(ctx, 0, "add", "--all"); err != nil {
		return "", err
	}
	if merged {
		if err := f.addBaseFiles(ctx); err != nil {
			return "", err
		}
	}
	out, err := f.git(ctx, maxOIDOutputBytes, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !validOID(tree) {
		return "", errors.New("unexpected tree")
	}
	out, err = f.git(ctx, maxOIDOutputBytes, "rev-parse", "--verify", f.Head+"^{tree}")
	if err != nil {
		return "", err
	}
	if !merged && strings.TrimSpace(string(out)) == tree {
		return f.Head, nil
	}
	args := []string{"commit-tree"}
	sign, err := f.signs(ctx)
	if err != nil {
		return "", err
	}
	if sign {
		args = append(args, "-S")
	}
	args = append(args, "-p", f.Head)
	if merged {
		args = append(args, "-p", f.Base)
	}
	out, err = f.git(ctx, maxOIDOutputBytes, append(args, "-m", message, tree)...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !validOID(commit) {
		return "", errors.New("unexpected commit")
	}
	return commit, nil
}

// addBaseFiles stages the files the base branch added since the merge base
// that the checkout has, even where an ignore rule would leave them out (a
// file the base added with git add -f): git add --all skips an ignored path
// the index does not track yet, and the merge would drop it.
func (f *Fix) addBaseFiles(ctx context.Context) error {
	// Added since the merge base, so a file the pull request deleted is not
	// one the base added.
	out, err := f.git(ctx, maxOIDOutputBytes, "merge-base", f.Head, f.Base)
	if err != nil {
		return err
	}
	mergeBase := strings.TrimSpace(string(out))
	if !validOID(mergeBase) {
		return errors.New("unexpected merge base")
	}
	out, err = f.git(ctx, 16<<20, "diff-tree", "-r", "-z", "--name-only", "--no-renames", "--diff-filter=A", mergeBase, f.Base, "--")
	if err != nil {
		return err
	}
	var paths []byte
	for path := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if path == "" || !validGitPath(path) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(f.tree, filepath.FromSlash(path))); err == nil {
			paths = append(append(paths, path...), 0)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	env := append(f.userEnv(), "GIT_DIR="+f.repo.common, "GIT_WORK_TREE="+f.tree, "GIT_INDEX_FILE="+f.index, "GIT_LITERAL_PATHSPECS=1")
	_, err = run(ctx, runSpec{dir: f.tree, env: env, timeout: f.repo.opts.CommandTimeout, config: f.config(), stdin: paths},
		"add", "--force", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
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
	env := f.userEnv()
	if b := background.Load(); b != nil {
		url, err := f.repo.RemotePushURL(ctx, f.remote)
		env = b.env(ctx, f.repo.dir, env, err != nil || sshRemote(url))
	}
	config := append(f.config(), "push.followTags=false", "push.recurseSubmodules=no", "push.gpgSign=false")
	out, err := run(ctx, runSpec{dir: f.repo.dir, env: env, limit: 1 << 20, keepOutput: true, timeout: f.repo.opts.FetchTimeout, config: config},
		"push", "--no-verify", "--porcelain", "--", f.remote, commit+":refs/heads/"+f.headRef)
	var gitErr *Error
	// --porcelain reports a ref's "[remote rejected]" reason on stdout.
	if errors.As(err, &gitErr) && (workflowRefusal.MatchString(gitErr.Stderr) || workflowRefusal.Match(out)) {
		return fmt.Errorf("%w: %w", ErrWorkflowScope, err)
	}
	// --porcelain reports the ref's "[rejected]" on stdout; stderr keeps the
	// summary.
	if errors.As(err, &gitErr) && (strings.Contains(gitErr.Stderr, "Updates were rejected") || strings.Contains(gitErr.Stderr, "non-fast-forward") || strings.Contains(gitErr.Stderr, "[rejected]")) {
		return fmt.Errorf("%w: %w", ErrPushRejected, err)
	}
	return err
}

// Close removes the fix's directory and the temporary refs. It runs even
// after the context of the fix ended, and is safe to call more than once.
func (f *Fix) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), f.repo.opts.CommandTimeout)
	defer cancel()
	var errs []error
	if err := forceRemove(f.dir); err != nil {
		errs = append(errs, fmt.Errorf("removing the fix directory: %w", err))
	}
	for _, ref := range f.refs {
		_ = f.clone(ctx, f.repo.opts.CommandTimeout, "update-ref", "-d", ref)
	}
	f.refs = nil
	return errors.Join(errs...)
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

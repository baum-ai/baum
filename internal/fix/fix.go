// Package fix runs a fix a person asked for on one of their pull requests: an
// agent resolves its merge conflicts or failing checks in a temporary
// checkout of its branch, and baum commits the result and pushes it.
//
// The agent gets write access to that checkout only, never git credentials
// or the network (see agent.Fixer). baum itself fetches the branch, starts
// the merge of the base branch for a conflict, commits what the agent left
// (with no hook running), refuses a result that does not descend from the
// head the fix was confirmed for, and pushes with a plain, never forced, git
// push using the user's own git credentials for the clone's remote. The
// checkout is a linked worktree of the registered clone under baum's cache
// directory, removed when the fix ends however it ends.
package fix

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Why a fix cannot run, found before it is queued.
const (
	ReasonFork               = "fork"
	ReasonNoPushAccess       = "no_push_access"
	ReasonNothingToFix       = "nothing_to_fix"
	ReasonUnsupportedBackend = "unsupported_backend"
)

// Why a fix that ran failed.
const (
	ReasonHeadChanged      = "head_changed"
	ReasonNoRemote         = "no_remote"
	ReasonGit              = "git"
	ReasonBackend          = "backend"
	ReasonBudget           = "budget"
	ReasonNoChanges        = "no_changes"
	ReasonConflictsRemain  = "conflicts_remain"
	ReasonHistoryRewritten = "history_rewritten"
	ReasonPushRejected     = "push_rejected"
	ReasonPushFailed       = "push_failed"
)

// Error is a fix that failed, with the machine-readable Reason. Its text is
// baum's own or git's sanitised stderr, never a provider's error, so a job
// may keep it.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func fail(reason string, err error) error { return &Error{Reason: reason, Err: err} }

// Supported reports whether a backend can fix: only claude and codex
// confine an agent with write access (see agent.Fixer).
func Supported(backend string) bool {
	return backend == agent.BackendClaude || backend == agent.BackendCodex
}

// Options are one fix.
type Options struct {
	// ID names the fix's directory and temporary refs: the job's ID.
	ID string
	// Clone is the registered clone's directory.
	Clone      string
	Repository github.Repository
	Number     int
	Title      string
	// HeadRef and HeadSHA are the branch to push to and the commit the fix
	// was confirmed for; BaseRef the base branch.
	HeadRef, HeadSHA, BaseRef string
	// Conflicts merges the base branch first; Checks fixes the failing
	// Checks.
	Conflicts, Checks bool
	Evidence          []Check
	Agent             agent.Fixer
	// Backend and Model name the agent in the commit message.
	Backend, Model      string
	MaxCalls, MaxTokens int
	WallClock           time.Duration
	Logger              *slog.Logger
	// Directory holds the fixes' temporary directories; empty is
	// DefaultDirectory.
	Directory string
	// Progress receives the phase the fix is in.
	Progress func(phase string)
}

// Outcome is what a fix did.
type Outcome struct {
	// Commit is the commit pushed to the pull request's branch.
	Commit string
	// Summary is the agent's final message.
	Summary string
	// Agent is the agent's run, nil when none was needed (a base branch
	// that merged cleanly).
	Agent *agent.Result
}

// DefaultDirectory is where fixes check out: fixes under the user's cache
// directory, outside every clone and outside the system temporary
// directory, which a Codex fix may not write to.
func DefaultDirectory() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "baum", "fixes"), nil
}

// staleAfter is how old a fix directory left by a crash is before the next
// fix removes it; no fix runs that long.
const staleAfter = 24 * time.Hour

// defaultWallClock bounds an agent run whose Options set no WallClock.
const defaultWallClock = 45 * time.Minute

// Run makes the fix: see the package documentation. A failure is an *Error,
// except a cancellation, which is the context's error. The checkout is
// removed before Run returns.
func Run(ctx context.Context, o Options) (out Outcome, err error) {
	progress := o.Progress
	if progress == nil {
		progress = func(string) {}
	}
	root := o.Directory
	if root == "" {
		if root, err = DefaultDirectory(); err != nil {
			return out, fail(ReasonGit, err)
		}
	}
	if err := privatefs.MkdirAll(root); err != nil {
		return out, fail(ReasonGit, fmt.Errorf("fix directory: %w", err))
	}
	sweep(root)
	dir := filepath.Join(root, o.ID)
	repo, err := openForRemote(ctx, o.Clone, o.Repository)
	if err != nil {
		return out, err
	}
	defer func() { _ = repo.Close() }()
	progress("fetching")
	base := ""
	if o.Conflicts {
		base = o.BaseRef
	}
	checkout, err := repo.StartFix(ctx, gitlocal.FixOptions{Dir: dir, ID: o.ID, HeadRef: o.HeadRef, HeadSHA: o.HeadSHA, BaseRef: base})
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(err, gitlocal.ErrHeadMoved) {
			return out, fail(ReasonHeadChanged, err)
		}
		return out, fail(ReasonGit, err)
	}
	defer func() {
		if closeErr := checkout.Close(); closeErr != nil && o.Logger != nil {
			o.Logger.Warn("fix checkout not fully removed", "error", closeErr)
		}
	}()
	message := commitMessage(o)
	var conflicts []string
	if o.Conflicts {
		progress("merging")
		if conflicts, err = checkout.Merge(ctx, message); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fail(ReasonGit, err)
		}
	}
	if len(conflicts) > 0 || o.Checks {
		progress("fixing")
		scratch := filepath.Join(dir, "tmp")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return out, fail(ReasonGit, err)
		}
		text, err := buildPrompt(ctx, task{Repository: o.Repository.String(), Number: o.Number, Title: o.Title, HeadRef: o.HeadRef, BaseRef: o.BaseRef, Conflicts: conflicts, Merged: o.Conflicts && len(conflicts) == 0, Checks: o.Evidence}, o.Conflicts, o.Checks)
		if err != nil {
			return out, fail(ReasonBackend, err)
		}
		wall := o.WallClock
		if wall <= 0 {
			wall = defaultWallClock
		}
		runCtx, cancel := context.WithTimeout(ctx, wall)
		result, err := o.Agent.Fix(runCtx, agent.Request{Logger: o.Logger, Directory: checkout.Dir(), Scratch: scratch, Prompt: text, MaxCalls: o.MaxCalls, MaxTokens: o.MaxTokens})
		timedOut := runCtx.Err() != nil
		cancel()
		out.Agent = &result
		out.Summary = string(result.Document)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			// A provider's own error text is logged, never kept in the job.
			if o.Logger != nil {
				o.Logger.Warn("fix agent failed", "error", err)
			}
			if timedOut || strings.HasPrefix(err.Error(), agent.GenerationBudgetExceededReason("")) {
				return out, fail(ReasonBudget, errors.New("the agent reached its time, call or token limit before it finished"))
			}
			return out, fail(ReasonBackend, errors.New("the agent or its provider failed before it finished"))
		}
	}
	progress("committing")
	if left := markers(checkout.Dir(), conflicts); len(left) > 0 {
		return out, fail(ReasonConflictsRemain, fmt.Errorf("conflict markers remain in %s", strings.Join(left, ", ")))
	}
	head, err := checkout.Commit(ctx, message)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fail(ReasonGit, err)
	}
	if head == o.HeadSHA {
		return out, fail(ReasonNoChanges, errors.New("the agent changed nothing, so there is nothing to push"))
	}
	descends, err := checkout.Descends(ctx, head)
	if err != nil {
		return out, fail(ReasonGit, err)
	}
	if !descends {
		return out, fail(ReasonHistoryRewritten, errors.New("the result does not build on the pull request's head; baum never rewrites a branch's history"))
	}
	progress("pushing")
	if err := checkout.Push(ctx, head); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(err, gitlocal.ErrPushRejected) {
			return out, fail(ReasonPushRejected, err)
		}
		return out, fail(ReasonPushFailed, err)
	}
	out.Commit = head
	return out, nil
}

// openForRemote opens the clone with the remote whose URL, as configured or
// as git rewrites it, names repository, origin first, as the registry files a
// clone.
func openForRemote(ctx context.Context, clone string, repository github.Repository) (*gitlocal.Repo, error) {
	repo, err := gitlocal.Open(ctx, clone, gitlocal.Options{})
	if err != nil {
		return nil, fail(ReasonGit, err)
	}
	remotes, err := repo.RemoteURLs(ctx)
	if err != nil {
		_ = repo.Close()
		return nil, fail(ReasonGit, err)
	}
	names := func(remote string) bool {
		named, err := github.RepositoryFromRemote(remote)
		return err == nil && strings.EqualFold(named.String(), repository.String())
	}
	name := ""
	for _, remote := range remotes {
		if !names(remote.URL) {
			rewritten, err := repo.RemoteFetchURL(ctx, remote.Name)
			if err != nil || !names(rewritten) {
				continue
			}
		}
		if name == "" || remote.Name == gitlocal.DefaultRemote {
			name = remote.Name
		}
	}
	switch name {
	case "":
		_ = repo.Close()
		return nil, fail(ReasonNoRemote, fmt.Errorf("the clone has no remote for %s", repository))
	case gitlocal.DefaultRemote:
		return repo, nil
	}
	_ = repo.Close()
	if repo, err = gitlocal.Open(ctx, clone, gitlocal.Options{Remote: name}); err != nil {
		return nil, fail(ReasonGit, err)
	}
	return repo, nil
}

func commitMessage(o Options) string {
	agentName := o.Backend
	if o.Model != "" {
		agentName += ", " + o.Model
	}
	switch {
	case o.Conflicts && o.Checks:
		return fmt.Sprintf("Merge branch '%s' into %s\n\nConflicts resolved and failing checks fixed by an agent (%s) through baum, on request.", o.BaseRef, o.HeadRef, agentName)
	case o.Conflicts:
		return fmt.Sprintf("Merge branch '%s' into %s\n\nConflicts resolved by an agent (%s) through baum, on request.", o.BaseRef, o.HeadRef, agentName)
	}
	return fmt.Sprintf("Fix failing checks\n\nChanged by an agent (%s) through baum, on request.", agentName)
}

// markers lists the paths, of those given, whose file in dir still has a
// conflict marker line. Only regular files are read, up to 8 MiB each.
func markers(dir string, paths []string) []string {
	var left []string
	for _, path := range paths {
		full := filepath.Join(dir, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(full)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") || line == "<<<<<<<" || line == ">>>>>>>" {
				left = append(left, path)
				break
			}
		}
		_ = file.Close()
	}
	return left
}

// sweep removes fix directories an interrupted daemon left behind. The
// clone's registration of such a checkout is pruned by the next fix in it.
func sweep(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && time.Since(info.ModTime()) > staleAfter {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

// Package fix runs a fix a person asked for on one of their pull requests: an
// agent resolves its merge conflicts, fixes its failing checks or addresses
// its unresolved review comments in a temporary checkout of its branch, and
// baum commits the result and pushes it. After the push, baum replies on each
// review thread the agent addressed, with text it composes itself, and
// resolves the thread (see threads.go).
//
// The agent runs commands in its backend's sandbox (see agent.Fixer): it may
// write to the checkout, a git repository of its own with the base branch as
// origin/<base>, and a scratch directory, reach the network when the
// fix_network preference allows it, and never gets git credentials. baum
// itself fetches the branch and the base, starts the merge of the base
// branch for a conflict, makes its own commit of the files the agent left (a
// merge commit when the agent's result merges the base; no hook, and nothing
// from the agent's repository, runs), refuses a result that does not descend
// from the head the fix was confirmed for or that adds a credential, a
// nested repository or a conflict marker (see guard.go), and pushes with a
// plain, never forced, git push using the user's own git credentials for the
// clone's remote. Everything lives in a directory under baum's cache
// directory, removed when the fix ends however it ends (gitlocal.Fix).
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
	"syscall"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Why a fix cannot run, found before it is queued.
const (
	ReasonFork               = "fork"
	ReasonNoPushAccess       = "no_push_access"
	ReasonNothingToFix       = "nothing_to_fix"
	ReasonNoChat             = "chat_unavailable"
	ReasonChatOutdated       = "chat_outdated"
	ReasonUnsupportedBackend = "unsupported_backend"
)

// Why a fix that ran failed.
const (
	ReasonHeadChanged      = "head_changed"
	ReasonThreadsChanged   = "threads_changed"
	ReasonChatChanged      = "chat_changed"
	ReasonNoRemote         = "no_remote"
	ReasonGit              = "git"
	ReasonSigning          = "signing"
	ReasonBackend          = "backend"
	ReasonSignedOut        = "signed_out"
	ReasonBudget           = "budget"
	ReasonNoChanges        = "no_changes"
	ReasonConflictsRemain  = "conflicts_remain"
	ReasonHistoryRewritten = "history_rewritten"
	ReasonWorkflowChanged  = "workflow_changed"
	// ReasonCredentialAdded: the result adds what looks like a credential.
	ReasonCredentialAdded = "credential_added"
	// ReasonNestedRepository: the result adds or changes a gitlink, which
	// is what git records for a repository the agent made in the checkout.
	ReasonNestedRepository = "nested_repository"
	ReasonPushRejected     = "push_rejected"
	// ReasonWorkflowScope: GitHub refused the push because the git
	// credential may not change workflows (an OAuth or personal token
	// without the workflow scope, an app without the workflows permission).
	ReasonWorkflowScope = "workflow_scope"
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
	// Threads are the review threads to address; Replies posts baum's
	// replies on those the agent addressed, after the push.
	Threads []Thread
	// Chat, when set, is a reviewer's conversation with the agent that wrote
	// the review, describing a change to make: untrusted data in the prompt.
	Chat    *ChatContext
	Replies ThreadWriter
	Agent   agent.Fixer
	// Backend and Model name the agent in the commit message.
	Backend, Model string
	// MaxCalls and MaxTokens bound the agent when set; zero is no limit.
	// WallClock is its time limit, defaultWallClock when unset.
	MaxCalls, MaxTokens int
	WallClock           time.Duration
	// Logger receives how the agent ended, its error included, which the
	// job keeps no text of; nil logs nothing.
	Logger *slog.Logger
	// Directory holds the fixes' temporary directories; empty is
	// DefaultDirectory.
	Directory string
	// Progress receives the phase the fix is in.
	Progress func(phase string)
	// Env are the user's agent variables for the git commands of the fix
	// (gitlocal.FixOptions.Env).
	Env []string
	// Network lets the agent's commands reach the network
	// (agent.Request.Network): the fix_network preference, on by default.
	Network bool
	// AllowWorkflows lets the fix push changes to GitHub workflows and
	// actions whatever was in its prompt: baum's GitHub connection has
	// workflow access (Reconnect with workflow access in Settings), or which
	// access the credential has is unknown (see guard). Without it, a fix
	// whose prompt held review comments or a conversation refuses them.
	AllowWorkflows bool
	// Prompts replace the built-in fix prompts (prompt.FixWith); nil keeps
	// them. The texts must already be validated.
	Prompts prompt.Overrides
	// Transcript, when set, receives the agent's steps (agent.Request.Transcript).
	Transcript func(agent.TranscriptEntry)
}

func (o Options) hasChat() bool { return o.Chat != nil && len(o.Chat.Turns) > 0 }

// Outcome is what a fix did.
type Outcome struct {
	// Commit is the commit pushed to the pull request's branch.
	Commit string
	// Summary is the agent's final message, without its answer block about
	// review threads.
	Summary string
	// Agent is the agent's run, nil when none was needed (a base branch
	// that merged cleanly).
	Agent *agent.Result
	// Threads is what became of each review thread, in order, once the
	// push succeeded.
	Threads []ThreadOutcome
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

// limitText is a time limit as a person reads it: "30 minutes".
func limitText(d time.Duration) string {
	if d%time.Minute != 0 {
		return d.String()
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

// defaultWallClock bounds an agent run whose Options set no WallClock.
const defaultWallClock = 30 * time.Minute

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
	// Every fix fetches the base branch: the agent sees it as origin/<base>,
	// since CI on pull_request builds the merge with it.
	checkout, err := repo.StartFix(ctx, gitlocal.FixOptions{Dir: dir, ID: o.ID, HeadRef: o.HeadRef, HeadSHA: o.HeadSHA, BaseRef: o.BaseRef, Env: o.Env})
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
	// A commit that cannot be signed fails before the agent's work is paid for.
	if err := checkout.CheckSigning(ctx); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fail(ReasonSigning, fmt.Errorf("git could not sign a commit, so the agent did not run: %w", err))
	}
	var conflicts []string
	if o.Conflicts {
		progress("merging")
		if conflicts, err = checkout.Merge(ctx, commitMessage(o, true)); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fail(ReasonGit, err)
		}
	}
	answers := map[string]Answer{}
	if len(conflicts) > 0 || o.Checks || len(o.Threads) > 0 || o.hasChat() {
		progress("fixing")
		scratch := filepath.Join(dir, "tmp")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return out, fail(ReasonGit, err)
		}
		text, tag, err := buildPrompt(ctx, task{Repository: o.Repository.String(), Number: o.Number, Title: o.Title, HeadRef: o.HeadRef, BaseRef: o.BaseRef, Conflicts: conflicts, Merged: o.Conflicts && len(conflicts) == 0, Checks: o.Evidence, Threads: o.Threads, Chat: o.Chat, Prompts: o.Prompts, Network: o.Network, Scratch: scratch}, o.Conflicts, o.Checks)
		if err != nil {
			return out, fail(ReasonBackend, err)
		}
		wall := o.WallClock
		if wall <= 0 {
			wall = defaultWallClock
		}
		runCtx, cancel := context.WithTimeout(ctx, wall)
		// The agent's steps go to the transcript, not the log.
		started := time.Now()
		result, err := o.Agent.Fix(runCtx, agent.Request{Directory: checkout.Dir(), Scratch: scratch, GitDir: checkout.GitDir(), Network: o.Network, Logger: o.Logger,
			Prompt: text, MaxCalls: o.MaxCalls, MaxTokens: o.MaxTokens, Transcript: o.Transcript})
		timedOut := runCtx.Err() != nil
		cancel()
		if o.Logger != nil {
			attrs := []any{"duration", time.Since(started).Round(time.Second), "time_limit", wall, "calls", result.Calls, "tokens", result.Tokens}
			if err != nil {
				// A provider's own error text is logged, never kept in the job.
				o.Logger.Warn("fix agent failed", append(attrs, "timed_out", timedOut, "error", err)...)
			} else {
				o.Logger.Info("fix agent finished", attrs...)
			}
		}
		out.Agent = &result
		out.Summary = string(result.Document)
		if len(o.Threads) > 0 {
			out.Summary, answers = parseAnswer(ctx, out.Summary, tag, o.Threads)
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if timedOut {
				return out, fail(ReasonBudget, fmt.Errorf("the agent reached its time limit (%s) before it finished; raise it in Settings", limitText(wall)))
			}
			if spent, ok := strings.CutPrefix(err.Error(), agent.GenerationBudgetExceededReason("")); ok {
				// Only the budget's name is kept: text after it may be the
				// provider's.
				limit := "call or token"
				switch {
				case strings.HasPrefix(spent, "model calls"):
					limit = "model call"
				case strings.HasPrefix(spent, "tokens"):
					limit = "token"
				}
				return out, fail(ReasonBudget, fmt.Errorf("the agent reached its %s limit before it finished", limit))
			}
			if agent.SignInRejected(err) {
				return out, fail(ReasonSignedOut, errors.New("the agent is not signed in, or its provider refused its sign-in"))
			}
			return out, fail(ReasonBackend, errors.New("the agent or its provider failed before it finished; the baum daemon log has the error"))
		}
	}
	progress("committing")
	if left := markers(checkout.Dir(), conflicts); len(left) > 0 {
		return out, fail(ReasonConflictsRemain, fmt.Errorf("conflict markers remain in %s", strings.Join(left, ", ")))
	}
	merged, err := checkout.Merged(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fail(ReasonGit, err)
	}
	if o.Conflicts && !merged {
		return out, fail(ReasonConflictsRemain, errors.New("the agent abandoned the merge of the base branch, so the conflicts remain; nothing was pushed"))
	}
	head, err := checkout.Commit(ctx, commitMessage(o, merged))
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
	if err := guard(ctx, checkout, head, (len(o.Threads) > 0 || o.hasChat()) && !o.AllowWorkflows); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, err
	}
	progress("pushing")
	if err := checkout.Push(ctx, head); err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(err, gitlocal.ErrWorkflowScope) {
			return out, fail(ReasonWorkflowScope, errors.New("GitHub refused the push because your git credential cannot change workflows; give it the workflow scope (for the GitHub CLI: gh auth refresh -s workflow) and try again"))
		}
		if errors.Is(err, gitlocal.ErrPushRejected) {
			return out, fail(ReasonPushRejected, err)
		}
		return out, fail(ReasonPushFailed, err)
	}
	out.Commit = head
	if len(o.Threads) > 0 && o.Replies != nil {
		progress("replying")
		out.Threads = reply(ctx, o.Replies, o.Repository, o.Number, o.Threads, answers, head)
	}
	return out, nil
}

// OpenForFetch opens the clone with the remote whose fetch URL, as configured
// or as git rewrites it, names repository, origin first, as the registry files
// a clone, for a read that only fetches: where it pushes does not matter. Its
// error is an *Error; the reason is ReasonNoRemote when no remote names the
// repository.
func OpenForFetch(ctx context.Context, clone string, repository github.Repository) (*gitlocal.Repo, error) {
	return openNamed(ctx, clone, repository, false)
}

// openForRemote opens the clone with the remote whose URL, as configured or
// as git rewrites it, names repository, origin first, as the registry files a
// clone, and that pushes only to it.
func openForRemote(ctx context.Context, clone string, repository github.Repository) (*gitlocal.Repo, error) {
	return openNamed(ctx, clone, repository, true)
}

// openNamed opens the clone with the remote naming repository, one that also
// pushes only to it when push is set.
func openNamed(ctx context.Context, clone string, repository github.Repository, push bool) (*gitlocal.Repo, error) {
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
		// Every URL git pushes to must name the repository too, as configured
		// or as rewritten, the same trust the fetch URL gets: a pushurl to a
		// fork would otherwise take the fix and leave the pull request as it was.
		if push && !pushesTo(ctx, repo, remote.Name, names) {
			continue
		}
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

// pushesTo reports whether every URL remote pushes to, as configured or as
// rewritten, names the repository.
func pushesTo(ctx context.Context, repo *gitlocal.Repo, remote string, names func(string) bool) bool {
	configured, effective, err := repo.RemotePushURLs(ctx, remote)
	if err != nil || len(effective) == 0 || len(configured) != len(effective) {
		return false
	}
	for i := range effective {
		if !names(configured[i]) && !names(effective[i]) {
			return false
		}
	}
	return true
}

// commitMessage is the message of baum's commit; merged says it merges the
// base branch, which a conflicts fix always does and any other fix does when
// its agent merged the base.
func commitMessage(o Options, merged bool) string {
	agentName := o.Backend
	if o.Model != "" {
		agentName += ", " + o.Model
	}
	var done []string
	if o.Conflicts {
		done = append(done, "Conflicts resolved")
	} else if merged {
		done = append(done, "base branch merged")
	}
	if o.Checks {
		done = append(done, "failing checks fixed")
	}
	if len(o.Threads) > 0 {
		done = append(done, "review comments addressed")
	}
	if o.hasChat() {
		done = append(done, "change from a conversation made")
	}
	if len(done) == 0 {
		done = append(done, "changed")
	}
	what := done[0]
	if len(done) > 1 {
		what = strings.Join(done[:len(done)-1], ", ") + " and " + done[len(done)-1]
	}
	what = strings.ToUpper(what[:1]) + what[1:]
	switch {
	case o.Conflicts || merged:
		return fmt.Sprintf("Merge branch '%s' into %s\n\n%s by an agent (%s) through baum, on request.", o.BaseRef, o.HeadRef, what, agentName)
	case o.hasChat():
		return fmt.Sprintf("Make the change from a conversation\n\nChanged by an agent (%s) through baum, on request.", agentName)
	case o.Checks && len(o.Threads) > 0:
		return fmt.Sprintf("Fix failing checks and address review comments\n\n%s by an agent (%s) through baum, on request.", what, agentName)
	case o.Checks:
		return fmt.Sprintf("Fix failing checks\n\nChanged by an agent (%s) through baum, on request.", agentName)
	}
	return fmt.Sprintf("Address review comments\n\nChanged by an agent (%s) through baum, on request.", agentName)
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
		// Opened without blocking and checked again: a process the agent
		// left could have swapped a FIFO in.
		file, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			if marker(scanner.Text()) {
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

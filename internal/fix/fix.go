// Package fix runs a fix a person asked for on one of their pull requests: an
// agent resolves its merge conflicts, fixes its failing checks or addresses
// its unresolved review comments in a temporary checkout of its branch, and
// baum commits the result and pushes it. After the push, baum replies on each
// review thread the agent addressed, with text it composes itself, and
// resolves the thread (see threads.go).
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
	ReasonBudget           = "budget"
	ReasonNoChanges        = "no_changes"
	ReasonConflictsRemain  = "conflicts_remain"
	ReasonHistoryRewritten = "history_rewritten"
	ReasonWorkflowChanged  = "workflow_changed"
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
	base := ""
	if o.Conflicts {
		base = o.BaseRef
	}
	checkout, err := repo.StartFix(ctx, gitlocal.FixOptions{Dir: dir, ID: o.ID, HeadRef: o.HeadRef, HeadSHA: o.HeadSHA, BaseRef: base, Env: o.Env})
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
	answers := map[string]Answer{}
	if len(conflicts) > 0 || o.Checks || len(o.Threads) > 0 || o.hasChat() {
		progress("fixing")
		scratch := filepath.Join(dir, "tmp")
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return out, fail(ReasonGit, err)
		}
		text, tag, err := buildPrompt(ctx, task{Repository: o.Repository.String(), Number: o.Number, Title: o.Title, HeadRef: o.HeadRef, BaseRef: o.BaseRef, Conflicts: conflicts, Merged: o.Conflicts && len(conflicts) == 0, Checks: o.Evidence, Threads: o.Threads, Chat: o.Chat}, o.Conflicts, o.Checks)
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
		result, err := o.Agent.Fix(runCtx, agent.Request{Directory: checkout.Dir(), Scratch: scratch, Prompt: text, MaxCalls: o.MaxCalls, MaxTokens: o.MaxTokens, Transcript: o.Transcript})
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
			return out, fail(ReasonBackend, errors.New("the agent or its provider failed before it finished; the baum daemon log has the error"))
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
	if (len(o.Threads) > 0 || o.hasChat()) && !o.Conflicts && !o.Checks {
		// A fix of review comments or of a conversation never pushes a
		// workflow change: they are other people's or an agent's words, and
		// a workflow runs with the repository's secrets as soon as it is
		// pushed.
		changed, err := checkout.Changed(ctx, head)
		if err != nil {
			return out, fail(ReasonGit, err)
		}
		if paths := workflows(changed); len(paths) > 0 {
			return out, fail(ReasonWorkflowChanged, fmt.Errorf("the agent changed GitHub workflow or action files (%s), which a fix of review comments or a conversation never pushes", strings.Join(paths, ", ")))
		}
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
	if len(o.Threads) > 0 && o.Replies != nil {
		progress("replying")
		out.Threads = reply(ctx, o.Replies, o.Repository, o.Number, o.Threads, answers, head)
	}
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
		// Every URL git pushes to must name the repository too, as configured
		// or as rewritten, the same trust the fetch URL gets: a pushurl to a
		// fork would otherwise take the fix and leave the pull request as it was.
		configured, effective, err := repo.RemotePushURLs(ctx, remote.Name)
		if err != nil || len(effective) == 0 || len(configured) != len(effective) {
			continue
		}
		pushes := true
		for i := range effective {
			if !names(configured[i]) && !names(effective[i]) {
				pushes = false
				break
			}
		}
		if !pushes {
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

func commitMessage(o Options) string {
	agentName := o.Backend
	if o.Model != "" {
		agentName += ", " + o.Model
	}
	var done []string
	if o.Conflicts {
		done = append(done, "Conflicts resolved")
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
	case o.Conflicts:
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

// workflows lists the paths, of those given, that are GitHub Actions
// workflows or repository actions, at most five.
func workflows(paths []string) []string {
	var found []string
	for _, path := range paths {
		lower := strings.ToLower(path)
		if strings.HasPrefix(lower, ".github/workflows/") || strings.HasPrefix(lower, ".github/actions/") {
			found = append(found, path)
		}
	}
	return found[:min(len(found), 5)]
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

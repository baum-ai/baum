package baumcli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/proc"
)

var entireGitHookArgumentCounts = map[string]int{
	"commit-msg":         1,
	"post-commit":        0,
	"post-rewrite":       1,
	"pre-push":           1,
	"prepare-commit-msg": 2,
}

func newHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Install baum's coding-agent hooks, or run them",
	}
	gitCommand := &cobra.Command{
		Use:                "git HOOK [args...]",
		Short:              "Run a baum Git hook",
		Hidden:             true,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "--check" {
				if len(args) != 2 {
					return errors.New("usage: baum hooks git --check HOOK")
				}
				if _, ok := entireGitHookArgumentCounts[args[1]]; !ok {
					return fmt.Errorf("unsupported Git hook %q", args[1])
				}
				return hooksEnabled(cmd.Context(), ".")
			}
			entireArgs, ok := entireGitHookArgs(args)
			if !ok {
				return nil
			}
			// Git writes refs to a pre-push hook's input and rewritten
			// commits to a post-rewrite hook's; the others get none.
			var input *hookInput
			if args[0] == "pre-push" || args[0] == "post-rewrite" {
				input = newHookInput(cmd.InOrStdin())
			}
			hosted := hostedHooksEnabled(cmd.Context(), ".") == nil
			err := runEntireGitHook(cmd, hosted, args, entireArgs, input, stdout, stderr)
			event := newHookEvent(hookevents.KindGit, "", args[0])
			event.Git = gitHookEvent(args, input.payload())
			event.Exit = exitStatus(err)
			sendHookEvent(cmd.Context(), event)
			return err
		},
	}
	gitCommand.SetOut(stdout)
	gitCommand.SetErr(stderr)
	cmd.AddCommand(gitCommand)
	cmd.AddCommand(newAgentHooksCommand(stdout, stderr))
	cmd.AddCommand(newBootstrapHooksCommand(stdout, stderr))
	cmd.AddCommand(newInstallUserHooksCommand(stdout, stderr))
	cmd.AddCommand(newUninstallUserHooksCommand(stdout, stderr))
	return cmd
}

// newAgentHooksCommand runs a coding agent's hook. Unlike the Git hooks, the
// agent hook protocol belongs to the agent: standard input carries its payload,
// standard output carries the reply it parses, and the exit code decides what
// the agent does next, so all four are passed through untouched.
func newAgentHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "agent AGENT EVENT",
		Short:              "Run a baum coding-agent hook",
		Hidden:             true,
		Args:               cobra.MinimumNArgs(2),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "--check" {
				userWide := len(args) > 1 && args[1] == "--user"
				if userWide {
					args = args[1:]
				}
				if len(args) != 3 {
					return errors.New("usage: baum hooks agent --check [--user] AGENT EVENT")
				}
				if err := validateAgentHook(args[1], args[2]); err != nil {
					return err
				}
				if userWide {
					return userHooksEnabled(cmd.Context(), ".", args[1], args[2])
				}
				return hooksEnabled(cmd.Context(), ".")
			}
			if len(args) != 2 {
				return errors.New("usage: baum hooks agent AGENT EVENT")
			}
			agent, event := args[0], args[1]
			if err := validateAgentHook(agent, event); err != nil {
				return err
			}
			input := newHookInput(cmd.InOrStdin())
			hosted := hostedHooksEnabled(cmd.Context(), ".") == nil
			err := runEntireAgentHook(cmd, hosted, agent, event, input, stdout, stderr)
			hookEvent := newHookEvent(hookevents.KindAgent, agent, event)
			hookEvent.Session = agentSession(input.identifiers())
			hookEvent.Exit = exitStatus(err)
			sendHookEvent(cmd.Context(), hookEvent)
			return err
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

// hookEntire finds the Entire a hook hands its event to. A hosted hook also
// finds the one baum installs outside PATH; a local one finds only what the
// fall-through command would, on PATH.
func hookEntire(hosted bool) (string, error) {
	if hosted {
		return resolveEntireBinary()
	}
	return lookPath("entire")
}

// runEntireGitHook hands a Git hook to Entire. Only a failing pre-push fails
// the hook, so the push stops; other failures and a missing Entire warn.
func runEntireGitHook(cmd *cobra.Command, hosted bool, args, entireArgs []string, input *hookInput, stdout, stderr io.Writer) error {
	entire, err := hookEntire(hosted)
	if err != nil {
		input.read()
		_, _ = fmt.Fprintln(stderr, "[baum] Entire CLI is not installed or not on PATH. Skipping Entire Git hook; continuing.")
		return nil
	}
	entireCommand := exec.CommandContext(cmd.Context(), entire, entireArgs...)
	entireCommand.Stdout = stdout
	entireCommand.Stderr = stderr
	if err := runDelegated(entireCommand, cmd.InOrStdin(), input); err != nil {
		if args[0] == "pre-push" {
			return fmt.Errorf("Entire pre-push hook failed: %w", err)
		}
		_, _ = fmt.Fprintf(stderr, "[baum] Entire Git hook failed; continuing: %s: %v\n", strings.Join(entireCommand.Args, " "), err)
	}
	return nil
}

// runEntireAgentHook runs the session-start bootstrap of a hosted hook, then
// hands the agent hook to Entire and returns its exit status as an
// ExitError. A local hook (see hooksEnabled) skips the bootstrap, so it does
// exactly what the fall-through command would.
func runEntireAgentHook(cmd *cobra.Command, hosted bool, agent, event string, input *hookInput, stdout, stderr io.Writer) error {
	if hosted && event == entireAgentSessionStart {
		// Session start precedes any commit in the session, so this is
		// where a fresh clone gets the hooks its first commit needs.
		bootstrapContext, cancel := context.WithTimeout(cmd.Context(), sessionStartBootstrapTimeout)
		defer cancel()
		bootstrapHooks(bootstrapContext, ".", stderr)
	}
	entire, err := hookEntire(hosted)
	if err != nil {
		input.read()
		spec, _ := lookupAgentHookSpec(agent)
		if notice := missingEntireNotice(spec, event); notice != "" {
			_, _ = fmt.Fprintln(stdout, notice)
		}
		return nil
	}
	entireCommand := exec.CommandContext(cmd.Context(), entire, "hooks", agent, event)
	entireCommand.Stdout = stdout
	entireCommand.Stderr = stderr
	err = runDelegated(entireCommand, cmd.InOrStdin(), input)
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		_, _ = fmt.Fprintf(stderr, "[baum] Entire agent hook could not run; continuing: %s: %v\n", strings.Join(entireCommand.Args, " "), err)
		return nil
	}
	code := exitErr.ExitCode()
	if code < 0 {
		code = 1
	}
	return &ExitError{Code: code}
}

// runDelegated runs a delegated command on the hook's input. With input it
// reads a pipe that input fills, so the hook keeps a copy for its event.
func runDelegated(command *exec.Cmd, in io.Reader, input *hookInput) error {
	command.Stdin = in
	var pipe *os.File
	if input != nil {
		if r, err := input.pipe(); err == nil {
			pipe = r
			command.Stdin = r
		}
	}
	err := command.Start()
	if pipe != nil {
		// The command holds its own copy; keeping this one open would keep
		// the copy blocked on a command that stopped reading.
		_ = pipe.Close()
	}
	if err != nil {
		return err
	}
	return command.Wait()
}

// errHostedHooksOff is the `--check` answer where the user has neither opted
// in to the hosted service nor run the baum daemon. The dispatchers committed
// to repositories and installed as Git hooks run baum only when `--check`
// succeeds, and otherwise run Entire directly, so such a user's hooks behave
// as if baum were not installed.
var errHostedHooksOff = errors.New("baum hooks are off here: run `baum login`, or `baum setup` in this clone, to opt in to the hosted service, or start the baum daemon")

// hooksEnabled answers `--check`: the hooks run through baum where the hosted
// service was opted in to (hostedHooksEnabled), or where the user has a baum
// daemon, whose state directory exists once it ran. Where only the second
// holds, a hook is local: it does nothing hosted (no bootstrap, no Entire
// that baum installed), hands the event to Entire exactly as the fall-through
// command would, and sends the event to the daemon. The daemon's directory is
// found as the daemon finds it, without contacting it.
func hooksEnabled(ctx context.Context, dir string) error {
	if daemonStateExists() {
		return nil
	}
	return hostedHooksEnabled(ctx, dir)
}

// daemonStateExists reports whether the user's daemon state directory exists.
func daemonStateExists() bool {
	paths, err := hookDaemonPaths()
	if err != nil {
		return false
	}
	info, err := os.Stat(paths.State)
	return err == nil && info.IsDir()
}

// hostedHooksEnabled reports whether the hooks of the clone containing dir
// should run through baum. They do when the user is logged in (BAUM_TOKEN or
// stored credentials, for any server), or when this clone was set up by
// `baum setup` or bootstrapped, which leaves a marker in its Git directory.
// A baum installed for the hosted service (hostedInstall) also runs them in an
// onboarded repository: one whose agent settings file committed at HEAD
// carries baum's dispatcher, as the onboarding pull request and `baum setup`
// write it. That keeps the onboarding promise for a teammate who never logged
// in, while an open-source baum never sets up Entire, whose hooks push
// transcripts, in a repository just because its files ask for it.
//
// The last two only let the hooks install Entire and the Git hook dispatchers
// and delegate to Entire; nothing a hook does contacts a baum server, and
// every command that does still needs a login. A repository is judged by its
// committed files, not by a hook having run, so a hook configured outside the
// repository falls through to Entire in a repository never onboarded. Only
// local state is read.
func hostedHooksEnabled(ctx context.Context, dir string) error {
	if loggedIn() {
		return nil
	}
	if hostedClone(ctx, dir) || (hostedInstall() && onboardedRepository(ctx, dir)) {
		return nil
	}
	return errHostedHooksOff
}

// loggedIn reports a login for any server: BAUM_TOKEN or a stored token.
func loggedIn() bool {
	if os.Getenv(tokenEnvVar) != "" {
		return true
	}
	creds, ok, err := loadCredentials()
	return err == nil && ok && creds.Token != ""
}

// hostedClone reports whether the clone containing dir carries the marker of
// a `baum setup` or of a hook bootstrap, in the worktree's own Git directory
// or in the common one, so every linked worktree of a set-up clone counts.
func hostedClone(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, gitDir := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		for _, name := range []string{setupMarkerName, bootstrapMarkerName} {
			info, err := os.Stat(filepath.Join(gitDir, "baum", name))
			if err == nil && info.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}

// Bounds on reading a repository's committed agent settings on every hook.
const (
	onboardingReadTimeout = 5 * time.Second
	maxSettingsFileBytes  = 1 << 20
)

// onboardedRepository reports whether an agent settings file committed at
// HEAD in the repository containing dir carries baum's dispatcher. One git
// process reads every settings file, missing ones included, under a timeout,
// without fetching missing objects or prompting, and skipping a file larger
// than maxSettingsFileBytes unread.
func onboardedRepository(ctx context.Context, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, onboardingReadTimeout)
	defer cancel()
	var request strings.Builder
	for _, spec := range agenthooks.All() {
		request.WriteString("HEAD:" + spec.SettingsFile + "\n")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	// GIT_NO_LAZY_FETCH (git 2.44 and later) keeps a partial clone from
	// fetching a missing blob. Older git ignores it, so the timeout kills the
	// whole process group: a fetch git started cannot outlive it or hold the
	// pipe open.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	proc.Configure(cmd)
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(request.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	found := readsDispatcher(bufio.NewReader(stdout))
	cancel()
	_ = cmd.Wait()
	return found
}

// readsDispatcher reads `git cat-file --batch` output and reports whether a
// blob of at most maxSettingsFileBytes carries baum's dispatcher.
func readsDispatcher(reader *bufio.Reader) bool {
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		// <oid> <type> <size>, or <object> missing.
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return false
		}
		if fields[1] != "blob" || size > maxSettingsFileBytes {
			if _, err := io.CopyN(io.Discard, reader, size+1); err != nil {
				return false
			}
			continue
		}
		contents := make([]byte, size+1)
		if _, err := io.ReadFull(reader, contents); err != nil {
			return false
		}
		if agenthooks.ContainsDispatcher(contents[:size]) {
			return true
		}
	}
}

func entireGitHookArgs(args []string) ([]string, bool) {
	argumentCount, ok := entireGitHookArgumentCounts[args[0]]
	if !ok {
		return nil, false
	}
	result := []string{"hooks", "git", args[0]}
	for i := 0; i < argumentCount; i++ {
		value := ""
		if i+1 < len(args) {
			value = args[i+1]
		}
		result = append(result, value)
	}
	return result, true
}

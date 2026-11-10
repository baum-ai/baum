package hookcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/agenthookdefs"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

var gitHookArgumentCounts = map[string]int{
	"post-commit":  0,
	"post-rewrite": 1,
	"pre-push":     1,
}

// sessionEnv are the variables a coding agent exports to the commands it runs,
// so a Git hook such as post-commit names the session that ran Git. An agent
// not listed here is found through the hook's ancestor processes instead.
var sessionEnv = []struct{ agent, name string }{
	{"claude-code", "CLAUDE_CODE_SESSION_ID"},
}

// maxSessionID bounds an environment session id, like a stored observation's.
const maxSessionID = 128

func newHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "hooks", Short: "Manage baum's local coding-agent plugins", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(newGitHooksCommand(), newAgentHooksCommand(stdout),
		newInstallUserHooksCommand(stdout, stderr), newUninstallUserHooksCommand(stdout, stderr),
		newUpdateUserHooksCommand(stdout, stderr), newUserHooksStatusCommand(stdout))
	return cmd
}

func newGitHooksCommand() *cobra.Command {
	return &cobra.Command{Use: "git HOOK [args...]", Short: "Record a local Git event", Hidden: true,
		Args: cobra.MinimumNArgs(1), DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "--check":
				// Dispatchers of earlier versions fall back to their own hooks.
				if len(args) != 2 {
					return errors.New("usage: baum hooks git --check HOOK")
				}
				if _, ok := gitHookArgumentCounts[args[1]]; !ok {
					return fmt.Errorf("unsupported Git hook %q", args[1])
				}
				return errors.New("repository Git hook dispatchers are no longer supported")
			case "--check-dispatcher":
				// The check of the dispatchers the daemon installs.
				if len(args) != 2 {
					return errors.New("usage: baum hooks git --check-dispatcher HOOK")
				}
				if _, ok := gitHookArgumentCounts[args[1]]; !ok {
					return fmt.Errorf("unsupported Git hook %q", args[1])
				}
				return hooksEnabled()
			}
			if _, ok := gitHookArgumentCounts[args[0]]; !ok {
				return fmt.Errorf("unsupported Git hook %q", args[0])
			}
			started := time.Now()
			var input *hookInput
			if args[0] == "pre-push" || args[0] == "post-rewrite" {
				input = newHookInput(cmd.InOrStdin())
				input.read()
			}
			event := newHookEvent(hookevents.KindGit, "", args[0], started)
			event.Git = gitHookEvent(args, input.payload())
			if tiesToSession(args[0]) {
				event.Agent, event.Session = environmentSession(os.Getenv)
				if processes := captureAncestors(); len(processes) > 0 {
					event.Terminal = &hookevents.Terminal{Processes: processes}
				}
			}
			if args[0] == "post-commit" {
				if head, branch := captureCommit(cmd.Context()); head != "" {
					if event.Git == nil {
						event.Git = &hookevents.Git{}
					}
					event.Git.Head, event.Git.Branch = head, branch
				}
			}
			sendHookEvent(cmd.Context(), event)
			return nil
		},
	}
}

func newAgentHooksCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{Use: "agent [--user] AGENT EVENT", Short: "Record a local coding-agent event", Hidden: true,
		Args: cobra.MinimumNArgs(2), DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			check := args[0] == "--check"
			if check {
				args = args[1:]
			}
			userWide := len(args) > 0 && args[0] == "--user"
			if userWide {
				args = args[1:]
			}
			if len(args) != 2 {
				return errors.New("usage: baum hooks agent [--check] [--user] AGENT EVENT")
			}
			agent, event := args[0], args[1]
			if err := agenthookdefs.ValidateUserWide(agent, event); err != nil {
				return err
			}
			if check {
				// Legacy repository dispatchers own their fallback to independent hooks.
				if !userWide {
					return errors.New("repository agent hooks are no longer supported; use the desktop plugins")
				}
				return hooksEnabled()
			}
			started := time.Now()
			input := newHookInput(cmd.InOrStdin())
			if input != nil && readsFullInput(agent, event) {
				input.limit = maxFullHookInput
			}
			input.read()
			fullInputReady := true
			if readsFullInput(agent, event) {
				_, fullInputReady = input.complete(fullInputWait)
			}
			hookEvent := newHookEvent(hookevents.KindAgent, agent, event, started)
			hookEvent.Session = agentSession(event, input.identifiers())
			if hookEvent.Session != nil && agent == "codex" && event == permissionRequestEvent && fullInputReady {
				hookEvent.Session.AutoReview = codexAutoReview(input)
			}
			if hookEvent.Session != nil && digestsToolCall(agent, event) && fullInputReady {
				hookEvent.Session.ToolDigest = toolDigest(input)
			}
			if locatesTerminal(event) {
				hookEvent.Terminal = captureTerminal()
			}
			delivered := sendHookEvent(cmd.Context(), hookEvent)
			switch event {
			case permissionRequestEvent:
				if delivered && fullInputReady && hookEvent.Session != nil && !hookEvent.Session.AutoReview {
					answerFromApp(cmd.Context(), agent, input, started, stdout)
				}
			case stopEvent:
				finishStop(cmd.Context(), agent, hookEvent.Session, delivered, stdout)
			}
			return nil
		},
	}
}

// environmentSession is the agent session whose command ran Git, named by the
// variables its agent exports, or nothing when none is set to a plausible id.
func environmentSession(getenv func(string) string) (string, *hookevents.Session) {
	for _, v := range sessionEnv {
		id := getenv(v.name)
		if id == "" || len(id) > maxSessionID || strings.ContainsFunc(id, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':')
		}) {
			continue
		}
		return v.agent, &hookevents.Session{ID: id}
	}
	return "", nil
}

// Checks are local and never inspect credentials or third-party hook settings.
func hooksEnabled() error {
	paths, err := hookDaemonPaths()
	if err != nil {
		return err
	}
	if !daemon.HasRun(paths) {
		return errors.New("baum hooks are off: start the baum desktop app first")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("not in a Git repository")
		}
		dir = parent
	}
}

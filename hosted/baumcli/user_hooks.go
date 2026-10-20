package baumcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/hosted/userhooks"
)

func newInstallUserHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	var (
		user   bool
		agents []string
	)
	cmd := &cobra.Command{
		Use:   "install --user [--agent AGENT]...",
		Short: "Install baum's hooks in your user-wide Claude Code and Codex settings",
		Long: `Adds baum's hooks to ~/.claude/settings.json (Claude Code) and
~/.codex/hooks.json (Codex), so the agents run them in every repository. Your
other settings and hooks are kept, and baum's go after yours; the previous
file is saved in baum's state directory ($XDG_STATE_HOME/baum/backups, or
~/.local/state/baum/backups). Running it again changes nothing, and reverts
edits made to baum's own entries.

The hooks run baum only in a Git repository with Entire enabled where no other
hook the agent runs already hands the event to Entire, and only where the
committed hooks would run baum: after baum login, in a clone set up with baum
setup, or, for a hosted install, in an onboarded repository, where they hand
the event to Entire; or where you run the baum daemon, where they only record
the event locally. Everywhere else they do nothing. They never contact a baum
server. Hooks an earlier baum installed are updated when you run this again.

Without --agent, it installs for the coding agents found on this machine.
Codex runs a new or changed hook only after you trust it with /hooks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user: this installs hooks in your user-wide agent settings (baum setup installs a repository's)")
			}
			if len(agents) == 0 {
				agents = detectedUserHookAgents()
				if len(agents) == 0 {
					return fmt.Errorf("no supported coding agent found; pass --agent (%s)", strings.Join(userhooks.Agents(), " or "))
				}
			}
			return changeUserHooks(agents, true, stdout, stderr)
		},
	}
	cmd.Flags().BoolVar(&user, "user", false, "install in your user-wide agent settings")
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "coding agent, "+strings.Join(userhooks.Agents(), " or ")+" (repeatable; default: those found)")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

func newUninstallUserHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	var (
		user   bool
		agents []string
	)
	cmd := &cobra.Command{
		Use:   "uninstall --user [--agent AGENT]...",
		Short: "Remove baum's hooks from your user-wide Claude Code and Codex settings",
		Long: `Removes the hooks baum hooks install --user added to
~/.claude/settings.json and ~/.codex/hooks.json and nothing else; the previous
file is saved in baum's state directory, and a file baum created is removed
once it holds nothing else. Hooks committed to repositories are not touched.
Without --agent, it removes them for every agent. Codex keys its trust in a
hook to its position: hooks of yours that move are listed, to trust again with
/hooks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user: this removes hooks from your user-wide agent settings")
			}
			if len(agents) == 0 {
				agents = userhooks.Agents()
			}
			return changeUserHooks(agents, false, stdout, stderr)
		},
	}
	cmd.Flags().BoolVar(&user, "user", false, "remove from your user-wide agent settings")
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "coding agent, "+strings.Join(userhooks.Agents(), " or ")+" (repeatable; default: all)")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

// changeUserHooks installs or uninstalls for each agent. One agent's failure,
// such as a settings file baum refuses to edit, does not stop the others.
func changeUserHooks(agents []string, install bool, stdout, stderr io.Writer) error {
	change := userhooks.Uninstall
	if install {
		change = userhooks.Install
	}
	for _, agent := range agents {
		if !slices.Contains(userhooks.Agents(), agent) {
			return fmt.Errorf("unsupported coding agent %q; use %s", agent, strings.Join(userhooks.Agents(), " or "))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find the home directory: %w", err)
	}
	failed := 0
	for _, agent := range slices.Compact(slices.Clone(agents)) {
		result, err := change(home, agent)
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "error: %s hooks: %v\n", agentDisplayName(agent), err)
			continue
		}
		reportUserHooks(stdout, stderr, result, install)
	}
	if failed > 0 {
		return fmt.Errorf("could not change the user-wide hooks of %d %s", failed, agentNoun(failed))
	}
	return nil
}

func reportUserHooks(stdout, stderr io.Writer, result userhooks.Result, install bool) {
	name, path := agentDisplayName(result.Agent), homeRelative(result.Path)
	switch {
	case install && result.Changed:
		_, _ = fmt.Fprintf(stdout, "Installed baum's %s hooks in %s.\n", name, path)
	case install:
		_, _ = fmt.Fprintf(stdout, "baum's %s hooks in %s are up to date.\n", name, path)
	case result.Removed:
		_, _ = fmt.Fprintf(stdout, "Removed baum's %s hooks and %s, which baum had created.\n", name, path)
	case result.Changed:
		_, _ = fmt.Fprintf(stdout, "Removed baum's %s hooks from %s.\n", name, path)
	default:
		_, _ = fmt.Fprintf(stdout, "No baum %s hooks in %s.\n", name, path)
	}
	if result.Backup != "" {
		_, _ = fmt.Fprintf(stdout, "  Previous file saved as %s.\n", homeRelative(result.Backup))
	}
	if install && result.Changed && result.Agent == "codex" {
		_, _ = fmt.Fprintln(stdout, "  Codex runs a new or changed hook only after you trust it: start Codex and review baum's hooks with /hooks.")
	}
	if len(result.Shifted) > 0 {
		_, _ = fmt.Fprintf(stderr, "warning: these %s hooks of yours in %s moved, and %s keys its trust in a hook to its position, so it skips them until you trust them again with /hooks:\n", name, path, name)
		for _, hook := range result.Shifted {
			_, _ = fmt.Fprintf(stderr, "  %s\n", hook)
		}
	}
}

// installUserHooksDuringSetup is setup's step for the user-wide hooks, run
// once per invocation. Failures are warnings: the repository's own hooks are
// what setup is for.
func installUserHooksDuringSetup(stdout, stderr io.Writer) {
	agents := detectedUserHookAgents()
	if len(agents) == 0 {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: could not install baum's user-wide hooks: %v\n", err)
		return
	}
	for _, agent := range agents {
		result, err := userhooks.Install(home, agent)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: could not install baum's user-wide %s hooks: %v\nRun `baum hooks install --user` once it is fixed.\n", agentDisplayName(agent), err)
			continue
		}
		if result.Changed {
			reportUserHooks(stdout, stderr, result, true)
		}
	}
}

// setupInstallsUserHooks reports whether setup installs the user-wide hooks:
// for a hosted install or a logged-in user, where they add something today.
// An open-source user gets them deliberately, from `baum hooks install
// --user` or the desktop app's onboarding.
func setupInstallsUserHooks() bool {
	return hostedInstall() || loggedIn()
}

// userHooksPendingForSetup reports whether setup would change any detected
// agent's user-wide settings.
func userHooksPendingForSetup() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, agent := range detectedUserHookAgents() {
		if pending, err := userhooks.Pending(home, agent); err == nil && pending {
			return true
		}
	}
	return false
}

// writeUserHooksPlan names the files outside the repositories that setup is
// about to change, before it asks to go ahead.
func writeUserHooksPlan(stdout io.Writer) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	var paths []string
	for _, agent := range detectedUserHookAgents() {
		if pending, err := userhooks.Pending(home, agent); err == nil && pending {
			settings, _ := agenthooks.LookupUserWide(agent)
			paths = append(paths, homeRelative(filepath.Join(home, filepath.FromSlash(settings.SettingsFile))))
		}
	}
	if len(paths) > 0 {
		_, _ = fmt.Fprintf(stdout, "Setup also adds baum's user-wide hooks to %s (--skip-user-hooks leaves them alone).\n", strings.Join(paths, " and "))
	}
}

func detectedUserHookAgents() []string {
	var agents []string
	for _, agent := range detectEntireAgents() {
		if slices.Contains(userhooks.Agents(), agent.Slug) {
			agents = append(agents, agent.Slug)
		}
	}
	return agents
}

func agentDisplayName(slug string) string {
	for _, agent := range entireAgents {
		if agent.Slug == slug {
			return agent.DisplayName
		}
	}
	return slug
}

// homeRelative writes a path below the home directory from "~", as the
// agents' documentation names their settings files.
func homeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}

func agentNoun(count int) string {
	if count == 1 {
		return "agent"
	}
	return "agents"
}

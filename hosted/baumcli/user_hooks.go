package baumcli

import (
	"context"
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

// The refusals of `baum hooks agent --check --user`. Each one leaves the
// user-wide hook doing nothing, which is what it does without baum.
var (
	errUserHookOutsideRepository = errors.New("not in a Git repository")
	errUserHookNoEntire          = errors.New("Entire is not enabled in this repository")
	errUserHookProjectHandles    = errors.New("the repository's own hooks handle this event")
)

// userHooksEnabled answers `baum hooks agent --check --user`, the gate of the
// hooks `baum hooks install --user` puts in the user-wide agent settings,
// which the agent runs in every directory. They run baum only where the
// committed hooks would and only where nothing else already handles the
// event:
//
//   - inside a Git repository whose Entire is enabled (.entire/settings.json
//     or settings.local.json): everywhere else the hook's only work,
//     delegating to Entire, would do nothing, and the session-start bootstrap
//     would install Git hooks in a repository that never asked for them;
//   - where no project hook file the agent reads, from the working directory
//     up to the repository root, runs Entire for the event: those are the
//     committed dispatchers of `baum setup` and the onboarding pull request,
//     the pre-rename ones, or Entire's own, and the event must reach Entire
//     once, not twice;
//   - and where the committed dispatcher's `--check` would succeed
//     (hostedHooksEnabled), so the opt-in rules are the same.
//
// The first two read a few local files and start no process, so the hook
// costs a repository that does not use Entire almost nothing.
func userHooksEnabled(ctx context.Context, dir, agent, event string) error {
	spec, _ := lookupAgentHookSpec(agent)
	settings, ok := agenthooks.LookupUserWide(agent)
	if !ok {
		return fmt.Errorf("%s has no user-wide hooks", agent)
	}
	start, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, ok := enclosingWorktree(start)
	if !ok {
		return errUserHookOutsideRepository
	}
	if !entireEnabled(root) {
		return errUserHookNoEntire
	}
	dirs := projectDirs(start, root)
	if project := os.Getenv("CLAUDE_PROJECT_DIR"); project != "" && agent == "claude-code" {
		dirs = append(dirs, project)
	}
	for _, projectDir := range dirs {
		for _, file := range settings.ProjectFiles {
			if projectFileHandles(filepath.Join(projectDir, filepath.FromSlash(file)), spec, event) {
				return errUserHookProjectHandles
			}
		}
	}
	return hostedHooksEnabled(ctx, dir)
}

// enclosingWorktree finds the nearest directory at or above dir holding a
// .git directory or file, without starting git.
func enclosingWorktree(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func entireEnabled(root string) bool {
	for _, name := range []string{"settings.json", "settings.local.json"} {
		if info, err := os.Stat(filepath.Join(root, ".entire", name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// projectDirs lists dir and its parents up to root, where agents look for
// project settings.
func projectDirs(dir, root string) []string {
	dirs := []string{dir}
	for dir != root {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		dirs = append(dirs, dir)
	}
	return dirs
}

// projectFileHandles reports whether a project hook file runs Entire for the
// event. A file baum cannot read in full counts as handling it: missing an
// event is better than reporting it twice.
func projectFileHandles(path string, spec agentHookSpec, event string) bool {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > maxSettingsFileBytes {
		return true
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return agenthooks.HandlesEvent(contents, spec, event)
}

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
other settings and hooks are kept; the previous file is saved beside it with
the suffix .baum-backup. Running it again changes nothing.

The hooks run baum only in a Git repository with Entire enabled whose own
hook settings do not already handle the event, and only where the committed
hooks would run baum: after baum login, in a clone set up with baum setup, or,
for a hosted install, in an onboarded repository. Everywhere else they do
nothing. They never contact a baum server.

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
file is saved beside it with the suffix .baum-backup. Hooks committed to
repositories are not touched. Without --agent, it removes them for every agent.`,
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
		reportUserHooks(stdout, result, install)
	}
	if failed > 0 {
		return fmt.Errorf("could not change the user-wide hooks of %d %s", failed, agentNoun(failed))
	}
	return nil
}

func reportUserHooks(stdout io.Writer, result userhooks.Result, install bool) {
	name, path := agentDisplayName(result.Agent), homeRelative(result.Path)
	switch {
	case install && result.Changed:
		_, _ = fmt.Fprintf(stdout, "Installed baum's %s hooks in %s.\n", name, path)
	case install:
		_, _ = fmt.Fprintf(stdout, "baum's %s hooks in %s are up to date.\n", name, path)
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
			reportUserHooks(stdout, result, true)
		}
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

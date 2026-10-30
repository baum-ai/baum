package baumcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/agentplugins"
)

func newInstallUserHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	return newChangeUserPluginsCommand("install", stdout, stderr)
}
func newUninstallUserHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	return newChangeUserPluginsCommand("uninstall", stdout, stderr)
}
func newUpdateUserHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	return newChangeUserPluginsCommand("update", stdout, stderr)
}

func newChangeUserPluginsCommand(action string, stdout, stderr io.Writer) *cobra.Command {
	var user bool
	var agents []string
	cmd := &cobra.Command{Use: action + " --user [--agent AGENT]...", Short: strings.ToUpper(action[:1]) + action[1:] + " baum's user-scoped agent marketplace plugins", Long: `Uses Claude Code or Codex's native plugin CLI to manage baum@baum.
Installation registers the public baum-ai/baum Git marketplace, downloads the
plugin and enables it. Update refreshes the marketplace and brings an installed
plugin to its latest version. The agent owns plugin settings; baum edits no hook
files. Review hook trust in the agent afterwards (Codex: /hooks). Installed and enabled
do not mean trusted. No baum sign-in is needed and no baum server is contacted.
Without --agent, uses supported coding-agent CLIs found on PATH.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user to manage user-scoped agent marketplace plugins")
			}
			if len(agents) == 0 && action == "update" {
				agents = installedUserHookAgents(cmd.Context())
				if len(agents) == 0 {
					return errors.New("no coding agent has baum@baum installed; run `baum hooks install --user` first")
				}
			}
			if len(agents) == 0 {
				agents = detectedUserHookAgents()
				if len(agents) == 0 {
					return errors.New("no supported coding-agent CLI on PATH; install/update Claude Code or Codex, then retry")
				}
			}
			return changeUserHooks(cmd.Context(), agents, action, stdout, stderr)
		}}
	cmd.Flags().BoolVar(&user, "user", false, "manage user-scoped agent marketplace plugins")
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "coding agent, "+strings.Join(agentplugins.Agents(), " or ")+" (default: CLIs found on PATH)")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

type userHooksStatus struct {
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	Detected  bool   `json:"detected"`
	Plugin    string `json:"plugin"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

func newUserHooksStatusCommand(stdout io.Writer) *cobra.Command {
	var user, asJSON bool
	cmd := &cobra.Command{Use: "status --user [--json]", Short: "Show native agent plugin installation and enablement", Long: `Reads Claude Code and Codex's native plugin registries. Reports baum@baum's
user installation, enablement and version separately. Trust belongs to the
agent: inspect its hooks UI. This does not edit hook settings.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user to inspect user-scoped agent plugins")
			}
			statuses := userHooksStatuses(cmd.Context())
			if asJSON {
				return json.NewEncoder(stdout).Encode(struct {
					Agents []userHooksStatus `json:"agents"`
				}{statuses})
			}
			for _, s := range statuses {
				state := "not installed"
				switch {
				case s.Error != "":
					state = "error: " + s.Error
				case !s.Detected:
					state = "CLI not found on PATH"
				case s.Installed && !s.Enabled:
					state = "installed, disabled"
				case s.Installed:
					state = "installed, enabled (review hook trust in agent)"
				}
				_, _ = fmt.Fprintf(stdout, "%s: %s %s\n", s.Name, s.Plugin, state)
			}
			return nil
		}}
	cmd.Flags().BoolVar(&user, "user", false, "inspect user-scoped agent marketplace plugins")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print status as one JSON line")
	cmd.SetOut(stdout)
	return cmd
}

func userHooksStatuses(ctx context.Context) []userHooksStatus {
	statuses := []userHooksStatus{}
	client := agentplugins.New()
	for _, agent := range agentplugins.Agents() {
		s := userHooksStatus{Agent: agent, Name: agentDisplayName(agent), Plugin: agentplugins.Plugin}
		_, err := lookPath(agentplugins.Binary(agent))
		s.Detected = err == nil
		if s.Detected {
			status, err := client.Status(ctx, agent)
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Installed = status.Installed
				s.Enabled = status.Enabled
				s.Version = status.Version
			}
		}
		statuses = append(statuses, s)
	}
	return statuses
}

func changeUserHooks(ctx context.Context, agents []string, action string, stdout, stderr io.Writer) error {
	for _, agent := range agents {
		if !slices.Contains(agentplugins.Agents(), agent) {
			return fmt.Errorf("unsupported coding agent %q; use %s", agent, strings.Join(agentplugins.Agents(), " or "))
		}
	}
	client := agentplugins.New()
	var failures []error
	for _, agent := range slices.Compact(slices.Clone(agents)) {
		name := agentDisplayName(agent)
		var status agentplugins.Status
		var before string
		var err error
		switch action {
		case "install":
			status, err = client.Install(ctx, agent)
		case "update":
			before, status, err = client.Update(ctx, agent)
		default:
			status, err = client.Uninstall(ctx, agent)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			_, _ = fmt.Fprintf(stderr, "error: %s plugin: %v\n", name, err)
			continue
		}
		switch {
		case action == "install":
			_, _ = fmt.Fprintf(stdout, "Installed and enabled %s for %s", agentplugins.Plugin, name)
			if status.Version != "" {
				_, _ = fmt.Fprintf(stdout, " (%s)", status.Version)
			}
			_, _ = fmt.Fprintln(stdout, ". Review hook trust in the agent before relying on the hooks (Codex: /hooks).")
		case action == "update" && status.Version != "" && before == status.Version:
			_, _ = fmt.Fprintf(stdout, "%s for %s is already the latest version (%s).\n", agentplugins.Plugin, name, status.Version)
		case action == "update" && before != "" && status.Version != "":
			_, _ = fmt.Fprintf(stdout, "Updated %s for %s from %s to %s. Restart running %s sessions to use the new hooks.", agentplugins.Plugin, name, before, status.Version, name)
			if agent == "codex" {
				_, _ = fmt.Fprint(stdout, " Trust new or changed hooks with /hooks in Codex.")
			}
			_, _ = fmt.Fprintln(stdout)
		case action == "update":
			_, _ = fmt.Fprintf(stdout, "Updated %s for %s. Restart running %s sessions to use the new hooks.\n", agentplugins.Plugin, name, name)
		default:
			_, _ = fmt.Fprintf(stdout, "%s is not installed for %s. The marketplace and other plugins are unchanged.\n", agentplugins.Plugin, name)
		}
	}
	return errors.Join(failures...)
}

func installUserHooksDuringSetup(ctx context.Context, stdout, stderr io.Writer) {
	agents := detectedUserHookAgents()
	if len(agents) == 0 {
		return
	}
	if err := changeUserHooks(ctx, agents, "install", stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, "warning: could not install all agent plugins; update the agent CLI or use its plugin UI, then retry `baum hooks install --user`.")
	}
}

func setupInstallsUserHooks() bool { return hostedInstall() || loggedIn() }

func userHooksPendingForSetup(ctx context.Context) bool {
	for _, s := range userHooksStatuses(ctx) {
		if s.Detected && (!s.Installed || !s.Enabled) {
			return true
		}
	}
	return false
}

func writeUserHooksPlan(ctx context.Context, stdout io.Writer) {
	var names []string
	for _, s := range userHooksStatuses(ctx) {
		if s.Detected && (!s.Installed || !s.Enabled) {
			names = append(names, s.Name)
		}
	}
	if len(names) > 0 {
		_, _ = fmt.Fprintf(stdout, "Setup also registers the public %s Git marketplace and installs/enables %s for %s (network download; --skip-user-hooks skips plugins). Review hook trust afterwards.\n", agentplugins.Marketplace, agentplugins.Plugin, strings.Join(names, " and "))
	}
}

// installedUserHookAgents are the agents with the plugin, which update
// brings to the latest version when none is named.
func installedUserHookAgents(ctx context.Context) []string {
	var agents []string
	for _, s := range userHooksStatuses(ctx) {
		if s.Installed {
			agents = append(agents, s.Agent)
		}
	}
	return agents
}

func detectedUserHookAgents() []string {
	var agents []string
	for _, agent := range agentplugins.Agents() {
		if _, err := lookPath(agentplugins.Binary(agent)); err == nil {
			agents = append(agents, agent)
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

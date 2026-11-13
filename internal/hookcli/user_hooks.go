package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentcli"
	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/agentplugins"
	"github.com/baum-ai/baum/internal/codexconfig"
	"github.com/baum-ai/baum/plugins"
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
	var claudeDir, codexHome string
	defaults := "supported coding-agent CLIs found on PATH"
	if action == "update" {
		defaults = "the coding agents with the plugin enabled"
	}
	cmd := &cobra.Command{Use: action + " --user [--agent AGENT]...", Short: strings.ToUpper(action[:1]) + action[1:] + " baum's user-scoped agent marketplace plugins", Long: `Uses Claude Code or Codex's native plugin CLI to manage baum@baum.
Installation registers the public baum-ai/baum Git marketplace, downloads the
plugin and enables it. Update refreshes the marketplace and brings an installed
plugin to its latest version. The agent owns plugin settings; baum edits no hook
files. Review hook trust in the agent afterwards (Codex: /hooks). Installed and enabled
do not mean trusted. No baum sign-in is needed and no baum server is contacted.
Without --agent, uses ` + defaults + `.

Claude Code keeps its plugins in its configuration directory: ~/.claude, or
the one CLAUDE_CONFIG_DIR names. To manage the plugin of another directory
(a second account, for example), pass --claude-config-dir or run this with
CLAUDE_CONFIG_DIR set as you run claude. Codex keeps them in its home:
~/.codex, or the one CODEX_HOME names; pass --codex-home or set CODEX_HOME
for another. baum remembers a directory it installed into and reads the
sessions kept there.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user to manage user-scoped agent marketplace plugins")
			}
			if err := useAgentDirs(claudeDir, codexHome); err != nil {
				return err
			}
			if len(agents) == 0 && action == "update" {
				agents = updatableUserHookAgents(cmd.Context())
				if len(agents) == 0 {
					return errors.New("no coding agent has baum@baum installed and enabled; install or enable it with `baum hooks install --user` first")
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
	cmd.Flags().StringSliceVar(&agents, "agent", nil, "coding agent, "+strings.Join(agentplugins.Agents(), " or ")+" (default: "+defaults+")")
	cmd.Flags().StringVar(&claudeDir, "claude-config-dir", "", claudeConfigDirUsage)
	cmd.Flags().StringVar(&codexHome, "codex-home", "", codexHomeUsage)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

type userHooksStatus struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
	// SignedIn is whether the detected CLI is signed in, as runs would find
	// it; nil when it was not asked or could not say.
	SignedIn *bool `json:"signed_in,omitempty"`
	// CLIPath and CLIVersion are the detected CLI and its version; Supported
	// is whether baum runs that version (MinimumVersion or newer), nil when
	// the version could not be read. Recorded says CLIPath is in a directory
	// `baum desktop-setup` recorded from the user's terminal.
	CLIPath        string `json:"cli_path,omitempty"`
	CLIVersion     string `json:"cli_version,omitempty"`
	MinimumVersion string `json:"minimum_version,omitempty"`
	Supported      *bool  `json:"supported,omitempty"`
	Recorded       bool   `json:"recorded,omitempty"`
	Plugin         string `json:"plugin"`
	Installed      bool   `json:"installed"`
	Enabled        bool   `json:"enabled"`
	Version        string `json:"version,omitempty"`
	// Latest is the plugin version this baum shipped with; UpdateAvailable
	// says the installed Version is older than it.
	Latest          string                  `json:"latest,omitempty"`
	UpdateAvailable bool                    `json:"update_available,omitempty"`
	Error           string                  `json:"error,omitempty"`
	Trust           *agentplugins.HookTrust `json:"trust,omitempty"`
	// ConfigDir is the agent's configuration directory the status is of
	// (Claude Code's configuration directory, Codex's home), when it is not
	// the default ~/.claude or ~/.codex.
	ConfigDir string `json:"config_dir,omitempty"`
}

const (
	claudeConfigDirUsage = "Claude Code configuration `directory` to manage the plugin of (default: $CLAUDE_CONFIG_DIR, else ~/.claude)"
	codexHomeUsage       = "Codex home `directory` to manage the plugin of (default: $CODEX_HOME, else ~/.codex)"
)

// agentDirs is the agentdirs.Agent of a coding agent whose directories baum
// knows: Claude Code's and Codex's.
func agentDirs(agent string) (agentdirs.Agent, bool) {
	switch agent {
	case "claude-code":
		return agentdirs.Claude, true
	case "codex":
		return agentdirs.Codex, true
	}
	return agentdirs.Agent{}, false
}

// useAgentDirs makes the directories given, each when given, those of this
// command: the agent CLIs it runs, and what it reads of the agents'
// settings, take them from CLAUDE_CONFIG_DIR and CODEX_HOME.
func useAgentDirs(claudeDir, codexHome string) error {
	for _, use := range []struct {
		agent     agentdirs.Agent
		flag, dir string
	}{{agentdirs.Claude, "--claude-config-dir", claudeDir}, {agentdirs.Codex, "--codex-home", codexHome}} {
		if use.dir == "" {
			continue
		}
		dir, err := agentdirs.Normalize(use.dir)
		if err != nil {
			return fmt.Errorf("%s: %w", use.flag, err)
		}
		if err := os.Setenv(use.agent.Env, dir); err != nil {
			return err
		}
	}
	return nil
}

// otherAgentDir is the directory of agent this command manages when it is
// not the default (~/.claude, ~/.codex), else "".
func otherAgentDir(agent string) string {
	a, ok := agentDirs(agent)
	if !ok {
		return ""
	}
	if dir := a.FromEnv(); dir != a.Default("") {
		return dir
	}
	return ""
}

// rememberAgentDir is a seam for tests: recording the directory installed
// into.
var rememberAgentDir = func(a agentdirs.Agent, dir string) (bool, error) { return a.Remember(dir) }

// rememberSessionDir records the directory of the agent session a hook runs
// in, when it is not the default: Claude Code passes its hooks its
// CLAUDE_CONFIG_DIR, and Codex's hooks inherit its CODEX_HOME. The daemon
// reads a session's transcript only from the directories it knows, so a
// session of a directory the plugin was installed into by hand would
// otherwise never show. It reads one small file when the directory is
// already recorded, and never fails the hook.
func rememberSessionDir(agent string) {
	a, ok := agentDirs(agent)
	if !ok {
		return
	}
	if dir := otherAgentDir(agent); dir != "" {
		_, _ = rememberAgentDir(a, dir)
	}
}

func newUserHooksStatusCommand(stdout io.Writer) *cobra.Command {
	var user, asJSON bool
	var claudeDir, codexHome string
	cmd := &cobra.Command{Use: "status --user [--json]", Short: "Show native agent plugin installation and enablement", Long: `Reads Claude Code and Codex's native plugin registries. Reports baum@baum's
user installation, enablement and version separately. Trust belongs to the
agent: inspect its hooks UI. This does not edit hook settings.`, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !user {
				return errors.New("pass --user to inspect user-scoped agent plugins")
			}
			if err := useAgentDirs(claudeDir, codexHome); err != nil {
				return err
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
					if s.Trust != nil {
						state = "installed, enabled (hooks: " + s.Trust.State + ")"
					}
				}
				name := s.Name
				if s.ConfigDir != "" {
					name += " (" + s.ConfigDir + ")"
				}
				_, _ = fmt.Fprintf(stdout, "%s: %s %s\n", name, s.Plugin, state)
			}
			return nil
		}}
	cmd.Flags().BoolVar(&user, "user", false, "inspect user-scoped agent marketplace plugins")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print status as one JSON line")
	cmd.Flags().StringVar(&claudeDir, "claude-config-dir", "", claudeConfigDirUsage)
	cmd.Flags().StringVar(&codexHome, "codex-home", "", codexHomeUsage)
	cmd.SetOut(stdout)
	return cmd
}

func userHooksStatuses(ctx context.Context) []userHooksStatus {
	statuses := []userHooksStatus{}
	client := agentplugins.New()
	var record agentcli.Record
	if file, err := agentcli.RecordFile(); err == nil {
		record, _ = agentcli.Load(file)
	}
	for _, agent := range agentplugins.Agents() {
		s := userHooksStatus{Agent: agent, Name: agentDisplayName(agent), Plugin: agentplugins.Plugin}
		s.ConfigDir = otherAgentDir(agent)
		binary, err := lookPath(agentplugins.Binary(agent))
		s.Detected = err == nil
		if cli, ok := agentcli.Lookup(agent); ok && s.Detected {
			s.CLIPath, s.MinimumVersion, s.Recorded = binary, cli.Minimum, record.Holds(binary)
			if version, err := agentVersion(ctx, binary); err == nil {
				supported := agentcli.Supported(version, cli.Minimum)
				s.CLIVersion, s.Supported = version, &supported
			}
		}
		if s.Detected {
			if signedIn, known := agentSignedIn(ctx, agent, binary); known {
				s.SignedIn = &signedIn
			}
			status, err := client.Status(ctx, agent)
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Installed = status.Installed
				s.Enabled = status.Enabled
				s.Version = status.Version
				s.Latest = plugins.Version(agent)
				s.UpdateAvailable = status.Installed && olderVersion(s.Version, s.Latest)
				if agent == "codex" && status.Installed && status.Enabled {
					trust := client.HookTrust(ctx, nil)
					s.Trust = &trust
				}
				if agent == "claude-code" && status.Installed && status.Enabled {
					trust := agentplugins.ClaudeHooks()
					s.Trust = &trust
				}
			}
		}
		statuses = append(statuses, s)
	}
	return statuses
}

// olderVersion reports whether installed is an older plugin version than
// latest. An unreadable version is never older, so it offers no update.
func olderVersion(installed, latest string) bool {
	installed, latest = "v"+strings.TrimPrefix(installed, "v"), "v"+strings.TrimPrefix(latest, "v")
	return semver.IsValid(installed) && semver.IsValid(latest) && semver.Compare(installed, latest) < 0
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
		if agent == "codex" && action != "uninstall" {
			changed, err := codexconfig.EnsureTerminalTitle()
			if err != nil {
				failures = append(failures, fmt.Errorf("Codex plugin is installed, but terminal focus setup failed: %w", err))
				_, _ = fmt.Fprintf(stderr, "error: Codex terminal focus setup: %v\n", err)
				continue
			}
			if changed {
				_, _ = fmt.Fprintln(stdout, "Configured Codex terminal focus. Restart running Codex terminal sessions, then try Focus again.")
			}
		}
		if dir := otherAgentDir(agent); dir != "" {
			name += " (" + dir + ")"
			if action == "install" {
				// baum reads sessions only from the directories it knows;
				// without the record this one's would not show.
				a, _ := agentDirs(agent)
				if _, err := rememberAgentDir(a, dir); err != nil {
					failures = append(failures, fmt.Errorf("%s plugin is installed, but baum could not remember %s to read its sessions: %w", a.Product, dir, err))
					_, _ = fmt.Fprintf(stderr, "error: remembering %s: %v\n", dir, err)
					continue
				}
			}
		}
		switch {
		case action == "install":
			_, _ = fmt.Fprintf(stdout, "Connected %s", name)
			if status.Version != "" {
				_, _ = fmt.Fprintf(stdout, " (baum plugin %s)", status.Version)
			}
			// Only Codex asks the person to approve hooks, and only until they
			// have; Claude Code runs them.
			if agent == "codex" && client.HookTrust(ctx, nil).State != "trusted" {
				_, _ = fmt.Fprintln(stdout, ". Open /hooks in Codex and trust baum's hooks so new sessions appear in baum.")
			} else {
				_, _ = fmt.Fprintf(stdout, ". New %s sessions will appear in baum.\n", name)
			}
		case action == "update" && status.Version != "" && before == status.Version:
			_, _ = fmt.Fprintf(stdout, "%s for %s is already the latest version (%s).\n", agentplugins.Plugin, name, status.Version)
		case action == "update":
			_, _ = fmt.Fprintf(stdout, "Updated %s for %s", agentplugins.Plugin, name)
			if before != "" && status.Version != "" {
				_, _ = fmt.Fprintf(stdout, " from %s to %s", before, status.Version)
			}
			_, _ = fmt.Fprintf(stdout, ". Restart running %s sessions to use the new hooks.", name)
			if agent == "codex" {
				_, _ = fmt.Fprint(stdout, " Trust new or changed hooks with /hooks in Codex.")
			}
			_, _ = fmt.Fprintln(stdout)
		default:
			_, _ = fmt.Fprintf(stdout, "Removed baum's hooks from %s. baum won't see new %s sessions until you connect it again.\n", name, name)
		}
	}
	return errors.Join(failures...)
}

// updatableUserHookAgents are the agents update brings to the latest version
// when none is named: those with the plugin enabled, and those whose status
// failed, so the update reports the failure rather than skipping them.
func updatableUserHookAgents(ctx context.Context) []string {
	var agents []string
	for _, s := range userHooksStatuses(ctx) {
		if s.Error != "" || (s.Installed && s.Enabled) {
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

// agentSignedIn asks a detected agent CLI whether it is signed in; known is
// false for an agent baum does not run jobs with or an answer it could not
// read. A variable, so tests do not run the CLIs.
var agentSignedIn = func(ctx context.Context, agent, binary string) (signedIn, known bool) {
	switch agent {
	case "claude-code":
		return claude.SignedIn(ctx, binary)
	case "codex":
		return codex.SignedIn(ctx, binary)
	}
	return false, false
}

// agentVersion reads a detected agent CLI's version. A variable, so tests do
// not run the CLIs.
var agentVersion = agentcli.Version

func agentDisplayName(slug string) string {
	if agent, ok := agentcli.Lookup(slug); ok {
		return agent.Name
	}
	return slug
}

var lookPath = exec.LookPath

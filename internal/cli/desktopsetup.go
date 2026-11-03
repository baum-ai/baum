package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/agentcli"
)

// desktopSetup is what `baum desktop-setup --json` prints.
type desktopSetup struct {
	Agents []agentcli.Found `json:"agents"`
	// Record is the file the paths were saved to.
	Record string `json:"record"`
}

func newDesktopSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "desktop-setup",
		Short: "Record the Claude Code and Codex your terminal runs, for the desktop app",
		Long: `Desktop-setup finds Claude Code (claude) and Codex (codex) on this terminal's
PATH, prints where each one is, its version and whether baum supports it, and
saves the directories they are in. From then on baum, the daemon and the
desktop app look there first, so they run the agents this terminal runs even
when your shell sets them up only for interactive use (a version manager in
~/.bashrc, for example). Run it in the terminal you use the agents from, and
again after you move or reinstall them; click Check again in the desktop app
afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			file, err := agentcli.RecordFile()
			if err != nil {
				return err
			}
			report := desktopSetup{Agents: []agentcli.Found{}, Record: file}
			var record agentcli.Record
			for _, agent := range agentcli.Agents {
				found := agentcli.Find(cmd.Context(), agent)
				report.Agents = append(report.Agents, found)
				if recorded, ok := agentcli.Recording(found); ok {
					record.Agents = append(record.Agents, recorded)
				}
			}
			if record.Agents == nil {
				record.Agents = []agentcli.Recorded{}
			}
			if err := agentcli.Save(file, record); err != nil {
				return fmt.Errorf("saving %s: %w", file, err)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			out := cmd.OutOrStdout()
			for _, found := range report.Agents {
				_, _ = fmt.Fprintln(out, describeFound(found))
			}
			_, _ = fmt.Fprintf(out, "\nSaved to %s. baum uses these from now on: click Check again in the baum app.\n", file)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "print the result as one JSON line")
	return cmd
}

// describeFound is one agent's line of desktop-setup's report.
func describeFound(found agentcli.Found) string {
	switch {
	case found.Path == "":
		return fmt.Sprintf("%s: not found on this terminal's PATH.", found.Name)
	case found.Version == "":
		return fmt.Sprintf("%s: %s, version unknown (%s). baum needs %s or newer.", found.Name, found.Path, found.Error, found.Minimum)
	case !found.Supported:
		return fmt.Sprintf("%s: %s, version %s: not supported. baum needs %s or newer; upgrade %s, then run this again.", found.Name, found.Path, found.Version, found.Minimum, found.Name)
	default:
		return fmt.Sprintf("%s: %s, version %s: supported.", found.Name, found.Path, found.Version)
	}
}

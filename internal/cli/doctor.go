package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/doctor"
)

// doctorOptions builds the report's options; tests replace it to stand in for
// the machine.
var doctorOptions = func(fetchPrices bool) doctor.Options {
	return doctor.Options{FetchPrices: fetchPrices, Daemon: daemonReport}
}

// problemsError is returned by "baum doctor" when the report has a problem, so
// the command exits with ExitDiagnostics after printing the report.
type problemsError struct{ problems int }

func (e *problemsError) Error() string {
	if e.problems == 1 {
		return "doctor found 1 problem; see its fix line"
	}
	return fmt.Sprintf("doctor found %d problems; see their fix lines", e.problems)
}

func newDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Show why a backend or GitHub token is not found, with a fix for each problem",
		Long: `Doctor reports what baum found on this machine: the path and version of git,
gh, claude, codex and cursor-agent; which backend --backend auto would pick and
why; where the GitHub token comes from (the environment, the config file or
gh), never the token itself; whether ANTHROPIC_API_KEY is set; where the price
list comes from; and where the config file, repository registry, job queue,
discovery cache and review stores live. Every problem and warning has a fix
line, for example setting backend_paths.claude in the config file.

It runs --version on git and gh, the same checks "baum generate" runs for
claude, codex (including codex's read-only sandbox check, which makes and
removes a temporary directory) and cursor-agent (its --help and --version)
and, when no token is configured, "gh auth token". It creates no files of its own and opens the registry
read-only. Its one network request is the signed price list; --offline skips
it.

Exit codes: 0 no problems (warnings allowed); 6 at least one problem.`,
		Args: cobra.NoArgs,
		RunE: runDoctor,
	}
	cmd.Flags().Bool("json", false, "print the report as JSON")
	cmd.Flags().Bool("offline", false, "do not fetch the signed price list")
	return cmd
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	offline, _ := cmd.Flags().GetBool("offline")
	ctx, stop := bundleContext(cmd.Context())
	defer stop()
	report := doctor.Run(ctx, doctorOptions(!offline))
	if ctx.Err() != nil {
		return interruptedError()
	}
	if err := writeDoctor(cmd, report, asJSON); err != nil {
		return err
	}
	if n := report.Problems(); n > 0 {
		return &problemsError{problems: n}
	}
	return nil
}

func writeDoctor(cmd *cobra.Command, report doctor.Report, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return doctor.WriteText(cmd.OutOrStdout(), report)
}

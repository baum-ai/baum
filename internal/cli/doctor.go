package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/generate"
)

// doctorOptions builds the report's options; tests replace it to stand in for
// the machine.
var doctorOptions = func(fetchPrices bool) doctor.Options {
	return doctor.Options{FetchPrices: fetchPrices, Daemon: daemonReport, Desktop: true}
}

// problemsError is returned by "baum internal doctor" when the report has a problem, so
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
		Long: `Doctor reports the desktop runtime on this machine: git, Claude Code and
Codex paths and versions; which desktop backend automatic selection would
pick; the source of the GitHub credential, never its value; the price list;
and the config, registry, job queue, discovery cache and review stores.
Every problem and warning includes a fix.

It runs bounded version and desktop engine checks, including Codex's read-only
sandbox preflight in a temporary directory. It opens the registry read-only,
queries a running daemon without starting one, and reads baum's operating
system credential store when no token is configured. Its only network request
is the signed price list; --offline skips it. Automatic selection considers
only Claude Code and Codex and never falls back to an API-key backend.

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
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := doctor.Run(ctx, doctorOptions(!offline))
	if ctx.Err() != nil {
		return &generate.Error{Category: generate.CategoryInterrupted, Err: errors.New("interrupted")}
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

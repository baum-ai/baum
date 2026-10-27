// Package cli implements the baum command-line interface.
package cli

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/repos"
)

// buildVersion is injected by the release build through -ldflags -X; local
// builds fall back to the module version recorded by the Go toolchain.
var buildVersion string

// Exit codes of the command line. Failures of "baum internal generate" carry one of the
// named categories; everything else, a bad command or a failure of another
// command, exits with ExitFailure.
const (
	ExitFailure    = 1
	ExitInput      = 2
	ExitBackend    = 3
	ExitBudget     = 4
	ExitValidation = 5
	// ExitDiagnostics is "baum internal doctor" finding at least one problem.
	ExitDiagnostics = 6
	ExitInterrupted = 130
)

// ExitCode is the process exit code for the error Run returned; nil is 0.
func ExitCode(err error) int {
	if code, ok := hookExitCode(err); ok {
		return code
	}
	var problems *problemsError
	if errors.As(err, &problems) {
		return ExitDiagnostics
	}
	switch generate.CategoryOf(err) {
	case "":
		if err == nil {
			return 0
		}
		return ExitFailure
	case generate.CategoryInput:
		return ExitInput
	case generate.CategoryBackend:
		return ExitBackend
	case generate.CategoryBudget:
		return ExitBudget
	case generate.CategoryValidation:
		return ExitValidation
	case generate.CategoryInterrupted:
		return ExitInterrupted
	case generate.CategoryInternal:
		return ExitFailure
	}
	return ExitFailure
}

// Run dispatches args to the matching subcommand.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Remotes using an earlier name the registry recorded read as the
	// repository's current one here too, as in the daemon.
	if r, err := repos.OpenReadOnly(""); err == nil {
		_ = r.Close()
	}
	cmd := newRootCommand(stdin, stdout, stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil && strings.HasPrefix(err.Error(), "unknown command ") {
		_, _ = fmt.Fprint(stderr, cmd.UsageString())
	}
	return err
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baum",
		Short: "Local-first pull request review for coding-agent work",
		Long: `baum reviews pull requests from the coding-agent sessions on your machine.
The desktop app is how you read the reviews; the agent hooks and the baum
daemon keep them current. The commands here set those up.`,
		Version:       version(),
		SilenceErrors: true,
		// Runtime failures should not dump usage; Run prints usage for unknown
		// commands explicitly.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprint(stderr, cmd.UsageString())
			return errors.New("no command given")
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	cmd.AddCommand(
		newDaemonCommand(),
		newInternalCommand(),
	)
	addHostedCommands(cmd, stdout, stderr)
	return cmd
}

// version reports the release version, the module version embedded by the Go
// toolchain, or "devel" for local builds.
func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

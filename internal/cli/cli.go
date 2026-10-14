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
)

// buildVersion is injected by the release build through -ldflags -X; local
// builds fall back to the module version recorded by the Go toolchain.
var buildVersion string

// ErrNotImplemented is returned by subcommands whose implementation lands in a
// later phase. The command still prints its help so the intended interface is
// visible.
var ErrNotImplemented = errors.New("not implemented yet")

// Exit codes of the command line. Failures of "baum generate" carry one of the
// named categories; everything else, a bad command or a failure of another
// command, exits with ExitFailure.
const (
	ExitFailure     = 1
	ExitInput       = 2
	ExitBackend     = 3
	ExitBudget      = 4
	ExitValidation  = 5
	ExitInterrupted = 130
)

// ExitCode is the process exit code for the error Run returned; nil is 0.
func ExitCode(err error) int {
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
		Short: "Local-first pull request review generator and viewer",
		Long: `baum builds a baum.review/1 document for a pull request from
coding-agent sessions in your home directory, the local git clone and GitHub,
and serves an embedded viewer for it on localhost.`,
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
		newGenerateCommand(),
		newBundleCommand(),
		newViewCommand(),
		newValidateCommand(),
		newRefreshCommand(),
		newSessionsCommand(),
	)
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

// notImplemented prints the command's help and fails, so a stub is never
// mistaken for a successful no-op in scripts.
func notImplemented(cmd *cobra.Command, _ []string) error {
	_, _ = fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
	return fmt.Errorf("%s: %w", cmd.Name(), ErrNotImplemented)
}

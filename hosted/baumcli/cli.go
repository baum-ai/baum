// Package baumcli implements the hosted service commands of the baum CLI:
// login, logout, whoami, setup, upload and the hook dispatchers agents and Git
// call. They are added to the baum root command beside the local ones, and
// nothing they do reaches a baum server until the user runs `baum login`.
package baumcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// GroupID is the help section the hosted commands are listed under.
const GroupID = "hosted"

// clientVersion is the baum release this binary was built as, set by
// AddCommands. It names the client in the upload manifest and in requests.
var clientVersion string

// ExitError carries an exit status a command must reproduce verbatim. Agent
// hooks own their exit-code semantics, so the process exits with the status the
// delegated command returned instead of the generic failure status, and prints
// nothing: anything on its output would reach the agent.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// AddCommands adds the hosted service commands to root, listed in their own
// help section. version is the release the binary was built as.
func AddCommands(root *cobra.Command, version string, stdout, stderr io.Writer) {
	clientVersion = version
	root.AddGroup(&cobra.Group{ID: GroupID, Title: "Hosted service (nothing is sent to a baum server before `baum login`):"})
	for _, cmd := range hostedCommands(stdout, stderr) {
		cmd.GroupID = GroupID
		cancelOnInterrupt(cmd)
		root.AddCommand(cmd)
	}
}

func hostedCommands(stdout, stderr io.Writer) []*cobra.Command {
	return []*cobra.Command{
		newLoginCommand(stdout, stderr),
		newLogoutCommand(stdout, stderr),
		newWhoamiCommand(stdout, stderr),
		newUploadCommand(stdout, stderr),
		newSetupCommand(stdout, stderr),
		newHooksCommand(stdout, stderr),
	}
}

// cancelOnInterrupt makes an interrupt or SIGTERM cancel the context of cmd
// and every command below it, so in-flight requests and staging unwind and
// their deferred cleanup runs, such as removing the upload staging directory.
func cancelOnInterrupt(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			cmd.SetContext(ctx)
			return run(cmd, args)
		}
	}
	for _, child := range cmd.Commands() {
		cancelOnInterrupt(child)
	}
}

// runContext runs the hosted commands on a root of their own, as tests do.
func runContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := newRootCommand(stdin, stdout, stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	if err != nil && strings.HasPrefix(err.Error(), "unknown command ") {
		_, _ = fmt.Fprint(stderr, cmd.UsageString())
	}
	return err
}

func runWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runContext(context.Background(), args, stdin, stdout, stderr)
}

func newRootCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "baum",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	AddCommands(cmd, clientVersion, stdout, stderr)
	return cmd
}

// version reports the release AddCommands was given, the module version
// embedded by the Go toolchain, or "devel" for local builds.
func version() string {
	if clientVersion != "" {
		return clientVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

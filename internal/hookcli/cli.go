// Package hookcli implements the desktop app's local coding-agent hooks.
package hookcli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var clientVersion string

// AddCommand adds the local plugin and event commands to the baum CLI.
func AddCommand(root *cobra.Command, version string, stdout, stderr io.Writer) {
	clientVersion = version
	cmd := newHooksCommand(stdout, stderr)
	cancelOnInterrupt(cmd)
	root.AddCommand(cmd)
}

func version() string {
	if clientVersion != "" {
		return clientVersion
	}
	return "devel"
}

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

func runContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := &cobra.Command{Use: "baum", SilenceErrors: true, SilenceUsage: true}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	AddCommand(cmd, clientVersion, stdout, stderr)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

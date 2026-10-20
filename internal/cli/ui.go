package cli

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/daemon"
)

// "baum ui" only makes sure the daemon runs and opens the application it
// serves; the daemon is the one server implementation.
func newUICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the authenticated local application, starting the baum daemon if needed",
		Long: `UI starts the baum daemon unless it is running (as "baum daemon ensure" does),
prints the application's private launch URL and opens it in a browser. The
daemon keeps serving after this command exits; stop it with "baum daemon stop".
--port and --concurrency apply only when this command starts the daemon.`,
		Args: cobra.NoArgs,
		RunE: runUI,
	}
	startFlags(cmd)
	cmd.Flags().Bool("no-open", false, "print the private launch URL without opening a browser")
	return cmd
}

func runUI(cmd *cobra.Command, _ []string) error {
	start, err := startOptions(cmd)
	if err != nil {
		return err
	}
	client, err := daemonClient(cmd, start)
	if err != nil {
		return err
	}
	status, err := client.Ensure(cmd.Context())
	if err != nil {
		return err
	}
	credential, err := daemon.ReadCredential(client.Paths)
	if err != nil {
		return err
	}
	if start.Port != 0 {
		if u, parseErr := url.Parse(status.App); parseErr == nil && u.Port() != strconv.Itoa(start.Port) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: the baum daemon was already running on %s; --port applies when it starts\n", status.App)
		}
	}
	launch := credential.URL
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), launch)
	if noOpen, _ := cmd.Flags().GetBool("no-open"); !noOpen {
		if err := openBrowser(launch); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not open a browser: %v\n", err)
		}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "The baum daemon (process %d) serves the app on this machine only. Keep the launch URL private. Stop it with: baum daemon stop\n", status.PID)
	return nil
}

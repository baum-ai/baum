package baumcli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/apiclient"
)

const envTokenWarning = "warning: BAUM_TOKEN is set; it was not revoked and still authenticates this shell"

func newLogoutCommand(stdout, stderr io.Writer) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out and revoke this device's token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, _, err := loadCredentials()
			if err != nil {
				return err
			}
			serverURL := resolveServerURL(server, creds)
			// Only the stored token is revoked: BAUM_TOKEN is provisioned
			// outside this machine and may be shared with other jobs.
			envSet := os.Getenv(tokenEnvVar) != ""
			if creds.Token == "" || !issuedFor(creds, serverURL) {
				_, _ = fmt.Fprintln(stdout, "Not logged in")
				if envSet {
					_, _ = fmt.Fprintln(stderr, envTokenWarning)
				}
				return nil
			}
			client := &apiclient.Client{BaseURL: serverURL, Token: creds.Token}
			// Revocation is best-effort: the local credentials are removed
			// regardless, and an unreachable server should not trap the user
			// in a logged-in state.
			if err := client.RevokeCurrentToken(cmd.Context()); err != nil {
				_, _ = fmt.Fprintf(stderr, "warning: revoking token on server failed: %v\n", err)
			}
			if err := removeCredentials(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(stdout, "Logged out")
			if envSet {
				_, _ = fmt.Fprintln(stderr, envTokenWarning)
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&server, "server", "", "server base URL")
	return cmd
}

package baumcli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/apiclient"
)

// tokenExpiryWarningWindow is how far ahead whoami warns about the stored
// token expiring, so a collector doesn't silently stop authenticating.
const tokenExpiryWarningWindow = 30 * 24 * time.Hour

func newWhoamiCommand(stdout, stderr io.Writer) *cobra.Command {
	var server string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the logged-in user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, creds, err := authenticatedClient(server)
			if err != nil {
				return err
			}

			user, err := client.Me(cmd.Context())
			if errors.Is(err, apiclient.ErrUnauthenticated) {
				return errors.New("token is expired or revoked; run `baum login`")
			}
			if err != nil {
				return err
			}

			if user.Name != nil && *user.Name != "" {
				_, _ = fmt.Fprintf(stdout, "%s (%s)\n", user.GitHubLogin, *user.Name)
			} else {
				_, _ = fmt.Fprintln(stdout, user.GitHubLogin)
			}

			if token, fromFile := resolveToken(creds, client.BaseURL); fromFile && token != "" && !creds.ExpiresAt.IsZero() {
				if until := time.Until(creds.ExpiresAt); until < tokenExpiryWarningWindow {
					_, _ = fmt.Fprintf(stderr, "warning: token expires in %d days; run `baum login` to renew\n", int(until.Hours()/24))
				}
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&server, "server", "", "server base URL")
	return cmd
}

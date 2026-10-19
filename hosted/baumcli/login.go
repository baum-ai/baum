package baumcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/apiclient"
)

var (
	hostname  = os.Hostname
	pollAfter = time.After
)

func newLoginCommand(stdout, stderr io.Writer) *cobra.Command {
	var (
		server string
		name   string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a baum server",
		Long: `Logs this device in through the server's device-authorization flow: the
command prints a one-time code and a verification URL, and keeps polling
while you approve the login in your browser. On approval the server issues
this device an API token, stored in your user config directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, hasCreds, err := loadCredentials()
			if err != nil {
				return err
			}
			client := &apiclient.Client{BaseURL: resolveServerURL(server, creds), UserAgent: userAgent()}

			start, err := client.StartDeviceAuthorization(cmd.Context(), name)
			if err != nil {
				return err
			}

			// The verification URL already carries the code, so it is shown to
			// be checked against the browser, not copied.
			_, _ = fmt.Fprintf(stdout, "Approve this device at: %s\n", start.VerificationURIComplete)
			_, _ = fmt.Fprintf(stdout, "The page should show this code: %s\n", start.UserCode)
			_, _ = fmt.Fprintln(stdout, "Waiting for approval...")

			token, err := pollForToken(cmd.Context(), client, start)
			if err != nil {
				return err
			}

			if hasCreds && creds.Token != "" {
				_, _ = fmt.Fprintln(stderr, "warning: replacing existing credentials")
			}
			if err := saveCredentials(credentials{
				ServerURL: client.BaseURL,
				Token:     token.Token,
				ExpiresAt: token.ExpiresAt,
			}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(stdout, "Logged in as %s\n", token.User.GitHubLogin)
			if os.Getenv(tokenEnvVar) != "" {
				_, _ = fmt.Fprintln(stderr, "warning: BAUM_TOKEN is set and takes precedence over these credentials")
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&server, "server", "", "server base URL (default "+defaultServerURL+", or BAUM_SERVER)")
	cmd.Flags().StringVar(&name, "name", defaultTokenName(), "name for this device's token")
	return cmd
}

// pollForToken polls the token endpoint at the server-provided interval until
// the login is approved, denied, or expired.
func pollForToken(ctx context.Context, client *apiclient.Client, start apiclient.DeviceAuthorizationStart) (apiclient.DeviceToken, error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return apiclient.DeviceToken{}, ctx.Err()
		case <-pollAfter(interval):
		}

		token, err := client.PollDeviceToken(ctx, start.DeviceCode)
		switch {
		case errors.Is(err, apiclient.ErrAuthorizationPending):
			continue
		case errors.Is(err, apiclient.ErrSlowDown):
			interval += 5 * time.Second
			continue
		case errors.Is(err, apiclient.ErrExpiredToken):
			return apiclient.DeviceToken{}, errors.New("login expired before approval; run `baum login` again")
		case errors.Is(err, apiclient.ErrAccessDenied):
			return apiclient.DeviceToken{}, errors.New("login was denied")
		case err != nil:
			return apiclient.DeviceToken{}, err
		}
		return token, nil
	}
}

func defaultTokenName() string {
	if name, err := hostname(); err == nil && name != "" {
		return "baum-cli@" + name
	}
	return "baum-cli"
}

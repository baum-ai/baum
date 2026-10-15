package cli

import (
	"fmt"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/spf13/cobra"
)

func newUICommand() *cobra.Command {
	cmd := &cobra.Command{Use: "ui", Short: "Open the authenticated local application", Args: cobra.NoArgs, RunE: runUI}
	cmd.Flags().Int("port", 0, "listen on this port instead of a free one")
	cmd.Flags().Bool("no-open", false, "print the private launch URL without opening a browser")
	return cmd
}
func runUI(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetInt("port")
	if port < 0 || port > 65535 {
		return fmt.Errorf("--port %d is out of range", port)
	}
	cfg, err := config.Load(config.Overrides{})
	if err != nil {
		return err
	}
	registry, err := repos.Open("")
	if err != nil {
		return err
	}
	defer func() { _ = registry.Close() }()
	api := uiserver.LocalAPI{Registry: registry, Config: cfg}
	ctx, stop := viewContext()
	defer stop()
	instance, err := uiserver.Start(ctx, "", port, uiserver.Options{API: api.Handler()})
	if err != nil {
		return err
	}
	defer func() { _ = instance.Close() }()
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), instance.URL)
	noOpen, _ := cmd.Flags().GetBool("no-open")
	if !noOpen {
		if err := openBrowser(instance.URL); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not open a browser: %v\n", err)
		}
	}
	if instance.Existing {
		return nil
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Serving on this machine only. Keep the launch URL private. Press Ctrl-C to stop.")
	return instance.Server.Serve(ctx)
}

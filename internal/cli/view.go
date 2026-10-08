package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/lastpersonlabs/baum/internal/openurl"
	"github.com/lastpersonlabs/baum/internal/viewserver"
	"github.com/lastpersonlabs/baum/review"
	"github.com/lastpersonlabs/baum/schema"
)

// Seams for tests: the browser is never opened and the server is stopped
// without a signal.
var (
	openBrowser = openurl.Open
	viewContext = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
)

func runView(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	sample, _ := flags.GetBool("sample")
	watch, _ := flags.GetBool("watch")
	noOpen, _ := flags.GetBool("no-open")
	port, _ := flags.GetInt("port")

	given := 0
	for _, set := range []bool{len(args) == 1, sample, flags.Changed("pr")} {
		if set {
			given++
		}
	}
	if given != 1 {
		return errors.New("give exactly one of a document file, --sample and --pr")
	}
	if flags.Changed("pr") {
		return errors.New("view --pr needs the local version store, which is not in this build; pass a document file or --sample")
	}
	if watch && sample {
		return errors.New("--watch needs a document file; --sample is fixed")
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("--port %d is out of range", port)
	}

	validator, err := review.NewDocumentValidator()
	if err != nil {
		return err
	}
	opts := viewserver.Options{
		Watch:    watch,
		Warn:     cmd.ErrOrStderr(),
		Validate: func(document []byte) []string { return validator.ValidateStored(document, nil) },
	}
	if sample {
		opts.Document = schema.Example
	} else {
		opts.Path = args[0]
	}
	server, err := viewserver.New(opts)
	if err != nil {
		return err
	}
	url, err := server.Listen(port)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %d is already in use; pick another with --port or omit it for a free one", port)
		}
		return fmt.Errorf("listen: %w", err)
	}

	_, _ = fmt.Fprintln(cmd.OutOrStdout(), url)
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Serving on this machine only. Press Ctrl-C to stop.")
	if !noOpen {
		if err := openBrowser(url); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not open a browser: %v\n", err)
		}
	}

	ctx, stop := viewContext()
	defer stop()
	return server.Serve(ctx)
}

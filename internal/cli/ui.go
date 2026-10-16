package cli

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/background"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/uiserver"
)

func newUICommand() *cobra.Command {
	cmd := &cobra.Command{Use: "ui", Short: "Open the authenticated local application", Args: cobra.NoArgs, RunE: runUI}
	cmd.Flags().Int("concurrency", 1, "maximum concurrent generation jobs (1–16)")
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
	discover := discovery.New(github.NewFromConfig(cfg, github.Options{}), "")
	index := &reviewindex.Service{Registry: registry, Discovery: discover}
	var server *uiserver.Server
	changed := func() {
		if server != nil {
			_ = server.Publish("index", struct{}{})
		}
	}
	worker := &background.Service{Registry: registry, Discovery: discover, Saved: background.Saved, Refresh: background.Local{Client: github.NewFromConfig(cfg, github.Options{}), Config: cfg}.Refresh, Changed: changed}
	var queue *jobs.Queue
	jobHandler := &deferredJobs{}
	api := uiserver.LocalAPI{Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, discover, changed), Index: index.Handler(), Jobs: jobHandler, Background: worker.Handler(), Changed: changed,
		Diagnostics: uiDiagnostics}
	ctx, stop := viewContext()
	defer stop()
	handler := api.Handler()
	defer handler.Close()
	instance, err := uiserver.Start(ctx, "", port, uiserver.Options{API: handler})
	if err != nil {
		return err
	}
	defer func() { _ = instance.Close() }()
	server = instance.Server
	if !instance.Existing {
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		queue, err = jobs.Open("", &jobs.Local{Registry: registry, Config: cfg, Client: github.NewFromConfig(cfg, github.Options{})}, concurrency, func(job jobs.Job) {
			_ = server.Publish("progress", map[string]any{"job": job})
			if job.Phase == "finished" {
				changed()
			}
		})
		if err != nil {
			return err
		}
		defer queue.Close()
		jobHandler.handler = queue.Handler()
		go func() { _ = pricing.Refresh(ctx) }()
	}
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
	watchContext, cancelWatch := context.WithCancel(ctx)
	backgroundDone := make(chan struct{})
	go func() { defer close(backgroundDone); worker.Run(watchContext) }()
	defer func() { cancelWatch(); <-backgroundDone }()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		instance.Server.WatchIndex(watchContext, 0, func(ctx context.Context) (any, error) { return index.Build(ctx) })
	}()
	defer func() { cancelWatch(); <-watchDone }()
	return instance.Server.Serve(ctx)
}

// uiDiagnostics is the report "baum ui" serves. It never fetches the price
// list: the page shows what the startup refresh produced.
func uiDiagnostics(ctx context.Context) doctor.Report {
	return doctor.Run(ctx, doctorOptions(false))
}

// Deferred until this process owns the per-user instance lock.
type deferredJobs struct{ handler http.Handler }

func (h *deferredJobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.handler == nil {
		http.Error(w, "runner unavailable", http.StatusServiceUnavailable)
		return
	}
	h.handler.ServeHTTP(w, r)
}

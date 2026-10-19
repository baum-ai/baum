package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/background"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/shellenv"
	"github.com/baum-ai/baum/internal/uiserver"
)

// Seams for tests.
var (
	daemonPaths = daemon.DefaultPaths
	// daemonSpawn starts a detached daemon; nil runs this executable.
	daemonSpawn daemon.Spawner
)

func newDaemonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run and control the local baum daemon shared by the app and agent hooks",
		Long: `The baum daemon is one long-lived local server per user. It serves the local
application on 127.0.0.1, keeps GitHub metadata of registered clones fresh, and
runs the generation jobs you confirm in the app. The desktop app and agent hooks
start it with "baum daemon ensure"; it keeps running after they exit, until
"baum daemon stop".

Its state, credential and logs are in $XDG_STATE_HOME/baum/daemon (default
~/.local/state/baum/daemon); its control socket is in $XDG_RUNTIME_DIR/baum
when that is set, else in the state directory.`,
		Args: cobra.NoArgs,
	}
	ensure := &cobra.Command{
		Use:   "ensure",
		Short: "Start the daemon unless a compatible one is running, and print its status",
		Long: `Ensure starts the daemon unless one of this version or newer is running,
waits until it serves, and prints its status. An older daemon is stopped
(running jobs are recorded as interrupted) and replaced. A daemon speaking a
newer control protocol is an error. --port and --concurrency apply only when
this command starts the daemon.`,
		Args: cobra.NoArgs,
		RunE: runDaemonEnsure,
	}
	ensure.Flags().Bool("json", false, "print the status as one JSON line")
	startFlags(ensure)
	status := &cobra.Command{Use: "status", Short: "Show whether the daemon runs, its version, process and uptime", Args: cobra.NoArgs, RunE: runDaemonStatus}
	status.Flags().Bool("json", false, "print the status as one JSON line")
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon, recording running jobs as interrupted",
		Args:  cobra.NoArgs,
		RunE:  runDaemonStop,
	}
	stop.Flags().Duration("timeout", daemon.DefaultStopTimeout, "how long to wait for the daemon to exit")
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground",
		Long: `Run is the daemon itself, in the foreground, logging to stderr and to its log
file. It resolves your login shell's environment first, so the tools and
tokens it uses do not depend on what started it. It exits when another daemon
is already running.`,
		Args: cobra.NoArgs,
		RunE: runDaemonRun,
	}
	startFlags(run)
	run.Flags().Bool("detached", false, "log only to the log file (used by ensure)")
	_ = run.Flags().MarkHidden("detached")
	printEnv := &cobra.Command{
		Use:    "print-env MARKER",
		Short:  "Print the environment between markers (used to read the login shell's)",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return shellenv.PrintEnv(cmd.OutOrStdout(), args[0])
		},
	}
	cmd.AddCommand(ensure, status, stop, run, printEnv)
	return cmd
}

func startFlags(cmd *cobra.Command) {
	cmd.Flags().Int("port", 0, "serve the app on this loopback port instead of a free one")
	cmd.Flags().Int("concurrency", 1, "maximum concurrent generation jobs (1–16)")
}

func startOptions(cmd *cobra.Command) (daemon.StartOptions, error) {
	port, _ := cmd.Flags().GetInt("port")
	concurrency, _ := cmd.Flags().GetInt("concurrency")
	if port < 0 || port > 65535 {
		return daemon.StartOptions{}, fmt.Errorf("--port %d is out of range", port)
	}
	if concurrency < 1 || concurrency > 16 {
		return daemon.StartOptions{}, fmt.Errorf("--concurrency %d is out of range (1–16)", concurrency)
	}
	start := daemon.StartOptions{Port: port}
	if concurrency != 1 {
		start.Concurrency = concurrency
	}
	return start, nil
}

func daemonClient(cmd *cobra.Command, start daemon.StartOptions) (*daemon.Client, error) {
	paths, err := daemonPaths()
	if err != nil {
		return nil, err
	}
	return &daemon.Client{Paths: paths, Version: version(), Start: start, Spawn: daemonSpawn, Restarted: func(old daemon.Status) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Replacing the older baum daemon (version %s, process %d).\n", old.Version, old.PID)
	}}, nil
}

func runDaemonEnsure(cmd *cobra.Command, _ []string) error {
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
	return printDaemonStatus(cmd, status)
}

func runDaemonStatus(cmd *cobra.Command, _ []string) error {
	client, err := daemonClient(cmd, daemon.StartOptions{})
	if err != nil {
		return err
	}
	status, err := client.Status(cmd.Context())
	if err != nil {
		return err
	}
	return printDaemonStatus(cmd, status)
}

func printDaemonStatus(cmd *cobra.Command, status daemon.Status) error {
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		data, err := json.Marshal(status)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "baum daemon %s is running as process %d (up %s)\n", status.Version, status.PID, status.Uptime(time.Now()))
	_, _ = fmt.Fprintf(w, "app:    %s\n", status.App)
	_, _ = fmt.Fprintf(w, "socket: %s\n", status.Socket)
	_, err := fmt.Fprintf(w, "state:  %s\n", status.State)
	return err
}

func runDaemonStop(cmd *cobra.Command, _ []string) error {
	client, err := daemonClient(cmd, daemon.StartOptions{})
	if err != nil {
		return err
	}
	client.StopTimeout, _ = cmd.Flags().GetDuration("timeout")
	stopped, err := client.Stop(cmd.Context())
	if err != nil {
		return err
	}
	if !stopped {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "The baum daemon is not running.")
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Stopped the baum daemon.")
	return err
}

func runDaemonRun(cmd *cobra.Command, _ []string) error {
	start, err := startOptions(cmd)
	if err != nil {
		return err
	}
	detached, _ := cmd.Flags().GetBool("detached")
	paths, err := daemonPaths()
	if err != nil {
		return err
	}
	if err = privatefs.MkdirAll(paths.State); err != nil {
		return err
	}
	logFile, err := daemon.OpenLog(paths.LogFile(), daemon.MaxLogBytes, daemon.LogBackups)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	var w io.Writer = logFile
	if !detached {
		w = io.MultiWriter(logFile, cmd.ErrOrStderr())
	}
	logger := slog.New(slog.NewTextHandler(w, nil))
	ctx, stop := viewContext()
	defer stop()
	concurrency := start.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	err = daemon.Run(ctx, daemon.Options{
		Paths:   paths,
		Version: version(),
		Port:    start.Port,
		Logger:  logger,
		Prepare: func(ctx context.Context) error {
			applyLoginEnvironment(ctx, logger)
			return nil
		},
		Start: localApplication(concurrency, logger),
	})
	if errors.Is(err, daemon.ErrRunning) {
		logger.Info("not starting: another daemon is running")
		if detached {
			return nil
		}
	}
	if err != nil && !errors.Is(err, daemon.ErrRunning) {
		logger.Error("daemon failed", "error", err)
	}
	return err
}

// resolveShell is a seam for tests.
var resolveShell = shellenv.Resolve

// applyLoginEnvironment replaces this process's environment with the
// variables the daemon keeps, taken from the login shell when it answers (see
// shellenv.Merge). Only variable names are logged.
func applyLoginEnvironment(ctx context.Context, log *slog.Logger) {
	inherited := os.Environ()
	result, err := resolveShell(ctx, shellenv.Options{Environ: inherited})
	var resolved map[string]string
	switch {
	case errors.Is(err, shellenv.ErrUnsupported):
		log.Info("using the inherited environment", "reason", err.Error())
	case err != nil:
		log.Warn("login shell environment not read; using the inherited environment", "shell", result.Shell, "reason", err.Error())
	default:
		resolved = result.Env
		log.Info("read the login shell environment", "shell", result.Shell)
	}
	env := shellenv.Merge(inherited, resolved)
	os.Clearenv()
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		_ = os.Setenv(name, value)
	}
	log.Info("environment", "variables", strings.Join(shellenv.Names(env), " "))
}

// localApplication builds what the daemon serves: the registry, discovery,
// review index, background refresh, diagnostics and the generation queue.
// It is where automatic generation belongs.
func localApplication(concurrency int, log *slog.Logger) func(context.Context, *uiserver.Server) (daemon.App, error) {
	return func(ctx context.Context, server *uiserver.Server) (daemon.App, error) {
		cfg, err := config.Load(config.Overrides{})
		if err != nil {
			return daemon.App{}, err
		}
		registry, err := repos.Open("")
		if err != nil {
			return daemon.App{}, err
		}
		changed := func() { _ = server.Publish("index", struct{}{}) }
		discover := discovery.New(github.NewFromConfig(cfg, github.Options{}), "")
		index := &reviewindex.Service{Registry: registry, Discovery: discover}
		worker := &background.Service{Registry: registry, Discovery: discover, Saved: background.Saved, Refresh: background.Local{Client: github.NewFromConfig(cfg, github.Options{}), Config: cfg}.Refresh, Changed: changed}
		state, err := jobs.Directory()
		if err != nil {
			_ = registry.Close()
			return daemon.App{}, err
		}
		// Queued jobs resume with the price list they were confirmed against,
		// so reinstall the verified downloaded list before workers start.
		if err := pricing.LoadPersisted(state); err != nil {
			log.Warn("stored price list not loaded", "error", err)
		}
		refresh := func(ctx context.Context) error { return pricing.Refresh(ctx, state) }
		queue, err := jobs.Open(filepath.Join(state, "jobs.json"), &jobs.Local{Registry: registry, Config: cfg, Client: github.NewFromConfig(cfg, github.Options{}), RefreshPrices: refresh}, concurrency, func(job jobs.Job) {
			_ = server.Publish("progress", map[string]any{"job": job})
			if job.Phase == "finished" {
				changed()
			}
		})
		if err != nil {
			_ = registry.Close()
			return daemon.App{}, err
		}
		api := uiserver.LocalAPI{Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, discover, changed), Index: index.Handler(), Jobs: queue.Handler(), Background: worker.Handler(), Changed: changed,
			Diagnostics: uiDiagnostics}.Handler()
		return daemon.App{
			API:    api,
			Assets: localapp.Handler(),
			Run: func(ctx context.Context) {
				var wg sync.WaitGroup
				wg.Go(func() {
					if err := refresh(ctx); err != nil && ctx.Err() == nil {
						log.Warn("price list not refreshed", "error", err)
					}
				})
				wg.Go(func() { worker.Run(ctx) })
				wg.Go(func() {
					server.WatchIndex(ctx, 0, func(ctx context.Context) (any, error) { return index.Build(ctx) })
				})
				wg.Wait()
			},
			Shutdown: queue.Shutdown,
			Close: func() {
				api.Close()
				_ = registry.Close()
			},
		}, nil
	}
}

// uiDiagnostics is the report the daemon serves. It never fetches the price
// list: the page shows what the startup refresh produced.
func uiDiagnostics(ctx context.Context) doctor.Report {
	return doctor.Run(ctx, doctorOptions(false))
}

// daemonReport describes the daemon for "baum doctor" without starting it.
func daemonReport(ctx context.Context) doctor.Daemon {
	paths, err := daemonPaths()
	if err != nil {
		return doctor.Daemon{Err: err}
	}
	report := doctor.Daemon{State: paths.State, Socket: paths.Socket}
	client := &daemon.Client{Paths: paths, Version: version()}
	if _, statErr := os.Stat(paths.State); errors.Is(statErr, os.ErrNotExist) {
		// Never create the state directory just to look.
		return report
	}
	status, err := client.Status(ctx)
	switch {
	case errors.Is(err, daemon.ErrNotRunning):
		return report
	case err != nil:
		report.Err = err
		return report
	}
	report.Running = true
	report.Version, report.Protocol, report.PID = status.Version, status.Protocol, status.PID
	report.Uptime = status.Uptime(time.Now())
	report.Incompatible = status.Protocol > daemon.Protocol
	report.Older = status.Protocol < daemon.Protocol || daemon.Older(status.Version, version())
	return report
}

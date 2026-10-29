package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/analytics"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/background"
	"github.com/baum-ai/baum/internal/clonefind"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubrepos"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/hookrefresh"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/notices"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/shellenv"
	"github.com/baum-ai/baum/internal/transcript"
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
runs the generation jobs you confirm in the app. Unless you turn it off in the
app, it also generates reviews automatically when a pull request you authored
or were asked to review gets a new head or review request, and it records the
events coding-agent and Git hooks send it. The desktop app starts it with
"baum daemon ensure"; a hook starts it in the background when none is running,
unless you stopped it with "baum daemon stop", BAUM_NO_DAEMON=1 is set, or CI
is set. It keeps running after they exit, until "baum daemon stop".

Its state, credential and logs are in $XDG_STATE_HOME/baum/daemon (default
~/.local/state/baum/daemon); its control socket is in $XDG_RUNTIME_DIR/baum
when that is set, else in the state directory, or in /tmp/baum-<uid> when
that path is too long for a socket. TMPDIR never changes where it is.`,
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
	cmd.AddCommand(ensure, status, stop, run, printEnv, newLoginPathCommand())
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
	}, Deferred: func(old daemon.Status) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "The baum daemon (version %s, process %d) is running jobs; it restarts as version %s once they finish.\n", old.Version, old.PID, version())
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
	// Ensure clears a "stopped by the user" mark, so hooks start the daemon
	// again.
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
	// Hooks leave a daemon the user stopped alone until something starts
	// it explicitly (ensure, or run in the foreground).
	if err = daemon.MarkStopped(client.Paths); err != nil {
		return err
	}
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
	if !detached {
		if err = daemon.ClearStopped(paths); err != nil {
			return err
		}
	}
	// Until it owns the lock the daemon logs to stderr (daemon.out when
	// detached); only the owner opens and rotates daemon.log.
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	openLogger := func() (*slog.Logger, func(), error) {
		logFile, err := daemon.OpenLog(paths.LogFile(), daemon.MaxLogBytes, daemon.LogBackups)
		if err != nil {
			return nil, nil, err
		}
		var w io.Writer = logFile
		if !detached {
			w = io.MultiWriter(logFile, cmd.ErrOrStderr())
		}
		logger = slog.New(slog.NewTextHandler(w, nil))
		return logger, func() { _ = logFile.Close() }, nil
	}
	ctx, stop := viewContext()
	defer stop()
	concurrency := start.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	err = daemon.Run(ctx, daemon.Options{
		Paths:      paths,
		Version:    version(),
		Port:       start.Port,
		Logger:     logger,
		OpenLogger: openLogger,
		Prepare: func(ctx context.Context) error {
			applyLoginEnvironment(ctx, logger)
			// Nobody answers prompts in the daemon: fetches over SSH run in
			// batch mode with an agent socket that still exists.
			gitlocal.SetBackground(&gitlocal.Background{SSHAgent: sshAgent().Socket})
			return nil
		},
		Start: func(ctx context.Context, server *uiserver.Server) (daemon.App, error) {
			return localApplication(paths, concurrency, logger)(ctx, server)
		},
		Respawn: func(executable string, status daemon.Status) error {
			return respawnDaemon(paths, executable, status, start)
		},
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

// respawnDaemon starts executable (the newer baum a client named) as a
// detached daemon on the same port, to take over once this one exits. It runs
// with the daemon's environment, unsandboxed, while the client that named it
// may be a sandboxed agent's hook, so the daemon passes only an executable it
// trusts, checked just before: this daemon's own, the baum on its PATH or one
// of daemon.TrustedExecutables, which no other user can replace.
func respawnDaemon(paths daemon.Paths, executable string, status daemon.Status, start daemon.StartOptions) error {
	args := []string{"daemon", "run", "--detached"}
	if u, err := url.Parse(status.App); err == nil && u.Port() != "" {
		args = append(args, "--port", u.Port())
	}
	if start.Concurrency != 0 {
		args = append(args, "--concurrency", strconv.Itoa(start.Concurrency))
	}
	_, err := daemon.Spawn(paths, exec.Command(executable, args...))
	return err
}

// resolveShell is a seam for tests.
var resolveShell = shellenv.Resolve

// sshAgent replaces a stale SSH agent socket with the login shell's. The
// shell is not given the stale socket, so it reports the one its own startup
// files or session provide.
func sshAgent() *shellenv.Agent {
	return &shellenv.Agent{Resolve: func(ctx context.Context) (string, error) {
		var environ []string
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "SSH_AUTH_SOCK=") {
				environ = append(environ, entry)
			}
		}
		result, err := resolveShell(ctx, shellenv.Options{Environ: environ})
		return result.Env["SSH_AUTH_SOCK"], err
	}}
}

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
	case result.Shell == "":
		// Windows: the inherited environment with the registry's PATH.
		resolved = result.Env
		log.Info("read PATH from the registry")
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
// review index, background refresh, diagnostics, the generation queue, the
// automatic generation scheduler, and the record of agent and Git hook events.
// A session ending or a push in a registered clone refreshes its pull requests
// at once (hookrefresh), and reaches the scheduler only through
// Scheduler.Trigger.
func localApplication(paths daemon.Paths, concurrency int, log *slog.Logger) func(context.Context, *uiserver.Server) (daemon.App, error) {
	return func(ctx context.Context, server *uiserver.Server) (daemon.App, error) {
		cfg, err := config.Load(config.Overrides{})
		if err != nil {
			return daemon.App{}, err
		}
		events := openHookEvents(paths, log)
		registry, err := repos.Open("")
		if err != nil {
			events.Close()
			return daemon.App{}, err
		}
		changed := func() { _ = server.Publish("index", struct{}{}) }
		discover := discovery.New(github.NewFromConfig(cfg, github.Options{}), "")
		index := &reviewindex.Service{Registry: registry, Discovery: discover}
		worker := &background.Service{Registry: registry, Discovery: discover, Saved: background.Saved, Refresh: background.Local{Client: github.NewFromConfig(cfg, github.Options{}), Config: cfg}.Refresh, Changed: changed}
		state, err := jobs.Directory()
		if err != nil {
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		// Jobs are priced with the newest verified list, so reinstall the
		// downloaded one before workers start.
		if err := pricing.LoadPersisted(state); err != nil {
			log.Warn("stored price list not loaded", "error", err)
		}
		refresh := func(ctx context.Context) error { return pricing.Refresh(ctx, state) }
		// Automatic generation is separate from the model-free background
		// refresh: discovery hands it snapshots, and it queues through the
		// same runner as confirmed jobs.
		scheduler := &automatic.Scheduler{Registry: registry, Path: filepath.Join(state, "automatic.json"), Discover: discover.PollNow, Log: log,
			Hold: func() bool {
				status, ok := daemon.Self()
				return ok && (status.Stopping || status.RestartPending)
			},
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) },
		}
		// Notices are what the desktop app turns into notifications; they
		// carry the pull request's title and risk score, read when they happen.
		notify := &notices.Service{Registry: registry, Index: index.Build, Publish: func(n notices.Notice) { _ = server.Publish("notice", n) }}
		var identityMu sync.RWMutex
		worker.Discovered = func(ctx context.Context, snapshot discovery.Snapshot) {
			identityMu.RLock()
			defer identityMu.RUnlock()
			if snapshot.Epoch != discover.Epoch() {
				return
			}
			scheduler.Observe(ctx, snapshot)
			notify.Observe(ctx, snapshot)
		}
		// Review groups live in the daemon's state, not in a clone.
		groups, err := reviewgroups.Open(filepath.Join(state, "groups.sqlite"))
		if err != nil {
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		queue, err := jobs.Open(filepath.Join(state, "jobs.json"), &jobs.Local{Registry: registry, Groups: groups, Config: cfg, Client: github.NewFromConfig(cfg, github.Options{})}, concurrency, func(job jobs.Job) {
			_ = server.Publish("progress", map[string]any{"job": job})
			if job.Phase == "finished" {
				changed()
				// Reading the index takes a moment; the queue must not wait.
				go notify.JobDone(ctx, job)
				// A PR whose job finished may have a pending head.
				scheduler.Wake()
				// A fix pushed a new head: show it, and its checks, soon.
				if repo, err := github.ParseRepository(job.Repository); err == nil && job.Kind == jobs.KindFix && job.Commit != "" {
					go func() {
						if _, err := discover.PollNow(ctx, repo); err == nil {
							changed()
						}
					}()
				}
			}
		})
		if err != nil {
			_ = groups.Close()
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		scheduler.Queue = queue
		queue.SetAutomaticGate(scheduler.Gate)
		hookRefresh := &hookrefresh.Service{Registry: registry, Cached: discover.Cached, Refresh: worker.RefreshNow, Trigger: scheduler.Trigger, Changed: changed, Log: log}
		// Subscribed before the spool drains or the socket serves, so no
		// event arrives unseen.
		refreshEvents, unsubscribe := events.Subscribe(hookrefresh.Buffer, hookrefresh.Match)
		// A clone found for a picked repository may let the scheduler start work.
		clones := newCloneFinder(registry, events, func() {
			changed()
			scheduler.Wake()
		}, log)
		settingsChanged := func() {
			changed()
			scheduler.Wake()
			// A repository may just have been picked.
			clones.Trigger()
		}
		cloneEvents, cloneDropped, unsubscribeClones := events.SubscribeCounted(clonefind.Buffer, clonefind.MatchEvent)
		// The live transcripts are written as the review page's are, and a
		// waiting session's message is masked the same way.
		renderer := transcript.NewRenderer("")
		live := events.liveSessions(paths, registry, discover, server, log)
		var liveAPI, requestsAPI http.Handler
		var requests *agentrequests.Store
		var requestEvents <-chan hookevents.Record
		unsubscribeRequests := func() {}
		if live != nil {
			live.Mask = func(text string) string { return renderer.Mask(ctx, text) }
			// A session that starts waiting for a permission or an answer is
			// a notice; reading the registry for it must not hold the tracker.
			live.Waiting = func(session livesessions.Session) { go notify.SessionWaiting(ctx, session) }
			liveAPI = (&livesessions.Transcripts{Tracker: live, Render: renderer.Render, Log: log}).Handler()
			// What agents wait on the app to answer, and the messages queued
			// for running sessions, stay in memory, masked as the sessions
			// are; only the Codex setting is kept, in the state directory.
			// The app reads them with the live sessions.
			requests = &agentrequests.Store{Sessions: live, Mask: live.Mask, StateDir: paths.State, Log: log,
				Notify: func() { _ = server.Publish("live", struct{}{}) },
				// The store's own Notify, as the request or message goes, tells
				// the app.
				Interrupted: func(agent, session string, at time.Time) { live.Interrupted(agent, session, at) },
				Answered:    func(agent, session string, at time.Time) { live.Answered(agent, session, at) },
				Resumed:     func(agent, session string, at time.Time) { live.Resumed(agent, session, at) }}
			requestEvents, unsubscribeRequests = events.Subscribe(256, agentrequests.Match)
			requestsAPI = requests.Handler()
		}
		methods, waits := events.Methods(), map[string]daemon.Method(nil)
		if requests != nil {
			methods = maps.Clone(methods)
			if methods == nil {
				methods = map[string]daemon.Method{}
			}
			maps.Copy(methods, requests.Methods())
			waits = requests.Waits()
		}
		home, _ := os.UserHomeDir()
		pickList := &githubrepos.Service{
			Lister: github.NewFromConfig(cfg, github.Options{}), Registry: registry,
			Token:  func(ctx context.Context) (string, error) { return cfg.ResolveGitHubToken(ctx, config.GitHubHost) },
			Health: config.GitHubAuth.Health, Home: home, Context: ctx, Changed: settingsChanged,
		}
		// The delivery figures' searches, cached per repository, and the
		// review threads and failing checks the Inbox lists; an account
		// change forgets both.
		delivery := &analytics.Delivery{Search: github.NewFromConfig(cfg, github.Options{})}
		panelCache := &prupdate.Cache{}
		beginIdentityChange := func() func(bool) {
			if token, _ := cfg.ExplicitGitHubTokenSource("github.com"); token != "" {
				return func(bool) {}
			}
			finish := scheduler.BeginIdentityChange()
			return func(changedAccount bool) {
				identityMu.Lock()
				defer identityMu.Unlock()
				if changedAccount {
					discover.ResetIdentity()
					worker.ResetIdentity()
					notify.ResetIdentity()
					pickList.ResetIdentity()
					delivery.Reset()
					panelCache.ResetIdentity()
				}
				finish(changedAccount)
			}
		}
		githubConnection := uiserver.NewGitHubAuthentication(ctx, config.GitHubAuth, cfg, changed, beginIdentityChange)
		// A write to a pull request re-reads the repository's pull requests so
		// the inbox shows its new state.
		githubClient := github.NewFromConfig(cfg, github.Options{})
		refreshRepository := func(ctx context.Context, repo github.Repository) { _, _ = discover.PollNow(ctx, repo) }
		// GitHub accepted the merge: the inbox shows it merged without
		// waiting for the background refresh to read it.
		mergedPullRequest := func(ctx context.Context, repo github.Repository, number int, head string) {
			_ = discover.Merged(ctx, repo, number, head)
			refreshRepository(ctx, repo)
		}
		// The Inbox panel read a pull request's review threads: its count
		// shows before the next poll, which a resolution does not hasten.
		countedThreads := func(ctx context.Context, repo github.Repository, number, unresolved int) {
			if moved, err := discover.Unresolved(ctx, repo, number, unresolved); err == nil && moved {
				changed()
			}
		}
		// Analytics keeps aggregates only, in the state directory: an index
		// of the sessions in registered clones, the merges, branch updates
		// and re-runs made from the app, and plan usage samples.
		plans := planusage.NewService()
		planHistory := &planusage.History{Path: filepath.Join(state, "plan-usage-history.json"), Service: plans}
		actions := &analytics.Actions{Path: filepath.Join(state, "analytics-actions.json")}
		sessionIndex := &analytics.Index{Path: filepath.Join(state, "analytics-index.json"), Clones: registry, Log: log}
		insights := &analytics.Service{Registry: registry, Index: sessionIndex, Jobs: queue.List, Actions: actions, Plans: planHistory, Delivery: delivery}
		// A session that ended has its last lines written: read it soon.
		endedSessions, unsubscribeEnded := events.Subscribe(4, func(e hookevents.Event) bool {
			return e.Kind == hookevents.KindAgent && e.Name == "session-end"
		})
		api := uiserver.LocalAPI{GitHubAuth: githubConnection.Handler(), Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, discover, changed), Index: index.Handler(), Jobs: queue.Handler(), Background: worker.Handler(), Automatic: scheduler.Handler(), Live: liveAPI, AgentRequests: requestsAPI, Clones: clones.Handler(), GitHubRepositories: pickList.Handler(), PlanUsage: plans.Handler(), Analytics: insights.Handler(), Merge: uiserver.MergeHandler(registry, actions.RecordMerges(githubClient), mergedPullRequest, changed), Review: uiserver.ReviewHandler(registry, githubClient, changed), Update: uiserver.UpdateHandler(registry, actions.RecordUpdates(prupdate.Service{Client: githubClient, Cache: panelCache, Counted: countedThreads}), refreshRepository, changed), Changed: settingsChanged,
			Groups:      (&reviewgroups.API{Store: groups, Registry: registry, Index: index.Build, Changed: changed}).Handler(),
			Diagnostics: uiDiagnostics}.Handler()
		return daemon.App{
			API:     api,
			Assets:  localapp.Handler(),
			Methods: methods,
			Waits:   waits,
			Run: func(ctx context.Context) {
				var wg sync.WaitGroup
				wg.Go(func() { events.Run(ctx) })
				if refreshEvents != nil {
					wg.Go(func() { hookRefresh.Run(ctx, refreshEvents) })
				}
				wg.Go(func() {
					if err := refresh(ctx); err != nil && ctx.Err() == nil {
						log.Warn("price list not refreshed", "error", err)
					}
				})
				wg.Go(func() { clones.Run(ctx, cloneEvents, cloneDropped) })
				wg.Go(func() { pickList.Run(ctx) })
				wg.Go(func() { worker.Run(ctx) })
				wg.Go(func() { scheduler.Run(ctx) })
				if live != nil {
					wg.Go(func() { live.Run(ctx) })
				}
				if requestEvents != nil {
					wg.Go(func() { requests.Run(ctx, requestEvents) })
				}
				wg.Go(func() { sessionIndex.Run(ctx) })
				wg.Go(func() { planHistory.Run(ctx) })
				wg.Go(func() { delivery.Run(ctx) })
				if endedSessions != nil {
					wg.Go(func() {
						for {
							select {
							case <-ctx.Done():
								return
							case _, ok := <-endedSessions:
								if !ok {
									return
								}
								sessionIndex.Trigger()
							}
						}
					})
				}
				wg.Go(func() {
					server.WatchIndex(ctx, 0, func(ctx context.Context) (any, error) { return index.Build(ctx) })
				})
				wg.Wait()
			},
			Shutdown: queue.Shutdown,
			Drain:    queue.Drain,
			Resume:   queue.Resume,
			Close: func() {
				api.Close()
				unsubscribe()
				unsubscribeClones()
				unsubscribeEnded()
				unsubscribeRequests()
				events.Close()
				_ = groups.Close()
				_ = registry.Close()
			},
		}, nil
	}
}

// hookEvents is the daemon's record of hook events, or nothing when it cannot
// be opened: a broken record must not keep the daemon from starting, or every
// hook would start another. Hooks then get unknown_method and spool.
type hookEvents struct{ service *hookevents.Service }

func openHookEvents(paths daemon.Paths, log *slog.Logger) hookEvents {
	service, err := hookevents.Open(paths, log)
	if err != nil {
		log.Error("hook events are not recorded; hooks spool them until the record opens", "error", err)
		return hookEvents{}
	}
	return hookEvents{service: service}
}

// Methods are the control requests the record serves, if any.
func (h hookEvents) Methods() map[string]daemon.Method {
	if h.service == nil {
		return nil
	}
	return h.service.Methods()
}

// liveSessions tracks the agent sessions the record's events show, or nothing
// when the record is not open. It tells the app over the events stream when
// they change.
func (h hookEvents) liveSessions(paths daemon.Paths, registry *repos.Registry, discover *discovery.Service, server *uiserver.Server, log *slog.Logger) *livesessions.Tracker {
	if h.service == nil {
		return nil
	}
	return &livesessions.Tracker{
		Source: h.service.Store,
		Linker: &livesessions.Resolver{Registry: registry, Discovery: discover},
		// Ended sessions are kept for a day, longer than the record may reach.
		StateDir: hookevents.RecordDir(paths),
		Notify:   func() { _ = server.Publish("live", struct{}{}) },
		Log:      log,
	}
}

// Run drains the spool until ctx ends.
func (h hookEvents) Run(ctx context.Context) {
	if h.service != nil {
		h.service.Run(ctx)
	}
}

// Subscribe delivers the events match accepts from now on, until the
// returned function ends it; without a record the channel is nil.
func (h hookEvents) Subscribe(buffer int, match func(hookevents.Event) bool) (<-chan hookevents.Record, func()) {
	if h.service == nil {
		return nil, func() {}
	}
	return h.service.Store.Subscribe(buffer, match)
}

// SubscribeCounted is Subscribe that also reports how many events the
// subscription missed.
func (h hookEvents) SubscribeCounted(buffer int, match func(hookevents.Event) bool) (<-chan hookevents.Record, func() uint64, func()) {
	if h.service == nil {
		return nil, func() uint64 { return 0 }, func() {}
	}
	ch, cancel, dropped := h.service.Store.SubscribeCounted(buffer, match)
	return ch, dropped, cancel
}

// newCloneFinder builds the service that finds local clones of picked
// repositories from the folders agents worked in and the usual code folders.
func newCloneFinder(registry *repos.Registry, events hookEvents, changed func(), log *slog.Logger) *clonefind.Service {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Warn("clones are not searched for: no home directory", "error", err)
	}
	options := clonefind.Options{Home: home}
	if events.service != nil {
		options.Events = events.service.Store
	}
	return &clonefind.Service{Registry: registry, Options: options, Changed: changed, Log: log}
}

// Close closes the record.
func (h hookEvents) Close() {
	if h.service != nil {
		_ = h.service.Close()
	}
}

// uiDiagnostics is the report the daemon serves. It never fetches the price
// list: the page shows what the startup refresh produced.
func uiDiagnostics(ctx context.Context) doctor.Report {
	return doctor.Run(ctx, doctorOptions(false))
}

// daemonReport describes the daemon for "baum internal doctor" without starting it.
func daemonReport(ctx context.Context) doctor.Daemon {
	paths, err := daemonPaths()
	if err != nil {
		return doctor.Daemon{Err: err}
	}
	report := doctor.Daemon{State: paths.State, Socket: paths.Socket}
	// Inside the daemon, report on itself: probing its own lock or socket
	// from the process that holds them proves nothing.
	if status, ok := daemon.Self(); ok {
		return daemonReportOf(report, status)
	}
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
	return daemonReportOf(report, status)
}

func daemonReportOf(report doctor.Daemon, status daemon.Status) doctor.Daemon {
	report.Running = true
	report.Socket = status.Socket
	report.Version, report.Protocol, report.PID = status.Version, status.Protocol, status.PID
	report.Uptime = status.Uptime(time.Now())
	report.Incompatible = status.Protocol > daemon.Protocol
	report.Older = status.Protocol < daemon.Protocol || daemon.Older(status.Version, version())
	return report
}

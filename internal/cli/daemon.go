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
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/agentcli"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/internal/agentplugins"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/analytics"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/background"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/clonefind"
	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/facts"
	"github.com/baum-ai/baum/internal/focus"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/githooks"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/githubrepos"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/hookrefresh"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/inboxqueue"
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
	"github.com/baum-ai/baum/internal/reviewruns"
	"github.com/baum-ai/baum/internal/sampleapp"
	"github.com/baum-ai/baum/internal/shellenv"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
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
newer control protocol is an error. --port applies only when this command
starts the daemon.`,
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
	cmd.AddCommand(ensure, status, stop, run, printEnv, newLoginPathCommand(), newDaemonProfileCommand())
	return cmd
}

func startFlags(cmd *cobra.Command) {
	cmd.Flags().Int("port", 0, "serve the app on this loopback port instead of a free one")
	// The app's settings choose how many jobs run at once; an older daemon
	// may still pass the flag when it respawns a newer one.
	cmd.Flags().Int("concurrency", 0, "ignored")
	_ = cmd.Flags().MarkDeprecated("concurrency", "set how many jobs run at once in the app's settings, under This computer")
}

func startOptions(cmd *cobra.Command) (daemon.StartOptions, error) {
	port, _ := cmd.Flags().GetInt("port")
	if port < 0 || port > 65535 {
		return daemon.StartOptions{}, fmt.Errorf("--port %d is out of range", port)
	}
	return daemon.StartOptions{Port: port}, nil
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
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = daemon.Run(ctx, daemon.Options{
		Paths:      paths,
		Version:    version(),
		Port:       start.Port,
		Logger:     logger,
		OpenLogger: openLogger,
		Prepare: func(ctx context.Context) error {
			applyLoginEnvironment(ctx, logger)
			// The agents `baum desktop-setup` recorded come first on the
			// PATH, including those recorded while the daemon runs.
			agentcli.Keep(ctx, os.Getenv("PATH"), recordedAgentsInterval, func(string) {
				logger.Info("PATH changed: baum desktop-setup recorded the coding agents again")
			})
			// Nobody answers prompts in the daemon: fetches over SSH run in
			// batch mode with an agent socket that still exists.
			gitlocal.SetBackground(&gitlocal.Background{SSHAgent: sshAgent().Socket})
			return nil
		},
		Start: func(ctx context.Context, server *uiserver.Server) (daemon.App, error) {
			return localApplication(paths, logger)(ctx, server)
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
	_, err := daemon.Spawn(paths, exec.Command(executable, args...))
	return err
}

// resolveShell is a seam for tests.
var resolveShell = shellenv.Resolve

// recordedAgentsInterval is how often the daemon checks whether `baum
// desktop-setup` recorded the coding agents again.
const recordedAgentsInterval = 15 * time.Second

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
// shellenv.Merge). Only variable names are logged, and how long the shell
// took: the daemon listens only after this returns.
func applyLoginEnvironment(ctx context.Context, log *slog.Logger) {
	inherited := os.Environ()
	began := time.Now()
	result, err := resolveShell(ctx, shellenv.Options{Environ: inherited})
	took := time.Since(began).Round(time.Millisecond)
	var resolved map[string]string
	switch {
	case errors.Is(err, shellenv.ErrUnsupported):
		log.Info("using the inherited environment", "reason", err.Error(), "duration", took)
	case err != nil:
		log.Warn("login shell environment not read; using the inherited environment", "shell", result.Shell, "reason", err.Error(), "duration", took)
	case result.Shell == "":
		// Windows: the inherited environment with the registry's PATH.
		resolved = result.Env
		log.Info("read PATH from the registry", "duration", took)
	case result.Fallback != "":
		resolved = result.Env
		log.Warn("read the environment of a login shell that is not interactive", "shell", result.Shell, "reason", result.Fallback, "duration", took)
	default:
		resolved = result.Env
		log.Info("read the login shell environment", "shell", result.Shell, "duration", took)
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
func localApplication(paths daemon.Paths, log *slog.Logger) func(context.Context, *uiserver.Server) (daemon.App, error) {
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
		hooks := gitHooks(registry, log)
		discover := discovery.New(github.NewFromConfig(cfg, github.Options{}), "")
		// Every reader shares one cached index. Discovery's writes only
		// invalidate it: the index watcher publishes if the index moved.
		index := reviewindex.NewCache(ctx, &reviewindex.Service{Registry: registry, Discovery: discover})
		checkedChange := func() {
			index.Invalidate()
			server.CheckIndex()
		}
		discover.Updated = checkedChange
		changed := indexChanged(index, server.Publish)
		worker := &background.Service{Registry: registry, Discovery: discover, Saved: background.Saved, Budget: discover.GraphQLBudget, Refresh: background.Local{Client: github.NewFromConfig(cfg, github.Options{}), Config: cfg}.Refresh, Retain: background.Retention{Log: log}.Retain, Changed: checkedChange}
		database, err := appdb.Path()
		if err != nil {
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		state := filepath.Dir(database)
		// Jobs are priced with the newest verified list, so reinstall the
		// downloaded one before workers start.
		if err := pricing.LoadPersisted(state); err != nil {
			log.Warn("stored price list not loaded", "error", err)
		}
		// Review groups live in the daemon's state, not in a clone.
		groups, err := reviewgroups.Open(database)
		if err != nil {
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		// Automatic generation is separate from the model-free background
		// refresh: discovery hands it snapshots, and it queues through the
		// same runner as confirmed jobs.
		scheduler := &automatic.Scheduler{Registry: registry, Path: database, Discover: discover.PollNow, Groups: groups, Log: log,
			ClaudeSignedIn: claude.SignedInWithConfigDir(cfg.BackendPaths[config.BackendClaude]),
			CodexSignedIn:  codex.SignedInWithHome(cfg.BackendPaths[config.BackendCodex]),
			Hold: func() bool {
				status, ok := daemon.Self()
				return ok && (status.Stopping || status.RestartPending)
			},
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) },
		}
		// Notices are what the desktop app turns into notifications; they
		// carry the pull request's title and risk score, read when they happen.
		notify := &notices.Service{Registry: registry, Index: index.Snapshot, Publish: func(n notices.Notice) { _ = server.Publish("notice", n) }}
		var identityMu sync.RWMutex
		worker.Discovered = func(ctx context.Context, snapshot discovery.Snapshot) {
			identityMu.RLock()
			defer identityMu.RUnlock()
			if snapshot.Epoch != discover.Epoch() {
				return
			}
			// Stacked pull requests are grouped for review by default.
			if !snapshot.Stale && snapshot.Error == "" {
				if synced, err := groups.SyncStacks(ctx, snapshot.Repository, snapshot.PullRequests, snapshot.Observations); err != nil {
					log.Warn("stack groups not updated", "repository", snapshot.Repository, "error", err)
				} else if synced {
					changed()
				}
			}
			scheduler.Observe(ctx, snapshot)
			notify.Observe(ctx, snapshot)
		}
		scheduler.Discovered = worker.Discovered
		chats := &chat.Service{Registry: registry, Config: cfg, Log: log}
		// Feature builds run on the feature worker made below.
		builds := &featureBuilder{}
		prefs, _ := registry.Preferences(ctx)
		// Held until the application is wired below: a journaled feature
		// build needs the feature log, and the queue's logger, usage events
		// and chat service are set after it opens.
		queue, err := jobs.OpenHeld(database, &jobs.Local{Registry: registry, Groups: groups, Config: cfg, Client: github.NewFromConfig(cfg, github.Options{}), Features: builds, Chat: chats, Log: log}, prefs.Concurrency(), func(job jobs.Job) {
			_ = server.PublishProgress(job)
			if event, ok := jobs.ChatEvent(job); ok {
				_ = server.Publish("chat", event)
			}
			if job.Phase == "finished" {
				changed()
				// Reading the index takes a moment; the queue must not wait.
				go notify.JobDone(ctx, job)
				// A PR whose job finished may have a pending head.
				scheduler.Wake()
				// A fix pushed a new head: show it at once, and poll again
				// until its checks appear, which takes GitHub a few seconds.
				if repo, err := github.ParseRepository(job.Repository); err == nil && job.Kind == jobs.KindFix && job.Commit != "" {
					go discovery.AfterPush(ctx, discover.PollNow, repo, job.PR, job.Commit, changed, nil)
				}
			}
		})
		if err != nil {
			_ = groups.Close()
			_ = registry.Close()
			events.Close()
			return daemon.App{}, err
		}
		queue.SetLogger(log)
		// A running job's usage, reported every few seconds, wakes no full
		// read in the app or the tray.
		queue.SetUsageChanged(func(usage jobs.Usage) { _ = server.PublishUsage(usage) })
		scheduler.Queue = queue
		queue.SetAutomaticGate(scheduler.Gate)
		queue.SetChat(chats)
		hookRefresh := &hookrefresh.Service{Registry: registry, Cached: discover.Cached, Refresh: worker.RefreshNow, Trigger: scheduler.Trigger, Changed: changed, Log: log}
		// Subscribed before the spool drains or the socket serves, so no
		// event arrives unseen.
		refreshEvents, unsubscribe := events.Subscribe(hookrefresh.Buffer, hookrefresh.Match)
		// A clone found for a picked repository may let the scheduler start work.
		clones := newCloneFinder(registry, events, func() {
			changed()
			scheduler.Wake()
		}, log)
		// The feature log follows the decision capture gate, so a settings
		// change wakes it too.
		var featureLog *decisionlog.FeatureWorker
		// The Timeline page reads the decision log again on this event; a
		// burst of writes is one event a second.
		timelineChanged := coalesce(time.Second, func() {
			if err := server.Publish("timeline", struct{}{}); err != nil {
				log.Warn("timeline event not published", "error", err)
			}
		})
		settingsChanged := func() {
			changed()
			// A repository's decisions may just have been turned on or off.
			timelineChanged()
			scheduler.Wake()
			featureLog.Wake()
			// A repository may just have been picked.
			clones.Trigger()
		}
		cloneEvents, cloneDropped, unsubscribeClones := events.SubscribeCounted(clonefind.Buffer, clonefind.MatchEvent)
		// The live transcripts are written as the review page's are, and a
		// waiting session's message is masked the same way.
		renderer := transcript.NewRenderer("")
		// The list and the tracker share what was confirmed of Claude Code's
		// processes.
		claude := &livesessions.ClaudeProcesses{}
		live := events.liveSessions(paths, registry, discover, server, log, claude)
		var liveAPI, requestsAPI http.Handler
		var requests *agentrequests.Store
		var requestEvents <-chan hookevents.Record
		unsubscribeRequests := func() {}
		var transcripts *livesessions.Transcripts
		if live != nil {
			live.Mask = func(text string) string { return renderer.Mask(ctx, text) }
			// A session that starts waiting for a permission or an answer is
			// a notice; reading the registry for it must not hold the tracker.
			live.Waiting = func(session livesessions.Session) { go notify.SessionWaiting(ctx, session) }
			transcripts = &livesessions.Transcripts{Tracker: live, Render: renderer.Render, Log: log, Senders: liveSenders(), Enders: liveEnders(), EndChecks: liveEndChecks(claude), Focus: &focus.Focuser{}}
			liveAPI = transcripts.Handler()
			// What agents wait on the app to answer, and the messages queued
			// for running sessions, stay in memory, masked as the sessions
			// are; only the Codex setting is kept, in the state directory.
			// The app reads them with the live sessions.
			requests = &agentrequests.Store{Sessions: live, Mask: live.Mask, Database: database, Log: log,
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
		// Decision capture follows the tracked sessions; its store keeps the
		// derived, redacted decisions in the daemon's state.
		var capture *decisionlog.Scheduler
		var decisionStore *decisionlog.Store
		var decisionRelevance *decisionlog.Relevance
		var timeline http.Handler
		// The GitHub connection's login, once it exists below.
		var connectionLogin func() string
		if live != nil {
			if decisionStore, err = decisionlog.Open(database, decisionlog.OpenOptions{AbandonPending: true}); err != nil {
				log.Warn("decision capture is off: its store did not open", "error", err)
			} else {
				decisionStore.Changed = func() { _ = server.Publish("decisions", struct{}{}) }
				decisionRelevance = &decisionlog.Relevance{Store: decisionStore, Registry: registry, Config: cfg, Patch: decisionlog.LocalRelevancePatch(registry), Log: log}
				capture = &decisionlog.Scheduler{Tracker: live, Source: events.service.Store, Log: log, Changed: timelineChanged,
					Worker: &decisionlog.Capturer{Store: decisionStore, Registry: registry, Config: cfg, Log: log, Changed: timelineChanged}}
				featureLog = &decisionlog.FeatureWorker{Store: decisionStore, Registry: registry, GitHub: githubReaders(cfg), Config: cfg, Log: log, Changed: func() { timelineChanged(); decisionStore.Changed() }}
				builds.worker.Store(featureLog)
				timeline = (&decisionlog.Timeline{Store: decisionStore, Registry: registry, Clone: decisionlog.LocalClones(registry), Transcripts: transcripts,
					Login: func(ctx context.Context) string { return viewerLogin(ctx, registry, discover.Cached, connectionLogin) }}).Handler()
			}
		}
		home, _ := os.UserHomeDir()
		pickList := &githubrepos.Service{
			Lister: github.NewFromConfig(cfg, github.Options{}), Registry: registry,
			Token:  func(ctx context.Context) (string, error) { return cfg.ResolveGitHubToken(ctx, config.GitHubHost) },
			Health: config.GitHubAuth.Health, Home: home, Context: ctx, Changed: settingsChanged,
			Organizations: func(ctx context.Context, refresh bool) (githubauth.OrganizationAccess, error) {
				return config.DeviceOrganizations(ctx, cfg, refresh)
			},
		}
		// The delivery figures' searches, cached per repository, and the
		// review threads and failing checks the Inbox lists; an account
		// change forgets both.
		delivery := &analytics.Delivery{Search: github.NewFromConfig(cfg, github.Options{}), Paused: worker.Paused, Limited: worker.Limited}
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
		connectionLogin = githubConnection.Login
		githubConnection.Organizations = func(ctx context.Context, refresh bool) (githubauth.OrganizationAccess, error) {
			return config.DeviceOrganizations(ctx, cfg, refresh)
		}
		// A write to a pull request re-reads the repository's pull requests so
		// the inbox shows its new state.
		githubClient := github.NewFromConfig(cfg, github.Options{})
		// An earlier name GitHub no longer forwards stops reading as the
		// repository it was recorded for.
		go func() {
			if _, err := registry.RecheckAliases(ctx, "", githubClient.CurrentName); err != nil {
				log.Warn("earlier repository names not rechecked", "error", err)
			}
		}()
		// The re-read of a busy repository takes seconds and may wait behind
		// a background poll, so it runs after the write has answered and
		// tells the app when it lands.
		refreshRepository := func(_ context.Context, repo github.Repository) {
			go func() {
				if _, err := discover.PollNow(ctx, repo); err == nil {
					changed()
				}
			}()
		}
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
		// The plans of the Claude Code configuration directories and Codex
		// homes Settings chose for runs show beside the daemon's own.
		plans.Others = func(ctx context.Context) []planusage.Other {
			prefs, err := registry.Preferences(ctx)
			if err != nil {
				return nil
			}
			var others []planusage.Other
			for _, use := range []struct {
				provider string
				agent    agentdirs.Agent
				dirs     []string
			}{
				{"claude", agentdirs.Claude, prefs.ClaudeConfigDirs(cfg)},
				{"codex", agentdirs.Codex, prefs.CodexHomes(cfg)},
			} {
				own := use.agent.Current()
				for _, dir := range use.dirs {
					if dir != own {
						others = append(others, planusage.Other{Provider: use.provider, Dir: dir})
					}
				}
			}
			return others
		}
		planHistory := &planusage.History{Path: database, Service: plans}
		actions := &analytics.Actions{Path: database}
		sessionIndex := &analytics.Index{Path: database, Clones: registry, Log: log}
		insights := &analytics.Service{Registry: registry, Index: sessionIndex, Jobs: queue.List, Actions: actions, Plans: planHistory, Delivery: delivery}
		var decisionsAPI func(uiserver.LinkedSessions) http.Handler
		if decisionStore != nil {
			insights.Captures = decisionStore.Ledger
			insights.Pulls = decisionStore.PullFlow
			insights.Login = func(ctx context.Context) string { return viewerLogin(ctx, registry, discover.Cached, connectionLogin) }
			decisionsAPI = func(linked uiserver.LinkedSessions) http.Handler {
				return (&decisionlog.API{Store: decisionStore, Registry: registry, Live: live.Sessions, Linked: linked, Relevance: decisionRelevance, Log: log}).Handler()
			}
		}
		// A pull request with no saved review shows its facts, read when its
		// page asks, within the background refresh's window and rate pause.
		pageFacts := &facts.Service{Build: facts.Local{Client: githubClient, Config: cfg}.Build, Window: worker.RefreshInterval(), Paused: worker.Paused, Limited: worker.Limited}
		// A session that ended has its last lines written: read it soon.
		endedSessions, unsubscribeEnded := events.Subscribe(4, func(e hookevents.Event) bool {
			return e.Kind == hookevents.KindAgent && e.Name == "session-end"
		})
		// The panel's git reads (conflicting files) use the first available
		// clone of the repository.
		availableClone := func(ctx context.Context, repo github.Repository) (string, bool) {
			clones, err := registry.List(ctx)
			if err != nil {
				return "", false
			}
			for _, c := range clones {
				if c.Available && strings.EqualFold(c.Repository, repo.String()) {
					return c.Path, true
				}
			}
			return "", false
		}
		groupsAPI := &reviewgroups.API{Store: groups, Registry: registry, Index: index.Snapshot, Changed: changed}
		uiState := &uistate.Store{Path: database}
		savedInboxes := inboxes.Open(database)
		// The app's Inbox, and which saved inboxes hold each pull request;
		// the desktop app's badge and tray count what it lists.
		inboxQueue := &inboxqueue.Service{Index: index.Snapshot, Jobs: queue.ActiveFixes, Groups: groups.List, Snoozes: func() (json.RawMessage, error) {
			record, err := uiState.Read("inbox", "")
			return record.Value, err
		}, Inboxes: func() ([]inboxes.Inbox, error) {
			state, err := savedInboxes.Read()
			return state.Inboxes, err
		}}
		inboxesAPI := &inboxes.API{Store: savedInboxes, Changed: changed}
		var lastHookTrust uiserver.LastHookTrust
		local := uiserver.LocalAPI{GitHubAuth: githubConnection.Handler(), Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, askedRefresh{discover, worker.Asked}, changed), Index: inboxQueue.CountHandler(index.Handler()), Inbox: inboxQueue.Handler(), Jobs: queue.Handler(), Chat: uiserver.ChatHandler(registry, queue), Background: worker.Handler(), Automatic: scheduler.Handler(), Live: liveAPI, AgentRequests: requestsAPI, Clones: clones.Handler(), GitHubRepositories: pickList.Handler(), CurrentName: currentName(githubClient), PlanUsage: plans.Handler(), PlanUsageCached: func() []json.RawMessage {
			var cached []json.RawMessage
			for _, plan := range plans.Cached() {
				if raw, err := json.Marshal(plan); err == nil {
					cached = append(cached, raw)
				}
			}
			return cached
		}, Analytics: insights.Handler(), Timeline: timeline, Merge: uiserver.MergeHandler(registry, actions.RecordMerges(githubClient), mergedPullRequest, changed), Review: uiserver.ReviewHandler(registry, githubClient, changed), Update: uiserver.UpdateHandler(registry, actions.RecordUpdates(prupdate.Service{Client: githubClient, Cache: panelCache, Counted: countedThreads, Clone: availableClone}), refreshRepository, changed), Changed: settingsChanged,
			UIState:   uiState.Handler(),
			Inboxes:   inboxesAPI.Handler(),
			Groups:    groupsAPI.Handler(),
			Decisions: decisionsAPI, Facts: pageFacts, Log: log,
			DecisionsChanged:   func() { _ = server.Publish("decisions", struct{}{}) },
			ConcurrencyChanged: func(n int) { _ = queue.SetConcurrency(n) },
			DecisionModel: func(ctx context.Context, prefs repos.Preferences) uiserver.DecisionModel {
				e := decisionlog.EffectiveModel(ctx, cfg, prefs, generate.Backends{}, nil)
				return uiserver.DecisionModel{Backend: e.Backend, Undetermined: e.Undetermined, Model: e.Model, Ignored: e.Ignored}
			},
			// The MCP server reports the newest answer and never reads again.
			HookTrust: lastHookTrust.Wrap(func(ctx context.Context) agentplugins.HookTrust {
				home, err := os.UserHomeDir()
				if err != nil {
					return agentplugins.HookTrust{State: "unknown"}
				}
				dirs := []string{home}
				clones, err := registry.List(ctx)
				if err != nil {
					return agentplugins.HookTrust{State: "unknown"}
				}
				for _, clone := range clones {
					if clone.Available {
						dirs = append(dirs, clone.Path)
					}
				}
				return agentplugins.New().CodexTrust(ctx, dirs)
			}),
			Diagnostics: uiDiagnostics, Version: version()}
		api := local.Handler()
		// The tour's sample project, written when first asked for.
		sample := &sampleapp.Workspace{Dir: filepath.Join(state, "sample"), Own: api}
		// What a chat proposes is saved, when the person saves it, through
		// the pages' own saves.
		proposers := &uiserver.Proposers{API: local, Automatic: scheduler, Inboxes: inboxesAPI, Groups: groupsAPI, UIState: uiState}
		if requests != nil {
			proposers.CodexApprovals = func() bool { return requests.Settings().CodexApprovals }
			proposers.SetCodexApprovals = func(on bool, check func(bool) error) error {
				_, err := requests.SetSettingsIf(agentrequests.Settings{CodexApprovals: on}, func(current agentrequests.Settings) error { return check(current.CodexApprovals) })
				return err
			}
		}
		// Agents' MCP server; an action's reads resolve clone ids to check
		// they stay in its repository.
		mcp := agentcontext.New(agentcontext.Options{API: api, Version: version(), PlanUsage: local.CachedPlans, PlanChoices: local.CachedPlanChoices, CodexHooks: lastHookTrust.Get, DecisionSettings: (&uiserver.DecisionViews{API: local}).Get, Proposers: proposers.Pages(), PickList: func(ctx context.Context, query string) (agentcontext.PickList, error) {
			listing, loaded, err := pickList.Cached(ctx, query)
			if err != nil {
				return agentcontext.PickList{}, err
			}
			out := agentcontext.PickList{Loaded: loaded, Total: listing.TotalCount, More: listing.HasMore}
			for _, r := range listing.Repositories {
				out.Repositories = append(out.Repositories, agentcontext.PickRepository{Repository: r.Repository, Private: r.Private, Fork: r.Fork,
					Description: r.Description, PushedAt: r.PushedAt, Picked: r.Picked, Cloned: r.Clone != ""})
			}
			return out, nil
		}, Clone: func(ctx context.Context, id string) (string, error) {
			clones, err := registry.CloneRows(ctx)
			if err != nil {
				return "", err
			}
			for _, c := range clones {
				if c.ID == id {
					return c.Repository, nil
				}
			}
			return "", errors.New("unknown clone")
		}})
		// Everything the journaled jobs need is wired: start them.
		queue.Resume()
		return daemon.App{
			API:     api,
			Assets:  localapp.Handler(),
			Sample:  sample,
			MCP:     mcp,
			Methods: methods,
			Waits:   waits,
			Run: func(ctx context.Context) {
				var wg sync.WaitGroup
				wg.Go(func() { events.Run(ctx) })
				if refreshEvents != nil {
					wg.Go(func() { hookRefresh.Run(ctx, refreshEvents) })
				}
				wg.Go(func() {
					if err := pricing.Refresh(ctx, state); err != nil && ctx.Err() == nil {
						log.Warn("price list not refreshed", "error", err)
					}
				})
				wg.Go(func() { clones.Run(ctx, cloneEvents, cloneDropped) })
				wg.Go(func() { pickList.Run(ctx) })
				wg.Go(func() { worker.Run(ctx) })
				wg.Go(func() { scheduler.Run(ctx) })
				if live != nil {
					wg.Go(func() { live.Run(ctx) })
					// A running session's progress is in its transcript, not
					// in hook events.
					wg.Go(func() { transcripts.Watch(ctx, func() { _ = server.Publish("live", struct{}{}) }) })
				}
				if capture != nil {
					wg.Go(func() { decisionRelevance.Run(ctx) })
					wg.Go(func() { capture.Run(ctx) })
					wg.Go(func() { featureLog.Run(ctx) })
				}
				if requestEvents != nil {
					wg.Go(func() { requests.Run(ctx, requestEvents) })
				}
				wg.Go(func() { sessionIndex.Run(ctx) })
				wg.Go(func() { maintainReviewRuns(ctx, log) })
				wg.Go(func() { hooks.Run(ctx) })
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
								api.InvalidateSessions()
								sessionIndex.Trigger()
							}
						}
					})
				}
				wg.Go(func() {
					server.WatchIndex(ctx, 0, uiserver.IndexWatch{
						Fingerprint: reviewindex.Fingerprint(registry.RepositoryRows, func() string { return strconv.FormatUint(config.GitHubAuth.Revision(), 10) }),
						Invalidate:  index.Invalidate,
						Fresh:       index.Fresh,
						Snapshot: func(ctx context.Context) (any, uint64, error) {
							snapshot, version, err := index.Current(ctx)
							return indexWatchValue(snapshot, worker.Errors()), version, err
						},
					})
				})
				wg.Wait()
			},
			Shutdown:     queue.Shutdown,
			Drain:        queue.Drain,
			DrainRunning: queue.DrainRunning,
			Resume:       queue.Resume,
			Close: func() {
				sample.Close()
				api.Close()
				groupsAPI.Close()
				unsubscribe()
				unsubscribeClones()
				unsubscribeEnded()
				unsubscribeRequests()
				events.Close()
				if decisionStore != nil {
					_ = decisionStore.Close()
				}
				_ = groups.Close()
				_ = registry.Close()
			},
		}, nil
	}
}

// askedRefresh is discovery as the app's refresh button reaches it: a
// refresh the person asks for also has the background refresh read the
// repository's open saved reviews once more, and again those whose reads
// failed, including those it stopped reading until their pull request
// changes (background.Service.Asked).
type askedRefresh struct {
	*discovery.Service
	asked func(repository string)
}

func (a askedRefresh) PollNow(ctx context.Context, repo github.Repository) (discovery.Snapshot, error) {
	a.asked(repo.String())
	return a.Service.PollNow(ctx, repo)
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
func (h hookEvents) liveSessions(paths daemon.Paths, registry *repos.Registry, discover *discovery.Service, server *uiserver.Server, log *slog.Logger, claude *livesessions.ClaudeProcesses) *livesessions.Tracker {
	if h.service == nil {
		return nil
	}
	return &livesessions.Tracker{
		Source: h.service.Store,
		Linker: &livesessions.Resolver{Registry: registry, Discovery: discover},
		// Ended sessions are kept for a day, longer than the record may reach,
		// and a Claude Code session waiting for its user while its process runs.
		StateDir:     hookevents.RecordDir(paths),
		Attributions: &commitattribution.Store{},
		Alive:        liveAlive(claude),
		Notify:       func() { _ = server.Publish("live", struct{}{}) },
		Log:          log,
	}
}

// liveAlive is how the tracker tells a silent session's agent still runs:
// Claude Code registers its processes (macOS and Linux). Codex's run in a
// shared app server, so there is none for them.
func liveAlive(claude *livesessions.ClaudeProcesses) func(agent, id string) bool {
	if runtime.GOOS == "windows" {
		return nil
	}
	return claude.Alive
}

// liveSenders are how the app's messages reach a waiting session: Claude
// Code's inbox socket (macOS and Linux; on Windows it is a named pipe), and
// `codex queue` when codex is on PATH.
func liveSenders() map[string]livesessions.Sender {
	senders := map[string]livesessions.Sender{}
	if runtime.GOOS != "windows" {
		senders[livesessions.AgentClaudeCode] = livesessions.ClaudeInbox{}.Send
	}
	if codex, ok := livesessions.NewCodexQueue(); ok {
		senders[livesessions.AgentCodex] = codex.Send
	}
	return senders
}

// liveEnders are how the app ends a waiting session: Claude Code's as /exit
// does (macOS and Linux). Codex has no clean way to close a session from
// outside, so it has none.
func liveEnders() map[string]livesessions.Ender {
	if runtime.GOOS == "windows" {
		return nil
	}
	return map[string]livesessions.Ender{livesessions.AgentClaudeCode: livesessions.ClaudeExit{}.End}
}

// liveEndChecks tell the list whether liveEnders would end a session now,
// so it offers End only where ending would go through: a Claude Code
// session still running a background command is not offered it. End itself
// confirms the process again.
func liveEndChecks(claude *livesessions.ClaudeProcesses) map[string]livesessions.EndChecker {
	if runtime.GOOS == "windows" {
		return nil
	}
	return map[string]livesessions.EndChecker{livesessions.AgentClaudeCode: claude.EndCheck}
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

// currentName asks GitHub for the name a github.com repository has now.
func currentName(client *github.Client) repos.CurrentName {
	return func(ctx context.Context, repository string) (string, error) {
		repo, err := github.ParseRepository(repository)
		if err != nil {
			return "", err
		}
		return client.CurrentName(ctx, repo)
	}
}

// gitHooks builds the service that keeps baum's Git hook dispatchers in the
// registered clones that are present, ensures each clone the registry stores
// from now on and gives a clone it drops its own hooks back. Only the daemon's
// registry does this: the CLI's installs no hooks.
func gitHooks(registry *repos.Registry, log *slog.Logger) *githooks.Service {
	convert := func(clones []repos.Clone, available bool) []githooks.Clone {
		var out []githooks.Clone
		seen := map[string]bool{}
		for _, c := range clones {
			// Linked worktrees of one repository share its hooks.
			if (c.Available || !available) && (!available || !seen[c.CommonDir]) {
				seen[c.CommonDir] = true
				out = append(out, githooks.Clone{ID: c.ID, Path: c.Path, CommonDir: c.CommonDir})
			}
		}
		return out
	}
	service := &githooks.Service{Log: log,
		Clones: func(ctx context.Context) ([]githooks.Clone, error) {
			clones, err := registry.List(ctx)
			return convert(clones, true), err
		},
		Registered: func(ctx context.Context) ([]githooks.Clone, error) {
			clones, err := registry.CloneRows(ctx)
			return convert(clones, false), err
		},
	}
	// A dispatcher falls back to this binary when baum is not on the PATH of
	// the process Git runs.
	if exe, err := os.Executable(); err == nil {
		service.Options.Baum = exe
	}
	toClone := func(c repos.Clone) githooks.Clone {
		return githooks.Clone{ID: c.ID, Path: c.Path, CommonDir: c.CommonDir}
	}
	registry.Registered = func(c repos.Clone) { service.Added(toClone(c)) }
	registry.Removed = func(c repos.Clone) { service.Removed(toClone(c)) }
	return service
}

// maintainReviewRuns expires the run directories of reviews that kept their
// agent session, at start and then daily.
func maintainReviewRuns(ctx context.Context, log *slog.Logger) {
	root, err := reviewruns.DefaultRoot()
	if err != nil {
		return
	}
	reviewruns.Maintain(ctx, root, 24*time.Hour, func(err error) { log.Warn("expiring kept review runs", "error", err) })
}

// indexWatchValue excludes a successful discovery poll's bookkeeping time.
// The app ages applied observations by ObservedAt; an unchanged list fetch
// changes no visible row. Keep the original snapshot intact for API readers.
func indexWatchValue(snapshot reviewindex.Snapshot, health map[string]string) any {
	snapshot.Entries = slices.Clone(snapshot.Entries)
	for i := range snapshot.Entries {
		snapshot.Entries[i].MetadataFetchedAt = ""
	}
	return struct {
		Index  reviewindex.Snapshot
		Errors map[string]string
	}{snapshot, health}
}

// indexChanged announces a change to the review index. It invalidates the
// cache before the event, so a client reading again after it sees the change.
func indexChanged(index *reviewindex.Cache, publish func(string, any) error) func() {
	return func() {
		index.Invalidate()
		_ = publish("index", struct{}{})
	}
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
// list: the page shows what the startup refresh produced. It reports for the
// desktop app, which runs jobs with Claude Code and Codex only.
func uiDiagnostics(ctx context.Context) doctor.Report {
	options := doctorOptions(false)
	options.Desktop = true
	return doctor.Run(ctx, options)
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

// coalesce calls f at most once every interval: a call within interval of
// the last one is made when the interval ends, and calls meanwhile join it.
func coalesce(every time.Duration, f func()) func() {
	var mu sync.Mutex
	var last time.Time
	pending := false
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if pending {
			return
		}
		wait := every - time.Since(last)
		if wait <= 0 {
			last = time.Now()
			f()
			return
		}
		pending = true
		time.AfterFunc(wait, func() {
			mu.Lock()
			pending, last = false, time.Now()
			mu.Unlock()
			f()
		})
	}
}

// viewerLogin is the user's GitHub login for the Timeline: the viewer of a
// picked repository's cached discovery snapshot, which follows the token
// actually in use, else the GitHub connection's; "" when neither knows.
func viewerLogin(ctx context.Context, registry *repos.Registry, cached func(context.Context, github.Repository) (discovery.Snapshot, error), connection func() string) string {
	list, err := registry.Repositories(ctx)
	if err == nil {
		for _, r := range list {
			repo, err := github.ParseRepository(r.Repository)
			if err != nil {
				continue
			}
			if snapshot, err := cached(ctx, repo); err == nil && snapshot.Viewer != "" {
				return snapshot.Viewer
			}
		}
	}
	if connection != nil {
		return connection()
	}
	return ""
}

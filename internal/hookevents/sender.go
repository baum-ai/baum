package hookevents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Budget bounds what Send adds to a hook, whatever the daemon does. The
// request to the daemon gets RequestBudget of it; spooling the event and
// starting a daemon that is not running get the rest. Agents wait for their
// hooks, so the budget is what the user could notice.
const (
	Budget        = 300 * time.Millisecond
	RequestBudget = 150 * time.Millisecond
)

// Hook log bounds: hooks log only what they spool, drop or start, to
// hooks.log in the daemon's state directory.
const (
	maxHookLogBytes = 256 << 10
	hookLogBackups  = 1
)

// HookLogFile is where hooks log the events they spool or drop.
func HookLogFile(paths daemon.Paths) string { return filepath.Join(paths.State, "hooks.log") }

// Outcome is what became of an event.
type Outcome int

const (
	// Delivered: the daemon recorded the event (or already had).
	Delivered Outcome = iota
	// Spooled: the event waits in the spool for the daemon to drain it.
	Spooled
	// Dropped: the event could not be spooled; the hook log says why.
	Dropped
	// Unfinished: the budget ended before the event was spooled. The spool
	// write goes on while the process lives; it is atomic, so an exit leaves
	// the event spooled whole or not at all.
	Unfinished
)

// Environment variables that keep a hook from starting a daemon. Events still
// reach a running daemon, or the spool.
const (
	// NoDaemonEnv set to 1 opts out of starting one.
	NoDaemonEnv = "BAUM_NO_DAEMON"
	// ciEnv is set by CI services, where a background daemon outliving a job
	// is unwanted.
	ciEnv = "CI"
)

// Sender delivers a hook's event to the user's daemon.
type Sender struct {
	Paths daemon.Paths
	// Version is this baum's version, compared with the daemon's.
	Version string
	// Spawn starts a daemon that is not running; nil starts this executable.
	Spawn daemon.Spawner
	// Executable is what an older daemon is asked to restart into; empty is
	// this process's executable.
	Executable string
	// Budget and RequestBudget override the package's; zero keeps them.
	Budget, RequestBudget time.Duration
	// Log receives what the sender spools, drops or starts; nil appends to
	// HookLogFile.
	Log *slog.Logger
	// beforeSpool runs before the event is spooled; tests slow the spool.
	beforeSpool func()
}

// Send hands e to the daemon and never fails the hook. It returns within the
// budget whatever the daemon, the file system or a process start does: what
// is not done by then is left to finish while the hook's process lives.
//
//   - A daemon that records it in time: Delivered.
//   - No daemon: e is spooled and a daemon is started, without waiting for
//     it to serve; it drains the spool when it starts. None is started when
//     the user stopped the daemon (`baum daemon stop`), BAUM_NO_DAEMON=1 or CI
//     is set, or one was started less than daemon.StartBackoff ago and has
//     not served yet.
//   - A daemon that does not answer in time, answers with an error, or does
//     not know Method or e's Version: e is spooled for the next drain. A
//     daemon older than this baum is asked to restart as this version once
//     it is idle, as `baum daemon ensure` would, without waiting for it.
//
// An event that cannot be spooled either is dropped, with the reason logged.
func (s *Sender) Send(ctx context.Context, e Event) Outcome {
	e.Bound()
	budget, requestBudget := s.Budget, s.RequestBudget
	if budget <= 0 {
		budget = Budget
	}
	if requestBudget <= 0 {
		requestBudget = RequestBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	client := &daemon.Client{Paths: s.Paths, Version: s.Version, Spawn: s.Spawn, Executable: s.Executable,
		Start: daemon.StartOptions{Env: daemonEnvironment()}}
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestBudget)
	response, callErr := client.Call(requestCtx, Method, e)
	cancelRequest()
	if callErr == nil && response.OK {
		return Delivered
	}
	done := make(chan Outcome, 1)
	go func() { done <- s.fallBack(ctx, client, e, response, callErr) }()
	select {
	case outcome := <-done:
		return outcome
	case <-ctx.Done():
		return Unfinished
	}
}

// fallBack spools e after the daemon did not record it, then starts or
// upgrades the daemon.
func (s *Sender) fallBack(ctx context.Context, client *daemon.Client, e Event, response daemon.Response, callErr error) Outcome {
	log, closeLog := s.logger()
	defer closeLog()
	reason := "the baum daemon is not running"
	switch {
	case callErr != nil && !errors.Is(callErr, daemon.ErrNotRunning):
		reason = callErr.Error()
	case callErr == nil && response.Error != nil:
		reason = "the baum daemon refused it: " + response.Error.Code
	case callErr == nil:
		reason = "the baum daemon answered without recording it"
	}
	if s.beforeSpool != nil {
		s.beforeSpool()
	}
	outcome := Spooled
	spool := &Spool{Dir: SpoolDir(s.Paths), Log: log}
	if err := spool.Put(e); err != nil {
		outcome = Dropped
		log.Warn("hook event dropped", "kind", e.Kind, "name", e.Name, "reason", reason, "spool_error", err.Error())
	} else {
		log.Info("hook event spooled", "kind", e.Kind, "name", e.Name, "reason", reason)
	}
	switch {
	case errors.Is(callErr, daemon.ErrNotRunning):
		s.start(ctx, client, log)
	case callErr == nil && response.Error != nil:
		switch response.Error.Code {
		case daemon.CodeProtocolMismatch, daemon.CodeUnknownMethod, CodeEventVersion:
			s.upgrade(ctx, client, response.Status, log)
		}
	}
	return outcome
}

// start starts a daemon unless the user or the environment rules it out.
func (s *Sender) start(ctx context.Context, client *daemon.Client, log *slog.Logger) {
	switch {
	case os.Getenv(NoDaemonEnv) == "1":
		log.Info("not starting the baum daemon: " + NoDaemonEnv + "=1")
		return
	case os.Getenv(ciEnv) != "":
		log.Info("not starting the baum daemon: CI is set")
		return
	case daemon.StoppedByUser(s.Paths):
		log.Info("not starting the baum daemon: it was stopped with `baum daemon stop`; `baum daemon ensure` starts it again")
		return
	}
	started, err := client.StartDetached(ctx)
	switch {
	case errors.Is(err, daemon.ErrStartBackoff):
		log.Info("not starting the baum daemon: one was started moments ago and has not served yet")
	case err != nil:
		log.Warn("baum daemon not started", "error", err.Error())
	case started:
		log.Info("started the baum daemon")
	}
}

// daemonEnvironment is the environment a hook starts the daemon with: its
// own, without TMPDIR, which agent sandboxes point at a directory that lasts
// one session. The daemon then uses the system's.
func daemonEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TMPDIR=") {
			env = append(env, entry)
		}
	}
	return env
}

// upgrade asks a daemon older than this baum to restart as this version once
// no job is queued or running. A daemon of this release restarts itself even
// when idle (StopParams.Respawn), so nobody has to wait for it to exit; an
// older one that ignores that stops when idle, and this hook, or the next,
// starts this version, which drains the spool.
func (s *Sender) upgrade(ctx context.Context, client *daemon.Client, status *daemon.Status, log *slog.Logger) {
	if status == nil {
		hello, err := client.Hello(ctx)
		if err != nil {
			return
		}
		status = &hello
	}
	older := status.Protocol < daemon.Protocol || (status.Protocol == daemon.Protocol && daemon.Older(status.Version, s.Version))
	if !older || status.RestartPending || status.Stopping {
		return
	}
	executable := s.Executable
	if executable == "" {
		self, err := os.Executable()
		if err != nil {
			return
		}
		executable = self
	}
	response, err := client.Call(ctx, "stop", daemon.StopParams{WhenIdle: true, Executable: executable, Respawn: true})
	if err != nil || !response.OK {
		return
	}
	log.Info("asked the older baum daemon to restart as this version once idle", "daemon_version", status.Version, "version", s.Version)
	if response.Status != nil && response.Status.Stopping {
		s.start(ctx, client, log)
	}
}

// logger is s.Log, or the hook log opened for this send.
func (s *Sender) logger() (*slog.Logger, func()) {
	if s.Log != nil {
		return s.Log, func() {}
	}
	if err := privatefs.MkdirAll(s.Paths.State); err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}
	}
	file, err := daemon.OpenLog(HookLogFile(s.Paths), maxHookLogBytes, hookLogBackups)
	if err != nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil)), func() {}
	}
	return slog.New(slog.NewTextHandler(file, nil)).With("pid", os.Getpid()), func() { _ = file.Close() }
}

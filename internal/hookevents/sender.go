package hookevents

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
}

// Send hands e to the daemon within the budget and never fails the hook.
//
//   - A daemon that records it in time: Delivered.
//   - No daemon: e is spooled and a daemon is started, without waiting for
//     it to serve; it drains the spool when it starts.
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
	client := &daemon.Client{Paths: s.Paths, Version: s.Version, Spawn: s.Spawn, Executable: s.Executable}
	requestCtx, cancelRequest := context.WithTimeout(ctx, requestBudget)
	response, callErr := client.Call(requestCtx, Method, e)
	cancelRequest()
	if callErr == nil && response.OK {
		return Delivered
	}
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
		started, err := client.StartDetached(ctx)
		if err != nil {
			log.Warn("baum daemon not started", "error", err.Error())
		} else if started {
			log.Info("started the baum daemon")
		}
	case callErr == nil && response.Error != nil:
		switch response.Error.Code {
		case daemon.CodeProtocolMismatch, daemon.CodeUnknownMethod, CodeEventVersion:
			s.upgrade(ctx, client, response.Status, log)
		}
	}
	return outcome
}

// upgrade asks a daemon older than this baum to restart as this version once
// no job is queued or running. An idle one stops at once; the next hook or
// `baum daemon ensure` starts this version, which drains the spool.
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
	response, err := client.Call(ctx, "stop", daemon.StopParams{WhenIdle: true, Executable: executable})
	if err != nil || !response.OK {
		return
	}
	log.Info("asked the older baum daemon to restart as this version once idle", "daemon_version", status.Version, "version", s.Version)
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

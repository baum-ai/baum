package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/semver"

	"github.com/baum-ai/baum/internal/privatefs"
)

// ErrNotRunning means no daemon answers on the socket and none holds the lock.
var ErrNotRunning = errors.New("the baum daemon is not running")

// Client defaults.
const (
	// DefaultReadyTimeout covers a new daemon waiting for its predecessor
	// to finish stopping.
	DefaultReadyTimeout = DefaultLockTimeout + 15*time.Second
	// DefaultStopTimeout leaves a stopping daemon its shutdown timeout and
	// time to close.
	DefaultStopTimeout = DefaultShutdownTimeout + 15*time.Second
	requestTimeout     = 5 * time.Second
)

// StartOptions apply only when a client starts a daemon.
type StartOptions struct {
	Port        int
	Concurrency int
	// Env, when not nil, is the environment the daemon process starts with;
	// nil passes the caller's.
	Env []string
}

// StartBackoff is how long StartDetached waits after starting a daemon
// before it starts another, unless that daemon served meanwhile: a daemon
// that cannot start must not be started again by every hook.
const StartBackoff = 3 * time.Minute

// ErrStartBackoff means StartDetached started a daemon less than
// StartBackoff ago that has not served yet.
var ErrStartBackoff = errors.New("a baum daemon was started moments ago and has not served yet")

// Spawner starts a detached daemon process. done is closed when the process
// exits (it never is for a daemon that keeps running).
type Spawner func(paths Paths, start StartOptions) (done <-chan struct{}, err error)

// Client finds, starts and stops the user's daemon.
type Client struct {
	Paths Paths
	// Version is this baum's version, compared with the daemon's.
	Version string
	Start   StartOptions
	// Spawn starts a daemon; nil starts this executable as "baum daemon run".
	Spawn        Spawner
	ReadyTimeout time.Duration
	StopTimeout  time.Duration
	// Restarted is told when Ensure stops an older daemon to replace it.
	Restarted func(old Status)
	// Deferred is told when Ensure keeps an older daemon that is running jobs
	// and has asked it to restart as this version once they finish.
	Deferred func(old Status)
	// Executable is what an older daemon restarts into; empty is this
	// process's executable.
	Executable string
}

// StopParams are the optional parameters of "stop".
type StopParams struct {
	// WhenIdle asks the daemon to wait until no job is queued or running and
	// then restart as Executable; an idle daemon stops at once.
	WhenIdle   bool   `json:"when_idle,omitempty"`
	Executable string `json:"executable,omitempty"`
}

// socket is the daemon's own record of its socket, from its status file, so
// clients whose runtime directory differs (ssh, su, a sandbox) still find it;
// without a record it is Paths.Socket.
func (c *Client) socket() string {
	path := c.Paths.statusFile()
	info, err := os.Lstat(path)
	if err != nil || !privatefs.PrivateFile(path, info) || info.Size() > 4096 {
		return c.Paths.Socket
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c.Paths.Socket
	}
	var status Status
	if json.Unmarshal(data, &status) != nil || !filepath.IsAbs(status.Socket) {
		return c.Paths.Socket
	}
	return status.Socket
}

// Call sends one request and reads its response. A missing socket or a
// refused connection is ErrNotRunning; a socket in a directory that is not
// private to the user is refused.
func (c *Client) Call(ctx context.Context, method string, params any) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	socket := c.socket()
	if err := checkSocketDirectory(socket); err != nil {
		return Response{}, fmt.Errorf("connect to the baum daemon: %w", err)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || refused(err) {
			return Response{}, ErrNotRunning
		}
		return Response{}, fmt.Errorf("connect to the baum daemon: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	req := Request{Protocol: Protocol, Method: method, Client: c.Version}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return Response{}, err
		}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err = conn.Write(append(data, '\n')); err != nil {
		return Response{}, fmt.Errorf("talk to the baum daemon: %w", err)
	}
	line, err := readLine(bufio.NewReader(conn))
	if err != nil {
		return Response{}, fmt.Errorf("the baum daemon did not answer: %w", err)
	}
	var response Response
	if err = json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("the baum daemon answered with an invalid response: %w", err)
	}
	return response, nil
}

// Hello returns the running daemon's status.
func (c *Client) Hello(ctx context.Context) (Status, error) {
	response, err := c.Call(ctx, "hello", nil)
	if err != nil {
		return Status{}, err
	}
	if !response.OK || response.Status == nil {
		return Status{}, errors.New("the baum daemon refused hello")
	}
	return *response.Status, nil
}

// Status is the daemon's status, ErrNotRunning, or an error naming the pid of
// a daemon that holds the lock but does not answer.
func (c *Client) Status(ctx context.Context) (Status, error) {
	status, err := c.Hello(ctx)
	if err == nil {
		return status, nil
	}
	locked, lockErr := c.locked(ctx)
	if lockErr != nil {
		return Status{}, errors.Join(err, lockErr)
	}
	if !locked {
		return Status{}, ErrNotRunning
	}
	return Status{}, c.unresponsive(err)
}

// Stop asks the daemon to stop and waits until it has exited. A daemon that
// is still starting is asked once it answers. It reports false when no daemon
// was running.
func (c *Client) Stop(ctx context.Context) (bool, error) {
	deadline := time.Now().Add(c.readyTimeout())
	starting := false
	for {
		response, err := c.Call(ctx, "stop", nil)
		switch {
		case err == nil && !response.OK:
			return false, errors.New("the baum daemon refused to stop")
		case err == nil:
			return true, c.waitExit(ctx)
		case !errors.Is(err, ErrNotRunning):
			return false, err
		}
		// Nothing answers: none runs, or one holds the lock and is starting
		// (or stopping) and will answer or exit shortly.
		locked, lockErr := c.locked(ctx)
		if lockErr != nil {
			return false, lockErr
		}
		if !locked {
			return starting, nil
		}
		starting = true
		if !time.Now().Before(deadline) {
			return true, c.unresponsive(err)
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// waitExit waits until the daemon lock is free.
func (c *Client) waitExit(ctx context.Context) error {
	timeout := c.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		locked, err := c.locked(ctx)
		if err != nil {
			return err
		}
		if !locked {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the baum daemon did not stop within %s%s", timeout, c.pidHint())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Ensure returns a running daemon this client can use, starting one when
// none runs and replacing one that is older than this client. Concurrent
// calls start exactly one daemon. A daemon newer than this client is kept;
// one speaking a newer protocol is an IncompatibleError.
func (c *Client) Ensure(ctx context.Context) (Status, error) {
	// An explicit start ends a stop the user asked for.
	if err := ClearStopped(c.Paths); err != nil {
		return Status{}, err
	}
	status, err := c.Hello(ctx)
	if err == nil && !status.Stopping {
		if st, done, err := c.decide(ctx, status); done {
			return st, err
		}
	}
	if err = privatefs.MkdirAll(c.Paths.State); err != nil {
		return Status{}, err
	}
	// Serialize starts: whoever holds the start lock starts the daemon, and
	// the others find it running when they get the lock.
	start, err := waitLock(ctx, c.Paths.startFile(), c.readyTimeout()+5*time.Second)
	if errors.Is(err, errLocked) {
		return Status{}, errors.New("another baum command is starting the daemon and did not finish; retry")
	}
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = start.Release() }()
	for attempt := 0; attempt < 3; attempt++ {
		status, err = c.Hello(ctx)
		if err == nil {
			if status.Stopping {
				if err = c.waitExit(ctx); err != nil {
					return Status{}, err
				}
				continue
			}
			st, done, err := c.decide(ctx, status)
			if done {
				return st, err
			}
			continue
		}
		// Nobody answers. A daemon holding the lock is starting (or hung);
		// otherwise any socket file is stale and a new daemon replaces it.
		locked, lockErr := c.locked(ctx)
		if lockErr != nil {
			return Status{}, errors.Join(err, lockErr)
		}
		if locked {
			return c.waitReady(ctx, nil)
		}
		spawn := c.Spawn
		if spawn == nil {
			spawn = SpawnSelf
		}
		exited, err := spawn(c.Paths, c.Start)
		if err != nil {
			return Status{}, fmt.Errorf("start the baum daemon: %w", err)
		}
		return c.waitReady(ctx, exited)
	}
	return Status{}, errors.New("the baum daemon kept stopping while it was being started; retry")
}

// StartDetached starts a daemon without waiting for it to serve, for callers
// that must not wait, such as hooks; Ensure waits. It starts none while a
// daemon holds the daemon lock (one is starting, stopping or not answering),
// another client is starting one, or it started one less than StartBackoff
// ago that has not served yet (ErrStartBackoff); it reports whether it
// started one. The start is recorded in a file the daemon removes once it
// serves, so callers racing it see it rather than start a second one.
func (c *Client) StartDetached(ctx context.Context) (bool, error) {
	if locked, err := c.locked(ctx); err != nil || locked {
		return false, err
	}
	if err := privatefs.MkdirAll(c.Paths.State); err != nil {
		return false, err
	}
	start, err := tryLock(ctx, c.Paths.startFile())
	if errors.Is(err, errLocked) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = start.Release() }()
	if locked, err := c.locked(ctx); err != nil || locked {
		return false, err
	}
	if info, err := os.Stat(c.Paths.startedFile()); err == nil {
		// A time in the future (a clock set back) does not count.
		if age := time.Since(info.ModTime()); age >= 0 && age < StartBackoff {
			return false, ErrStartBackoff
		}
	}
	if err = writePrivate(c.Paths.startedFile(), time.Now().UTC()); err != nil {
		return false, err
	}
	spawn := c.Spawn
	if spawn == nil {
		spawn = SpawnSelf
	}
	if _, err = spawn(c.Paths, c.Start); err != nil {
		return false, fmt.Errorf("start the baum daemon: %w", err)
	}
	return true, nil
}

// decide keeps a compatible daemon (done), replaces an older one (not done,
// after stopping it) or refuses a newer protocol (done, with an error).
func (c *Client) decide(ctx context.Context, status Status) (Status, bool, error) {
	switch {
	case status.Protocol > Protocol:
		return status, true, &IncompatibleError{Daemon: status, Client: c.Version}
	case status.Protocol < Protocol:
		// It cannot serve this client at all: replace it now, interrupting
		// its jobs.
		if c.Restarted != nil {
			c.Restarted(status)
		}
		if _, err := c.Stop(ctx); err != nil {
			return Status{}, true, fmt.Errorf("stop the older baum daemon (version %s): %w", status.Version, err)
		}
		return Status{}, false, nil
	case Older(status.Version, c.Version):
		// Same protocol: keep using it, and let it restart as this version
		// once no job is queued or running, so no paid run is interrupted.
		executable := c.Executable
		if executable == "" {
			self, err := os.Executable()
			if err != nil {
				return status, true, nil
			}
			executable = self
		}
		response, err := c.Call(ctx, "stop", StopParams{WhenIdle: true, Executable: executable})
		if err != nil || !response.OK || response.Status == nil {
			return status, true, nil
		}
		if response.Status.Stopping {
			// It was idle and is stopping now; start this version.
			if c.Restarted != nil {
				c.Restarted(status)
			}
			if err = c.waitExit(ctx); err != nil {
				return Status{}, true, err
			}
			return Status{}, false, nil
		}
		// Tell the user once, when the restart is first scheduled, not on
		// every later hook run.
		if c.Deferred != nil && !status.RestartPending {
			c.Deferred(*response.Status)
		}
		return *response.Status, true, nil
	}
	return status, true, nil
}

func (c *Client) readyTimeout() time.Duration {
	if c.ReadyTimeout > 0 {
		return c.ReadyTimeout
	}
	return DefaultReadyTimeout
}

// waitReady waits until a daemon answers hello and is not stopping. exited,
// when given, is closed if the spawned process ends first.
func (c *Client) waitReady(ctx context.Context, exited <-chan struct{}) (Status, error) {
	timeout := c.readyTimeout()
	deadline := time.Now().Add(timeout)
	var last error
	for {
		status, err := c.Hello(ctx)
		if err == nil && !status.Stopping {
			if status.Protocol > Protocol {
				return status, &IncompatibleError{Daemon: status, Client: c.Version}
			}
			return status, nil
		}
		last = err
		select {
		case <-exited:
			// It may have lost the lock to another daemon that is starting.
			exited = nil
			if locked, _ := c.locked(ctx); !locked {
				if status, err := c.Hello(ctx); err == nil {
					return status, nil
				}
				return Status{}, fmt.Errorf("the baum daemon exited while starting; see %s and %s", c.Paths.LogFile(), c.Paths.OutputFile())
			}
		default:
		}
		if !time.Now().Before(deadline) {
			if last == nil {
				last = errors.New("it is stopping")
			}
			return Status{}, fmt.Errorf("the baum daemon did not become ready within %s (%v)%s; see %s", timeout, last, c.pidHint(), c.Paths.LogFile())
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Client) locked(ctx context.Context) (bool, error) {
	if _, err := os.Stat(c.Paths.LockFile()); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return held(ctx, c.Paths.LockFile())
}

func (c *Client) unresponsive(cause error) error {
	return fmt.Errorf("a baum daemon holds the lock but does not answer (%v)%s; see %s", cause, c.pidHint(), c.Paths.LogFile())
}

// pidHint names the pid from the status file, which only a running daemon
// writes.
func (c *Client) pidHint() string {
	data, err := os.ReadFile(c.Paths.statusFile())
	if err != nil {
		return ""
	}
	var status Status
	if json.Unmarshal(data, &status) != nil || status.PID <= 0 {
		return ""
	}
	return fmt.Sprintf("; its process id is %d", status.PID)
}

// Older reports whether a daemon of version daemon should be replaced by a
// client of version client: an older release, or a development build when
// the client is a release. Unequal development builds are kept; stop one with
// "baum daemon stop".
func Older(daemon, client string) bool {
	if daemon == client {
		return false
	}
	d, c := semver.IsValid(daemon), semver.IsValid(client)
	switch {
	case d && c:
		return semver.Compare(daemon, client) < 0
	case !d && c:
		return true
	}
	return false
}

// rotateOutput rotates a detached daemon's previous output file to
// OutputFile()+".1", so each start's output is kept once.
func rotateOutput(paths Paths) error {
	err := os.Rename(paths.OutputFile(), paths.OutputFile()+".1")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// home is the directory a detached daemon runs in, so it does not keep the
// starting client's directory busy.
func home() string {
	if dir, err := os.UserHomeDir(); err == nil && filepath.IsAbs(dir) {
		return dir
	}
	return string(filepath.Separator)
}

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
	DefaultReadyTimeout = 30 * time.Second
	// DefaultStopTimeout leaves a stopping daemon its shutdown timeout and
	// time to close.
	DefaultStopTimeout = DefaultShutdownTimeout + 15*time.Second
	requestTimeout     = 5 * time.Second
)

// StartOptions apply only when a client starts a daemon.
type StartOptions struct {
	Port        int
	Concurrency int
}

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
	// Restarted is told when Ensure replaces an older daemon.
	Restarted func(old Status)
}

// Call sends one request and reads its response. A missing socket or a
// refused connection is ErrNotRunning.
func (c *Client) Call(ctx context.Context, method string, params any) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.Paths.Socket)
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

// Stop asks the daemon to stop and waits until it has exited. It reports
// false when no daemon was running.
func (c *Client) Stop(ctx context.Context) (bool, error) {
	response, err := c.Call(ctx, "stop", nil)
	if errors.Is(err, ErrNotRunning) {
		locked, lockErr := c.locked(ctx)
		if lockErr != nil || !locked {
			return false, lockErr
		}
		// Starting, or stopping already: wait for it below.
	} else if err != nil {
		return false, err
	} else if !response.OK {
		return false, errors.New("the baum daemon refused to stop")
	}
	return true, c.waitExit(ctx)
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

// decide keeps a compatible daemon (done), replaces an older one (not done,
// after stopping it) or refuses a newer protocol (done, with an error).
func (c *Client) decide(ctx context.Context, status Status) (Status, bool, error) {
	switch {
	case status.Protocol > Protocol:
		return status, true, &IncompatibleError{Daemon: status, Client: c.Version}
	case status.Protocol < Protocol || Older(status.Version, c.Version):
		if c.Restarted != nil {
			c.Restarted(status)
		}
		if _, err := c.Stop(ctx); err != nil {
			return Status{}, true, fmt.Errorf("stop the older baum daemon (version %s): %w", status.Version, err)
		}
		return Status{}, false, nil
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
	if _, err := os.Stat(c.Paths.lockFile()); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return held(ctx, c.Paths.lockFile())
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

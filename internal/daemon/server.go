package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baum-ai/baum/internal/filelock"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/uiserver"
)

// Defaults of Options.
const (
	DefaultShutdownTimeout = 20 * time.Second
	// DefaultLockTimeout outlasts a predecessor's shutdown, so a successor
	// started during a restart waits for it rather than giving up.
	DefaultLockTimeout = DefaultShutdownTimeout + 15*time.Second
	// idleTimeout closes a control connection that sends nothing.
	idleTimeout = 30 * time.Second
	// methodTimeout bounds one control request's handler.
	methodTimeout = 30 * time.Second
	// restartPoll is how often a daemon asked to restart checks for idleness.
	restartPoll = time.Second
)

// ErrRunning is returned by Run when another daemon owns the lock.
var ErrRunning = errors.New("another baum daemon is already running")

// Method handles one control request kind. Its params are the request's
// Params; its result is encoded as the response's Result. An *Error it
// returns is answered with its code; any other error with CodeFailed.
type Method func(ctx context.Context, params json.RawMessage) (any, error)

// App is what the daemon serves and runs. Every field may be nil.
type App struct {
	// API serves /api/ routes on the app transport, behind its
	// authentication, Host and Origin checks.
	API http.Handler
	// Assets serves the browser application under the session namespace.
	Assets http.Handler
	// Run does background work until its context ends.
	Run func(ctx context.Context)
	// Shutdown stops work that must be recorded when it is cut short (the
	// generation queue records running jobs as interrupted). It is given
	// Options.ShutdownTimeout.
	Shutdown func(ctx context.Context) error
	// Close releases resources once nothing is served.
	Close func()
	// Drain stops accepting paid work and reports true when none is queued
	// or running, atomically with accepting work, so nothing confirmed can
	// slip in between; when work is pending it changes nothing and reports
	// false. Without it the daemon is always idle.
	Drain func() bool
	// Resume accepts paid work again after a Drain that led nowhere.
	Resume func()
	// Methods registers further control requests the application serves,
	// beside Options.Methods; they require the client's protocol to match.
	Methods map[string]Method
}

// Options configure Run.
type Options struct {
	Paths   Paths
	Version string
	// Port is the app transport's loopback port; zero picks a free one.
	Port int
	// Prepare runs once this process owns the daemon lock, before Start. The
	// command line resolves the login shell environment here.
	Prepare func(ctx context.Context) error
	// Start builds the application once this process owns the daemon lock.
	// The server is not listening yet; Publish may be called at any time.
	Start func(ctx context.Context, server *uiserver.Server) (App, error)
	// Methods registers further control requests; they require the client's
	// protocol to match.
	Methods map[string]Method
	// Respawn starts the daemon executable a newer client named, detached,
	// to take over once this daemon exits (see StopParams). Without it such
	// requests stop the daemon only when it is idle, and are refused while
	// it is not.
	Respawn func(executable string, status Status) error
	// OpenLogger opens the log once the lock is held, so a daemon that loses
	// the race never rotates the winner's log. It replaces Logger.
	OpenLogger func() (*slog.Logger, func(), error)
	// ShutdownTimeout bounds App.Shutdown; zero is DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// LockTimeout is how long to wait for a previous daemon to release the
	// lock; zero is DefaultLockTimeout.
	LockTimeout time.Duration
	Logger      *slog.Logger
}

// Run is the daemon. It returns ErrRunning when another daemon holds the
// lock, and otherwise serves until ctx ends or a client asks it to stop.
func Run(ctx context.Context, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}
	lockTimeout := opts.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = DefaultLockTimeout
	}
	paths := opts.Paths
	if err := privatefs.MkdirAll(paths.State); err != nil {
		return err
	}
	if err := privatefs.MkdirAll(filepath.Dir(paths.Socket)); err != nil {
		return err
	}
	lock, err := acquire(ctx, paths, lockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if opts.OpenLogger != nil {
		opened, closeLog, err := opts.OpenLogger()
		if err != nil {
			return err
		}
		defer closeLog()
		log = opened
	}
	// Holding the lock proves an earlier daemon's files are stale.
	for _, stale := range []string{paths.CredentialFile(), paths.statusFile(), paths.Socket} {
		if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	status := Status{Protocol: Protocol, Version: opts.Version, PID: os.Getpid(), StartedAt: time.Now().UTC(), Socket: paths.Socket, State: paths.State}
	log.Info("daemon starting", "version", status.Version, "protocol", Protocol, "pid", status.PID)
	if opts.Prepare != nil {
		if err := opts.Prepare(ctx); err != nil {
			return err
		}
	}
	var api, assets swapHandler
	server, err := uiserver.New(uiserver.Options{API: &api, App: &assets})
	if err != nil {
		return err
	}
	var app App
	if opts.Start != nil {
		if app, err = opts.Start(ctx, server); err != nil {
			return err
		}
	}
	if app.Close != nil {
		defer app.Close()
	}
	shutdownApp := func() {
		if app.Shutdown == nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := app.Shutdown(shutdownCtx); err != nil {
			log.Warn("work did not stop in time; running jobs are recorded as interrupted on the next start", "timeout", shutdownTimeout)
		}
	}
	api.set(app.API)
	assets.set(app.Assets)
	if _, err = server.Listen(opts.Port); err != nil {
		shutdownApp()
		return err
	}
	status.App = server.Origin()
	if err = writePrivate(paths.CredentialFile(), Credential{URL: server.LaunchURL()}); err != nil {
		shutdownApp()
		return err
	}
	defer func() { _ = os.Remove(paths.CredentialFile()) }()
	if err = writePrivate(paths.statusFile(), status); err != nil {
		shutdownApp()
		return err
	}
	defer func() { _ = os.Remove(paths.statusFile()) }()
	listener, err := listenControl(paths.Socket)
	if err != nil {
		shutdownApp()
		return err
	}
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	methods := map[string]Method{}
	maps.Copy(methods, opts.Methods)
	maps.Copy(methods, app.Methods)
	control := &controller{status: status, stop: stop, methods: methods, log: log, conns: map[net.Conn]struct{}{}, drain: app.Drain, resume: app.Resume, respawn: opts.Respawn}
	self.Store(control)
	defer self.Store(nil)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	control.ctx = runCtx
	var running sync.WaitGroup
	if app.Run != nil {
		running.Go(func() { app.Run(runCtx) })
	}
	httpCtx, cancelHTTP := context.WithCancel(context.Background())
	defer cancelHTTP()
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Serve(httpCtx) }()
	controlDone := make(chan struct{})
	go func() { defer close(controlDone); control.serve(listener) }()
	// It served: a client that started it may start the next one at once.
	_ = os.Remove(paths.startedFile())
	log.Info("daemon ready", "app", status.App, "socket", paths.Socket)
	running.Go(func() { control.restartWhenIdle(runCtx) })

	var serveErr error
	select {
	case <-stopCtx.Done():
	case serveErr = <-httpDone:
		log.Error("app transport failed", "error", serveErr)
	}
	control.setStopping()
	log.Info("daemon stopping")
	shutdownApp()
	cancelRun()
	running.Wait()
	cancelHTTP()
	if serveErr == nil {
		serveErr = <-httpDone
	}
	_ = listener.Close()
	<-controlDone
	log.Info("daemon stopped")
	return serveErr
}

// acquire takes the daemon lock. A daemon that answers and is not stopping
// owns it: ErrRunning at once. One that is stopping (its jobs may take up to
// its shutdown timeout) or starting is waited for, up to timeout.
func acquire(ctx context.Context, paths Paths, timeout time.Duration) (*filelock.Lock, error) {
	deadline := time.Now().Add(timeout)
	probe := &Client{Paths: paths}
	for {
		lock, err := tryLock(ctx, paths.LockFile())
		if !errors.Is(err, errLocked) {
			return lock, err
		}
		if status, err := probe.Hello(ctx); err == nil && !status.Stopping {
			return nil, ErrRunning
		}
		if !time.Now().Before(deadline) {
			return nil, ErrRunning
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// listenControl listens on the control socket, readable and writable only by
// the user. Its directory is already private; on Windows that directory's
// current-user DACL is what protects the socket.
func listenControl(path string) (net.Listener, error) {
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(path, 0o600); err != nil {
			_ = l.Close()
			return nil, err
		}
	}
	return l, nil
}

// swapHandler serves the handler set before the server starts, or 404.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) set(h http.Handler) {
	if h != nil {
		s.h.Store(&h)
	}
}
func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.h.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

type controller struct {
	ctx     context.Context
	mu      sync.Mutex
	status  Status
	stop    func()
	methods map[string]Method
	log     *slog.Logger
	conns   map[net.Conn]struct{}
	drain   func() bool
	resume  func()
	respawn func(string, Status) error
	// restartInto is the executable of the newest client that asked the
	// daemon to restart as its version, and restartVersion that version.
	restartInto, restartVersion string
	wg                          sync.WaitGroup
}

func (c *controller) setStopping() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Stopping = true
}
func (c *controller) current() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// drained stops accepting paid work if none is pending.
func (c *controller) drained() bool { return c.drain == nil || c.drain() }

// undrain accepts paid work again.
func (c *controller) undrain() {
	if c.resume != nil {
		c.resume()
	}
}

// restartWhenIdle starts the requested executable and stops once no job is
// queued or running.
func (c *controller) restartWhenIdle(ctx context.Context) {
	ticker := time.NewTicker(restartPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		executable, status := c.restartInto, c.status
		c.mu.Unlock()
		// Drain refuses new jobs in the same step that finds none pending,
		// so a job confirmed now gets a "restarting, retry" answer instead of
		// being interrupted.
		if executable == "" || !c.drained() {
			continue
		}
		c.setStopping()
		c.log.Info("idle; restarting as the newer version")
		if err := c.respawn(executable, status); err != nil {
			c.log.Error("could not start the newer version; keeping this one", "error", err)
			c.mu.Lock()
			c.restartInto, c.restartVersion, c.status.RestartPending, c.status.Stopping = "", "", false, false
			c.mu.Unlock()
			c.undrain()
			continue
		}
		c.stop()
		return
	}
}

// self is the controller of the daemon this process runs, if any.
var self atomic.Pointer[controller]

// Self returns the status of the daemon running in this process. Code that
// runs inside the daemon (its own diagnostics) uses it rather than probing the
// socket or the lock.
func Self() (Status, bool) {
	c := self.Load()
	if c == nil {
		return Status{}, false
	}
	return c.current(), true
}

func (c *controller) serve(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			break
		}
		c.mu.Lock()
		c.conns[conn] = struct{}{}
		c.mu.Unlock()
		c.wg.Go(func() {
			c.handle(conn)
			c.mu.Lock()
			delete(c.conns, conn)
			c.mu.Unlock()
		})
	}
	c.mu.Lock()
	for conn := range c.conns {
		_ = conn.Close()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *controller) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for {
		_ = conn.SetDeadline(time.Now().Add(idleTimeout))
		line, err := readLine(r)
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				_ = writeResponse(conn, Response{Error: &Error{Code: CodeBadRequest, Message: "request too long"}})
			}
			return
		}
		response, stop := c.dispatch(line)
		if err = writeResponse(conn, response); err != nil {
			return
		}
		if stop {
			c.log.Info("stop requested")
			c.stop()
		}
	}
}

func (c *controller) dispatch(line []byte) (Response, bool) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Response{Error: &Error{Code: CodeBadRequest, Message: "request is not valid JSON"}}, false
	}
	status := c.current()
	switch req.Method {
	case "hello":
		return Response{OK: true, Status: &status}, false
	case "stop":
		var params StopParams
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) != nil {
			return Response{Error: &Error{Code: CodeBadRequest, Message: "invalid stop parameters"}}, false
		}
		if params.WhenIdle && !c.drained() {
			if c.respawn == nil || !filepath.IsAbs(params.Executable) {
				return Response{OK: true, Status: &status}, false
			}
			c.mu.Lock()
			// Racing clients of different versions: the newest wins.
			if c.restartInto == "" || Older(c.restartVersion, req.Client) {
				c.restartInto, c.restartVersion = params.Executable, req.Client
				c.log.Info("restart requested once idle", "client", req.Client)
			}
			c.status.RestartPending = true
			status = c.status
			c.mu.Unlock()
			return Response{OK: true, Status: &status}, false
		}
		status.Stopping = true
		return Response{OK: true, Status: &status}, true
	}
	if req.Protocol != Protocol {
		return Response{Status: &status, Error: &Error{Code: CodeProtocolMismatch, Message: "the daemon speaks a different control protocol"}}, false
	}
	if req.Method == "ping" {
		return Response{OK: true}, false
	}
	method := c.methods[req.Method]
	if method == nil {
		return Response{Error: &Error{Code: CodeUnknownMethod, Message: "unknown method"}}, false
	}
	ctx, cancel := context.WithTimeout(c.ctx, methodTimeout)
	defer cancel()
	result, err := method(ctx, req.Params)
	if err != nil {
		// A method names its own code with an *Error.
		if coded, ok := errors.AsType[*Error](err); ok {
			return Response{Error: coded}, false
		}
		return Response{Error: &Error{Code: CodeFailed, Message: err.Error()}}, false
	}
	data, err := json.Marshal(result)
	if err != nil {
		return Response{Error: &Error{Code: CodeFailed, Message: "result could not be encoded"}}, false
	}
	return Response{OK: true, Result: data}, false
}

var errLineTooLong = errors.New("line too long")

// readLine reads one newline-terminated line of at most maxLine bytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, errLineTooLong
		}
		if err == nil {
			return bytes.TrimSpace(line), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func writeResponse(w io.Writer, response Response) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

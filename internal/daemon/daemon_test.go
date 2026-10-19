package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/uiserver"
)

// TestDaemonHelper is a daemon process started by the tests below through
// Spawn. It runs the real Run with a generation queue whose one job blocks
// until it is cancelled.
func TestDaemonHelper(t *testing.T) {
	if os.Getenv("BAUM_DAEMON_HELPER") != "1" {
		t.Skip("daemon process")
	}
	paths := Paths{State: os.Getenv("BAUM_DAEMON_STATE"), Socket: os.Getenv("BAUM_DAEMON_SOCKET")}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := Run(ctx, Options{
		Paths:   paths,
		Version: os.Getenv("BAUM_DAEMON_VERSION"),
		Prepare: func(context.Context) error {
			// Count the processes that got the lock: the daemons that served.
			f, err := os.OpenFile(filepath.Join(paths.State, "served"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(f, os.Getpid())
			return errors.Join(err, f.Close())
		},
		Start: func(ctx context.Context, _ *uiserver.Server) (App, error) {
			if os.Getenv("BAUM_DAEMON_JOB") != "1" {
				return App{}, nil
			}
			executor := &blockingExecutor{started: make(chan struct{})}
			queue, err := jobs.Open(filepath.Join(paths.State, "jobs.json"), executor, 1, nil)
			if err != nil {
				return App{}, err
			}
			estimate, err := queue.Estimate(ctx, []jobs.Item{{Clone: "clone", PR: 1}}, "single")
			if err == nil {
				_, err = queue.Submit(ctx, estimate.ID, true, false)
			}
			if err != nil {
				queue.Close()
				return App{}, err
			}
			<-executor.started
			return App{Shutdown: queue.Shutdown}, nil
		},
		ShutdownTimeout: 5 * time.Second,
	})
	if errors.Is(err, ErrRunning) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

type blockingExecutor struct {
	once    sync.Once
	started chan struct{}
}

func (b *blockingExecutor) Prepare(_ context.Context, item jobs.Item) (jobs.Prepared, string, error) {
	price := 1.0
	return jobs.Prepared{Item: item, Repository: "acme/demo", HeadSHA: "head", Backend: "claude", Model: "m", EstimateUSD: &price}, "v1", nil
}
func (b *blockingExecutor) Execute(ctx context.Context, _ jobs.Prepared, _ func(jobs.Progress)) (*float64, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// testPaths are short, private paths: Unix socket paths are limited to about
// 100 bytes, which t.TempDir() can exceed.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "bd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "r", "d.sock")}
}

type helper struct {
	paths  Paths
	spawns atomic.Int32
	env    []string
}

func newHelper(t *testing.T, env ...string) *helper {
	t.Helper()
	h := &helper{paths: testPaths(t), env: env}
	t.Cleanup(func() {
		c := h.client("cleanup")
		if _, err := c.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return h
}

func (h *helper) client(version string, env ...string) *Client {
	return &Client{Paths: h.paths, Version: version, ReadyTimeout: 20 * time.Second, StopTimeout: 20 * time.Second, Spawn: func(paths Paths, _ StartOptions) (<-chan struct{}, error) {
		h.spawns.Add(1)
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(self, "-test.run=^TestDaemonHelper$")
		cmd.Env = append(append(append(os.Environ(), "BAUM_DAEMON_HELPER=1", "BAUM_DAEMON_STATE="+paths.State, "BAUM_DAEMON_SOCKET="+paths.Socket, "BAUM_DAEMON_VERSION="+version), h.env...), env...)
		return Spawn(paths, cmd)
	}}
}

func (h *helper) served(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.paths.State, "served"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestEnsureStartsReusesAndStops(t *testing.T) {
	h := newHelper(t)
	c := h.client("v1.0.0")
	ctx := context.Background()
	if _, err := c.Status(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("status before start: %v", err)
	}
	first, err := c.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.PID == os.Getpid() || first.PID <= 0 || first.Version != "v1.0.0" || first.Protocol != Protocol || first.State != h.paths.State || !strings.HasPrefix(first.App, "http://127.0.0.1:") {
		t.Fatalf("status %+v", first)
	}
	second, err := c.Ensure(ctx)
	if err != nil || second.PID != first.PID || h.spawns.Load() != 1 {
		t.Fatalf("second ensure %+v %v, %d spawns", second, err, h.spawns.Load())
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{h.paths.State: 0o700, filepath.Dir(h.paths.Socket): 0o700, h.paths.Socket: 0o600, h.paths.CredentialFile(): 0o600, h.paths.statusFile(): 0o600, h.paths.OutputFile(): 0o600} {
			if got := mode(t, path); got != want {
				t.Errorf("%s mode %o, want %o", path, got, want)
			}
		}
	}
	credential, err := ReadCredential(h.paths)
	if err != nil || credential.Origin() != first.App {
		t.Fatalf("credential %v %v", credential, err)
	}
	stopped, err := c.Stop(ctx)
	if err != nil || !stopped {
		t.Fatal(stopped, err)
	}
	if _, err = c.Status(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("status after stop: %v", err)
	}
	for _, gone := range []string{h.paths.Socket, h.paths.CredentialFile(), h.paths.statusFile()} {
		if _, err = os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", gone, err)
		}
	}
	if stopped, err = c.Stop(ctx); err != nil || stopped {
		t.Fatal("second stop", stopped, err)
	}
}

func TestConcurrentEnsureStartsExactlyOneDaemon(t *testing.T) {
	h := newHelper(t)
	var wg sync.WaitGroup
	pids := make([]int, 8)
	errs := make([]error, 8)
	for i := range pids {
		wg.Go(func() {
			status, err := h.client("v1.0.0").Ensure(context.Background())
			pids[i], errs[i] = status.PID, err
		})
	}
	wg.Wait()
	for i := range pids {
		if errs[i] != nil || pids[i] != pids[0] {
			t.Fatalf("ensure %d: pid %d (first %d), %v", i, pids[i], pids[0], errs[i])
		}
	}
	if served := h.served(t); len(served) != 1 || served[0] != strconv.Itoa(pids[0]) {
		t.Fatalf("daemons that served: %v", served)
	}
	if n := h.spawns.Load(); n != 1 {
		t.Fatalf("%d daemons spawned", n)
	}
}

func TestEnsureReplacesStaleFiles(t *testing.T) {
	h := newHelper(t)
	for _, dir := range []string{h.paths.State, filepath.Dir(h.paths.Socket)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A crashed daemon leaves its socket, credential and status file, but the
	// operating system has released its lock.
	stale, err := net.Listen("unix", h.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	oldCredential := Credential{URL: "http://127.0.0.1:1/#token=" + strings.Repeat("a", 43)}
	if err = writePrivate(h.paths.CredentialFile(), oldCredential); err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(h.paths.statusFile(), Status{PID: 999999}); err != nil {
		t.Fatal(err)
	}
	status, err := h.client("v1.0.0").Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	credential, err := ReadCredential(h.paths)
	if err != nil || credential == oldCredential || credential.Origin() != status.App {
		t.Fatalf("credential not replaced: %v %v", credential, err)
	}
}

func TestEnsureReportsDaemonThatHoldsTheLockButDoesNotAnswer(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := tryLock(context.Background(), paths.lockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if err = writePrivate(paths.statusFile(), Status{PID: 4242}); err != nil {
		t.Fatal(err)
	}
	c := &Client{Paths: paths, Version: "v1.0.0", ReadyTimeout: 300 * time.Millisecond, Spawn: func(Paths, StartOptions) (<-chan struct{}, error) {
		t.Error("spawned while another process holds the lock")
		return nil, errors.New("no")
	}}
	if _, err = c.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "did not become ready") || !strings.Contains(err.Error(), "4242") {
		t.Fatal(err)
	}
	if _, err = c.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "does not answer") || !strings.Contains(err.Error(), "4242") {
		t.Fatal(err)
	}
}

func TestOlderDaemonIsRestartedAndNewerKept(t *testing.T) {
	h := newHelper(t)
	ctx := context.Background()
	old, err := h.client("v0.1.0").Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upgraded := h.client("v0.2.0")
	var replaced []Status
	upgraded.Restarted = func(s Status) { replaced = append(replaced, s) }
	current, err := upgraded.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.PID == old.PID || current.Version != "v0.2.0" || len(replaced) != 1 || replaced[0].PID != old.PID {
		t.Fatalf("old %+v, current %+v, replaced %+v", old, current, replaced)
	}
	kept, err := h.client("v0.1.5").Ensure(ctx)
	if err != nil || kept.PID != current.PID {
		t.Fatalf("an older client replaced a newer daemon: %+v %v", kept, err)
	}
}

// fakeControl answers every request on paths.Socket with respond.
func fakeControl(t *testing.T, paths Paths, respond func(Request) Response) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(paths.Socket), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				for {
					line, err := readLine(r)
					if err != nil {
						return
					}
					var req Request
					_ = json.Unmarshal(line, &req)
					_ = writeResponse(conn, respond(req))
				}
			}()
		}
	}()
}

func TestNewerProtocolIsRefusedClearly(t *testing.T) {
	paths := testPaths(t)
	newer := Status{Protocol: Protocol + 1, Version: "v9.0.0", PID: 77}
	fakeControl(t, paths, func(Request) Response { return Response{OK: true, Status: &newer} })
	c := &Client{Paths: paths, Version: "v1.0.0", Spawn: func(Paths, StartOptions) (<-chan struct{}, error) {
		t.Error("spawned beside a newer daemon")
		return nil, errors.New("no")
	}}
	_, err := c.Ensure(context.Background())
	var incompatible *IncompatibleError
	if !errors.As(err, &incompatible) || !strings.Contains(err.Error(), fmt.Sprintf("protocol %d", Protocol+1)) || !strings.Contains(err.Error(), "baum daemon stop") {
		t.Fatal(err)
	}
}

// runInProcess runs a daemon in this process until the test ends.
func runInProcess(t *testing.T, paths Paths, opts Options) (stop func()) {
	t.Helper()
	opts.Paths = paths
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	c := &Client{Paths: paths, ReadyTimeout: 10 * time.Second}
	if _, err := c.waitReady(ctx, nil); err != nil {
		cancel()
		t.Fatal(err, <-done)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func rawCall(t *testing.T, paths Paths, line string) Response {
	t.Helper()
	conn, err := net.Dial("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := readLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err = json.Unmarshal(reply, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestControlProtocol(t *testing.T) {
	paths := testPaths(t)
	runInProcess(t, paths, Options{Version: "v1.2.3", Methods: map[string]Method{
		"echo": func(_ context.Context, params json.RawMessage) (any, error) { return params, nil },
	}})
	hello := rawCall(t, paths, `{"protocol":0,"method":"hello"}`)
	if !hello.OK || hello.Status == nil || hello.Status.Version != "v1.2.3" || hello.Status.Protocol != Protocol {
		t.Fatalf("hello from an old client: %+v", hello)
	}
	mismatch := rawCall(t, paths, `{"protocol":99,"method":"ping"}`)
	if mismatch.OK || mismatch.Error == nil || mismatch.Error.Code != CodeProtocolMismatch || mismatch.Status == nil {
		t.Fatalf("mismatch %+v", mismatch)
	}
	if r := rawCall(t, paths, fmt.Sprintf(`{"protocol":%d,"method":"ping"}`, Protocol)); !r.OK {
		t.Fatalf("ping %+v", r)
	}
	if r := rawCall(t, paths, fmt.Sprintf(`{"protocol":%d,"method":"echo","params":{"a":1}}`, Protocol)); !r.OK || string(r.Result) != `{"a":1}` {
		t.Fatalf("echo %+v", r)
	}
	if r := rawCall(t, paths, fmt.Sprintf(`{"protocol":%d,"method":"ingest"}`, Protocol)); r.Error == nil || r.Error.Code != CodeUnknownMethod {
		t.Fatalf("unknown %+v", r)
	}
	if r := rawCall(t, paths, "not json"); r.Error == nil || r.Error.Code != CodeBadRequest {
		t.Fatalf("bad %+v", r)
	}
	if r := rawCall(t, paths, `{"method":"`+strings.Repeat("x", maxLine)+`"}`); r.Error == nil || r.Error.Code != CodeBadRequest {
		t.Fatalf("long %+v", r)
	}
}

func TestSecondRunReportsRunning(t *testing.T) {
	paths := testPaths(t)
	runInProcess(t, paths, Options{})
	err := Run(context.Background(), Options{Paths: paths, LockTimeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrRunning) {
		t.Fatal(err)
	}
}

// exchange posts the credential's token to the bootstrap exchange as the
// app's webview does.
func exchange(t *testing.T, credential Credential) int {
	t.Helper()
	req, err := http.NewRequest("POST", credential.Origin()+"/api/auth/exchange", strings.NewReader(fmt.Sprintf(`{"token":%q}`, credential.Token())))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", credential.Origin())
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

func TestCredentialAuthenticatesRepeatedlyUntilTheDaemonRestarts(t *testing.T) {
	paths := testPaths(t)
	stop := runInProcess(t, paths, Options{})
	first, err := ReadCredential(paths)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && mode(t, paths.CredentialFile()) != 0o600 {
		t.Fatal("credential is not private")
	}
	// The app is relaunched many times against one daemon.
	for range 3 {
		if code := exchange(t, first); code != http.StatusOK {
			t.Fatalf("exchange: %d", code)
		}
	}
	req, _ := http.NewRequest("GET", first.Origin()+"/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+first.Token())
	response, err := http.DefaultClient.Do(req)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	_ = response.Body.Close()
	port, err := strconv.Atoi(first.Origin()[strings.LastIndex(first.Origin(), ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err = ReadCredential(paths); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("credential left after stop: %v", err)
	}
	// A new daemon on the same port has a new credential; the old one fails.
	runInProcess(t, paths, Options{Port: port})
	second, err := ReadCredential(paths)
	if err != nil || second == first || second.Origin() != first.Origin() {
		t.Fatalf("second credential %v %v", second, err)
	}
	if code := exchange(t, first); code != http.StatusUnauthorized {
		t.Fatalf("stale credential: %d", code)
	}
	if code := exchange(t, second); code != http.StatusOK {
		t.Fatalf("new credential: %d", code)
	}
}

func TestStopRecordsRunningJobInterrupted(t *testing.T) {
	h := newHelper(t, "BAUM_DAEMON_JOB=1")
	c := h.client("v1.0.0")
	if _, err := c.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(h.paths.State, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal []jobs.Job
	if err = json.Unmarshal(raw, &journal); err != nil || len(journal) != 1 || journal[0].State != "running" {
		t.Fatalf("before stop: %s %v", raw, err)
	}
	if stopped, err := c.Stop(context.Background()); err != nil || !stopped {
		t.Fatal(stopped, err)
	}
	if raw, err = os.ReadFile(filepath.Join(h.paths.State, "jobs.json")); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &journal); err != nil || journal[0].State != "interrupted" || journal[0].Reason != "interrupted" {
		t.Fatalf("after stop: %s %v", raw, err)
	}
}

func TestReadCredentialRefusesUnsafeFiles(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredential(paths); !errors.Is(err, ErrNoCredential) {
		t.Fatal(err)
	}
	for _, url := range []string{"http://example.com:1/#token=" + strings.Repeat("a", 43), "http://127.0.0.1:1/#token=short", "https://127.0.0.1:1/#token=" + strings.Repeat("a", 43)} {
		if err := writePrivate(paths.CredentialFile(), Credential{URL: url}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredential(paths); err == nil {
			t.Errorf("accepted %s", url)
		}
	}
	if runtime.GOOS != "windows" {
		if err := writePrivate(paths.CredentialFile(), Credential{URL: "http://127.0.0.1:1/#token=" + strings.Repeat("a", 43)}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(paths.CredentialFile(), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadCredential(paths); err == nil {
			t.Error("accepted a readable credential")
		}
	}
}

func TestOlder(t *testing.T) {
	for _, tc := range []struct {
		daemon, client string
		want           bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.2.0", "v0.1.0", false},
		{"v0.2.0", "v0.2.0", false},
		{"devel", "v0.2.0", true},
		{"v0.2.0", "devel", false},
		{"devel", "devel", false},
	} {
		if got := Older(tc.daemon, tc.client); got != tc.want {
			t.Errorf("Older(%s, %s) = %v", tc.daemon, tc.client, got)
		}
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	p, err := DefaultPaths()
	if err != nil || p.State != filepath.FromSlash("/state/baum/daemon") || p.Socket != filepath.FromSlash("/run/user/7/baum/daemon.sock") {
		t.Fatal(p, err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if p, err = DefaultPaths(); err != nil || p.Socket != filepath.Join(p.State, "daemon.sock") {
		t.Fatal(p, err)
	}
	if runtime.GOOS != "windows" {
		t.Setenv("XDG_STATE_HOME", "/"+strings.Repeat("long/", 30))
		if p, err = DefaultPaths(); err != nil || p.Socket != filepath.Join(os.TempDir(), "baum-"+strconv.Itoa(os.Getuid()), "daemon.sock") {
			t.Fatal(p, err)
		}
	}
}

func TestRotatingFileIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	log, err := OpenLog(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err = fmt.Fprintf(log, "line %02d %s\n", i, strings.Repeat("x", 30)); err != nil {
			t.Fatal(err)
		}
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil || info.Size() > 100 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	if _, err = os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("kept a fourth file")
	}
	last, _ := os.ReadFile(path)
	if !strings.Contains(string(last), "line 19") {
		t.Fatalf("latest line not in the current file: %q", last)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/shellenv"
)

// stubDaemon makes the daemon commands use short private paths and start the
// daemon in this process, serving the default status page.
func stubDaemon(t *testing.T) daemon.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "bc")
	if err != nil {
		t.Fatal(err)
	}
	paths := daemon.Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "d.sock")}
	oldPaths, oldSpawn := daemonPaths, daemonSpawn
	daemonPaths = func() (daemon.Paths, error) { return paths, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := false
	daemonSpawn = func(p daemon.Paths, start daemon.StartOptions) (<-chan struct{}, error) {
		started = true
		exited := make(chan struct{})
		go func() {
			defer close(exited)
			defer close(done)
			_ = daemon.Run(ctx, daemon.Options{Paths: p, Version: version(), Port: start.Port})
		}()
		return exited, nil
	}
	t.Cleanup(func() {
		cancel()
		if started {
			<-done
		}
		daemonPaths, daemonSpawn = oldPaths, oldSpawn
		_ = os.RemoveAll(dir)
	})
	return paths
}

func TestDaemonCommands(t *testing.T) {
	paths := stubDaemon(t)
	if _, _, err := run(t, "daemon", "status"); !errors.Is(err, daemon.ErrNotRunning) {
		t.Fatalf("status before start: %v", err)
	}
	out, _, err := run(t, "daemon", "ensure", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("ensure --json printed %q", out)
	}
	var status map[string]any
	if err = json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"protocol", "version", "pid", "started_at", "app", "state"} {
		if _, ok := status[field]; !ok {
			t.Errorf("status lacks %s: %s", field, out)
		}
	}
	if status["state"] != paths.State {
		t.Errorf("state %v", status["state"])
	}
	out, _, err = run(t, "daemon", "status")
	if err != nil || !strings.Contains(out, "is running as process") || !strings.Contains(out, paths.State) {
		t.Fatal(out, err)
	}
	out, _, err = run(t, "daemon", "stop")
	if err != nil || !strings.Contains(out, "Stopped") {
		t.Fatal(out, err)
	}
	// Hooks leave a daemon the user stopped alone until an explicit start.
	if !daemon.StoppedByUser(paths) {
		t.Error("stop did not mark the daemon stopped by the user")
	}
	out, _, err = run(t, "daemon", "stop")
	if err != nil || !strings.Contains(out, "not running") {
		t.Fatal(out, err)
	}
	// Ensure, an explicit start, clears it even when the start fails.
	client := &daemon.Client{Paths: paths, Spawn: func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		return nil, errors.New("test: no daemon")
	}}
	if _, err = client.Ensure(t.Context()); err == nil {
		t.Fatal("ensure succeeded without a daemon")
	}
	if daemon.StoppedByUser(paths) {
		t.Error("ensure left the stopped mark")
	}
}

// A record that cannot be opened leaves the daemon running without it: hooks
// get unknown_method and spool, rather than start a daemon on every call.
func TestDaemonRunsWithoutAHookEventRecordItCannotOpen(t *testing.T) {
	dir, err := os.MkdirTemp("", "bce")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{State: dir, Socket: filepath.Join(dir, "d.sock")}
	// A file where the record's directory belongs cannot be opened on any OS.
	if err = os.WriteFile(filepath.Join(dir, "events"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	events := openHookEvents(paths, slog.New(slog.NewTextHandler(&log, nil)))
	if events.service != nil || events.Methods() != nil {
		t.Fatal("a record with a blocked directory was opened")
	}
	events.Run(t.Context())
	events.Close()
	if !strings.Contains(log.String(), "hook events are not recorded") {
		t.Errorf("log = %s", log.String())
	}
}

func TestDaemonReportDescribesTheRunningDaemon(t *testing.T) {
	paths := stubDaemon(t)
	if _, _, err := run(t, "daemon", "ensure"); err != nil {
		t.Fatal(err)
	}
	report := daemonReport(context.Background())
	if !report.Running || report.State != paths.State || report.PID != os.Getpid() || report.Older || report.Incompatible {
		t.Fatalf("doctor report %+v", report)
	}
}

func TestDoctorReportsStoppedDaemonWithoutCreatingState(t *testing.T) {
	paths := stubDaemon(t)
	report := daemonReport(context.Background())
	if report.Running || report.Err != nil || report.State != paths.State {
		t.Fatalf("%+v", report)
	}
	if _, err := os.Stat(paths.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("doctor created the daemon state directory")
	}
}

func TestDaemonFlagsAreValidated(t *testing.T) {
	stubDaemon(t)
	for _, args := range [][]string{{"daemon", "ensure", "--port", "70000"}, {"daemon", "run", "--concurrency", "0"}, {"daemon", "ensure", "--concurrency", "17"}} {
		if _, _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestApplyLoginEnvironment(t *testing.T) {
	saved := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, entry := range saved {
			name, value, _ := strings.Cut(entry, "=")
			_ = os.Setenv(name, value)
		}
	})
	old := resolveShell
	t.Cleanup(func() { resolveShell = old })
	for _, tc := range []struct {
		name         string
		resolve      func(context.Context, shellenv.Options) (shellenv.Result, error)
		inherit      []string
		want         map[string]string
		wantUnixPath string
		dropped      []string
	}{
		{
			name: "login shell wins",
			resolve: func(context.Context, shellenv.Options) (shellenv.Result, error) {
				return shellenv.Result{Shell: "/bin/zsh", Env: map[string]string{"PATH": "/opt/bin:/usr/bin", "GITHUB_TOKEN": "tok-from-shell", "HOME": "/elsewhere"}}, nil
			},
			inherit: []string{"PATH=/usr/bin", "GITHUB_TOKEN=tok-from-agent", "HOME=/home/user", "CLAUDE_CODE_ENTRYPOINT=cli"},
			want:    map[string]string{"PATH": "/opt/bin:/usr/bin", "GITHUB_TOKEN": "tok-from-shell", "HOME": "/home/user"},
			dropped: []string{"CLAUDE_CODE_ENTRYPOINT"},
		},
		{
			// Launched from a desktop launcher without XDG_STATE_HOME while the
			// shell sets one: the daemon locked the default state directory, so
			// its job journal must stay beside it.
			name: "locations never come from the shell",
			resolve: func(context.Context, shellenv.Options) (shellenv.Result, error) {
				return shellenv.Result{Shell: "/bin/zsh", Env: map[string]string{"PATH": "/opt/bin", "XDG_STATE_HOME": "/elsewhere/state", "XDG_RUNTIME_DIR": "/run/elsewhere"}}, nil
			},
			inherit:      []string{"PATH=/usr/bin", "HOME=/home/user"},
			want:         map[string]string{"PATH": "/opt/bin", "HOME": "/home/user"},
			wantUnixPath: "/opt/bin:/usr/bin",
			dropped:      []string{"XDG_STATE_HOME", "XDG_RUNTIME_DIR"},
		},
		{
			// Windows: shellenv.Resolve returns the inherited environment
			// with the registry's PATH, and no shell.
			name: "registry PATH",
			resolve: func(_ context.Context, opts shellenv.Options) (shellenv.Result, error) {
				env := map[string]string{}
				for _, entry := range opts.Environ {
					name, value, _ := strings.Cut(entry, "=")
					env[name] = value
				}
				env["PATH"] = `/registry/bin`
				return shellenv.Result{Env: env}, nil
			},
			inherit:      []string{"PATH=/usr/bin", "GITHUB_TOKEN=tok-from-agent", "HOME=/home/user"},
			want:         map[string]string{"PATH": "/registry/bin", "GITHUB_TOKEN": "tok-from-agent", "HOME": "/home/user"},
			wantUnixPath: "/registry/bin:/usr/bin",
		},
		{
			name: "failing shell falls back",
			resolve: func(context.Context, shellenv.Options) (shellenv.Result, error) {
				return shellenv.Result{Shell: "/bin/zsh"}, errors.New("login shell /bin/zsh did not finish within 10s")
			},
			inherit: []string{"PATH=/usr/bin", "GITHUB_TOKEN=tok-from-agent", "SECRET_THING=x"},
			want:    map[string]string{"PATH": "/usr/bin", "GITHUB_TOKEN": "tok-from-agent"},
			dropped: []string{"SECRET_THING"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolveShell = tc.resolve
			os.Clearenv()
			for _, entry := range tc.inherit {
				name, value, _ := strings.Cut(entry, "=")
				_ = os.Setenv(name, value)
			}
			var logged strings.Builder
			applyLoginEnvironment(context.Background(), slog.New(slog.NewTextHandler(&logged, nil)))
			for name, value := range tc.want {
				if name == "PATH" && tc.wantUnixPath != "" && runtime.GOOS != "windows" {
					value = tc.wantUnixPath
				}
				if got := os.Getenv(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			for _, name := range tc.dropped {
				if _, ok := os.LookupEnv(name); ok {
					t.Errorf("%s kept", name)
				}
			}
			// Names are logged, values never.
			if strings.Contains(logged.String(), "tok-from-") {
				t.Errorf("value logged: %s", logged.String())
			}
			if !strings.Contains(logged.String(), "PATH") {
				t.Errorf("names not logged: %s", logged.String())
			}
			if token, ok := tc.want["GITHUB_TOKEN"]; ok {
				if !strings.Contains(logged.String(), "GITHUB_TOKEN") {
					t.Errorf("GITHUB_TOKEN not named: %s", logged.String())
				}
				if strings.Contains(logged.String(), token) {
					t.Errorf("GITHUB_TOKEN's value logged: %s", logged.String())
				}
			}
		})
	}
}

// Stopping a daemon that never ran leaves no trace a hook would read as one
// that ran.
func TestStopWithoutAStateDirectoryCreatesNone(t *testing.T) {
	paths := stubDaemon(t)
	if out, _, err := run(t, "daemon", "stop"); err != nil || !strings.Contains(out, "not running") {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(paths.State); !os.IsNotExist(err) {
		t.Errorf("stop created the state directory: %v", err)
	}
	if daemon.HasRun(paths) {
		t.Error("stop made the daemon look as if it ran")
	}
}

func TestDaemonDiagnosticsNeverFetchPrices(t *testing.T) {
	doctorMachine(t)
	old := doctorOptions
	t.Cleanup(func() { doctorOptions = old })
	var fetch []bool
	doctorOptions = func(f bool) doctor.Options {
		fetch = append(fetch, f)
		opts := old(f)
		opts.RefreshPrices = func(context.Context) error { t.Error("price list fetched"); return nil }
		return opts
	}
	report := uiDiagnostics(context.Background())
	if len(report.Checks) == 0 || len(fetch) != 1 || fetch[0] {
		t.Fatalf("checks %d, fetch flags %v", len(report.Checks), fetch)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/daemon"
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
	oldPaths, oldSpawn, oldOpen := daemonPaths, daemonSpawn, openBrowser
	daemonPaths = func() (daemon.Paths, error) { return paths, nil }
	var opened []string
	openBrowser = func(url string) error { opened = append(opened, url); return nil }
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
		daemonPaths, daemonSpawn, openBrowser = oldPaths, oldSpawn, oldOpen
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
	out, _, err = run(t, "daemon", "stop")
	if err != nil || !strings.Contains(out, "not running") {
		t.Fatal(out, err)
	}
}

func TestUIOpensTheDaemonsLaunchURL(t *testing.T) {
	paths := stubDaemon(t)
	out, errOut, err := run(t, "ui", "--no-open")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := daemon.ReadCredential(paths)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != credential.URL || !strings.Contains(errOut, "baum daemon stop") {
		t.Fatalf("stdout %q, stderr %q", out, errOut)
	}
	// A second invocation reuses the running daemon and its credential.
	again, _, err := run(t, "ui", "--no-open")
	if err != nil || again != out {
		t.Fatalf("second ui %q %v", again, err)
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
	for _, args := range [][]string{{"daemon", "ensure", "--port", "70000"}, {"daemon", "run", "--concurrency", "0"}, {"ui", "--concurrency", "17"}} {
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
		name    string
		resolve func(context.Context, shellenv.Options) (shellenv.Result, error)
		inherit []string
		want    map[string]string
		dropped []string
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
			inherit: []string{"PATH=/usr/bin", "HOME=/home/user"},
			want:    map[string]string{"PATH": "/opt/bin", "HOME": "/home/user"},
			dropped: []string{"XDG_STATE_HOME", "XDG_RUNTIME_DIR"},
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

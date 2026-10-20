package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/uiserver"
)

// busyDaemon runs a daemon of version v0.1.0 in this process, busy until idle
// is called, that sends what it restarts into to respawned instead of
// starting it.
func busyDaemon(t *testing.T, trust func(string) (string, error)) (paths Paths, idle func(), respawned <-chan string) {
	t.Helper()
	paths = testPaths(t)
	var busy atomic.Bool
	busy.Store(true)
	ch := make(chan string, 4)
	runInProcess(t, paths, Options{
		Version: "v0.1.0",
		Start: func(context.Context, *uiserver.Server) (App, error) {
			return App{Drain: func() bool { return !busy.Load() }}, nil
		},
		Respawn: func(executable string, _ Status) error { ch <- executable; return nil },
		trust:   trust,
	})
	return paths, func() { busy.Store(false) }, ch
}

func askRestart(t *testing.T, paths Paths, client, executable string) Status {
	t.Helper()
	r := rawCall(t, paths, fmt.Sprintf(`{"protocol":%d,"method":"stop","client":%q,"params":{"when_idle":true,"executable":%q}}`, Protocol, client, executable))
	if !r.OK || r.Status == nil || r.Status.Stopping {
		t.Fatalf("%+v", r)
	}
	return *r.Status
}

// keptRunning checks, after the daemon had time to restart, that it did not.
func keptRunning(t *testing.T, paths Paths, respawned <-chan string) {
	t.Helper()
	select {
	case exe := <-respawned:
		t.Fatalf("restarted as %s", exe)
	case <-time.After(3 * restartPoll):
	}
	hello := rawCall(t, paths, `{"protocol":1,"method":"hello"}`)
	if !hello.OK || hello.Status == nil || hello.Status.Stopping || hello.Status.RestartPending || hello.Status.PID != os.Getpid() {
		t.Fatalf("%+v", hello)
	}
}

// executableIn writes an executable file at rel under dir.
func executableIn(t *testing.T, dir, rel string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// safeDir is a private temporary directory with its symbolic links resolved.
func safeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func noExecutable() (string, error) { return "", errors.New("none") }

// policyFor allows exactly allowed, as the baum on the PATH, and checks the
// directories above it up to dir.
func policyFor(dir, allowed string) executablePolicy {
	return executablePolicy{self: noExecutable, lookPath: func(string) (string, error) { return allowed, nil }, extra: []string{}, top: dir}
}

func TestRestartExecutableOnlyFromANewerClient(t *testing.T) {
	var checked atomic.Int32
	paths, idle, respawned := busyDaemon(t, func(path string) (string, error) { checked.Add(1); return path, nil })
	for _, client := range []string{"v0.1.0", "v0.0.9", "", "dev"} {
		if status := askRestart(t, paths, client, "/opt/baum"); status.RestartPending {
			t.Fatalf("client %q scheduled a restart", client)
		}
	}
	if checked.Load() != 0 {
		t.Fatal("checked an executable an older client named")
	}
	idle()
	keptRunning(t, paths, respawned)
}

func TestRestartRefusesAnExecutableOutsideTheAllowlist(t *testing.T) {
	// The default policy: this daemon's executable is the test binary, and
	// the file is not the baum on the PATH.
	paths, idle, respawned := busyDaemon(t, nil)
	planted := executableIn(t, safeDir(t), "workspace/baum")
	if status := askRestart(t, paths, "v0.2.0", planted); status.RestartPending {
		t.Fatal("scheduled a restart into a planted file")
	}
	idle()
	keptRunning(t, paths, respawned)
}

func TestRestartIntoAnAllowedExecutable(t *testing.T) {
	dir := safeDir(t)
	baum := executableIn(t, dir, "bin/baum")
	link := filepath.Join(dir, "baum-link")
	if err := os.Symlink(baum, link); err != nil {
		t.Fatal(err)
	}
	paths, idle, respawned := busyDaemon(t, policyFor(dir, baum).trusted)
	if status := askRestart(t, paths, "v0.2.0", link); !status.RestartPending {
		t.Fatal("refused the baum on the PATH")
	}
	idle()
	select {
	case exe := <-respawned:
		if exe != baum {
			t.Fatalf("restarted as %s, not the resolved %s", exe, baum)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never restarted")
	}
}

func TestExecutablePolicyAllowlist(t *testing.T) {
	dir := safeDir(t)
	baum := executableIn(t, dir, "bin/baum")
	self := executableIn(t, dir, "app/baum")
	extra := executableIn(t, dir, "bundle/baum")
	other := executableIn(t, dir, "workspace/baum")
	toOther := filepath.Join(dir, "bin", "to-other")
	toBaum := filepath.Join(dir, "workspace", "to-baum")
	for link, target := range map[string]string{toOther: other, toBaum: baum} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	policy := executablePolicy{
		self:     func() (string, error) { return self, nil },
		lookPath: func(string) (string, error) { return toBaum, nil },
		extra:    []string{extra},
		top:      dir,
	}
	for _, allowed := range []struct{ path, want string }{{baum, baum}, {toBaum, baum}, {self, self}, {extra, extra}} {
		if got, err := policy.trusted(allowed.path); err != nil || got != allowed.want {
			t.Errorf("%s: %q %v", allowed.path, got, err)
		}
	}
	for _, refused := range []string{other, toOther, "bin/baum", filepath.Join(dir, "bin"), filepath.Join(dir, "missing")} {
		if got, err := policy.trusted(refused); err == nil {
			t.Errorf("%s allowed as %s", refused, got)
		}
	}
	if _, err := policy.trusted(other); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Errorf("reason %v", err)
	}
}

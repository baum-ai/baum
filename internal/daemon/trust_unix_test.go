//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutablePolicyRefusesFilesOthersCanReplace(t *testing.T) {
	for _, c := range []struct {
		name   string
		chmod  string // relative to the temporary directory
		mode   os.FileMode
		reason string
	}{
		{"group-writable file", "d/baum", 0o775, "writable"},
		{"world-writable file", "d/baum", 0o757, "writable"},
		{"not executable", "d/baum", 0o644, "not executable"},
		{"group-writable directory", "d", 0o775, "writable"},
		{"world-writable directory", "d", 0o757, "writable"},
		{"sticky world-writable directory", "d", 0o777 | os.ModeSticky, "writable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := safeDir(t)
			baum := executableIn(t, dir, "d/baum")
			policy := policyFor(dir, baum)
			if _, err := policy.trusted(baum); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, c.chmod), c.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := policy.trusted(baum); err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("allowed, or not because %s: %v", c.reason, err)
			}
		})
	}
}

// The executable is checked again just before it runs: one replaced by a
// file others can write after the restart was scheduled is refused, and the
// daemon keeps running.
func TestRestartRechecksTheExecutableBeforeRunningIt(t *testing.T) {
	dir := safeDir(t)
	baum := executableIn(t, dir, "bin/baum")
	paths, idle, respawned := busyDaemon(t, policyFor(dir, baum).trusted)
	if status := askRestart(t, paths, "v0.2.0", baum); !status.RestartPending {
		t.Fatal("refused the baum on the PATH")
	}
	if err := os.Chmod(baum, 0o777); err != nil {
		t.Fatal(err)
	}
	idle()
	for deadline := time.Now().Add(10 * time.Second); ; {
		hello := rawCall(t, paths, `{"protocol":1,"method":"hello"}`)
		if hello.Status != nil && !hello.Status.RestartPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart still pending: %+v", hello)
		}
		time.Sleep(50 * time.Millisecond)
	}
	keptRunning(t, paths, respawned)
}

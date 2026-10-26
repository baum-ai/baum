package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/uiserver"
)

// replaceFile swaps path for a new file, as an update or rebuild does: the
// running executable's file is unlinked, never written in place.
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	next := path + ".next"
	if err := os.WriteFile(next, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

func TestExecutableWatchReportsAStableReplacementOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baum")
	if err := os.WriteFile(path, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := newExecutableWatch(path, time.Second)
	now := time.Unix(1000, 0)
	tick := func() bool { now = now.Add(time.Second); return w.replaced(now) }
	if first, second := tick(), tick(); first || second {
		t.Fatal("reported the running file")
	}
	replaceFile(t, path, "new")
	if tick() {
		t.Fatal("reported a replacement before it settled")
	}
	if !tick() {
		t.Fatal("missed a settled replacement")
	}
	if first, second := tick(), tick(); first || second {
		t.Fatal("reported one replacement twice")
	}
	replaceFile(t, path, "newer")
	if tick() || !tick() {
		t.Fatal("missed a second replacement")
	}
	if w.replaced(now) {
		t.Fatal("checked again within the interval")
	}
}

func TestDaemonRestartsFromItsReplacedExecutableOnceIdle(t *testing.T) {
	paths := testPaths(t)
	executable := filepath.Join(t.TempDir(), "baum")
	if err := os.WriteFile(executable, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	respawned := make(chan Status, 1)
	runInProcess(t, paths, Options{
		Version: "devel",
		Start: func(context.Context, *uiserver.Server) (App, error) {
			return App{}, nil
		},
		Respawn: func(exe string, status Status) error {
			if exe != executable {
				t.Errorf("restarted as %s", exe)
			}
			respawned <- status
			return nil
		},
		trust:         func(path string) (string, error) { return path, nil },
		executable:    executable,
		replacedCheck: 10 * time.Millisecond,
	})
	select {
	case <-respawned:
		t.Fatal("restarted before the executable was replaced")
	case <-time.After(1500 * time.Millisecond):
	}
	replaceFile(t, executable, "new")
	select {
	case status := <-respawned:
		if status.Version != "devel" || !status.Stopping {
			t.Fatalf("%+v", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("never restarted")
	}
}

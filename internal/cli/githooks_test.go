package cli

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/repos"
)

func newHooksClone(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/" + name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	return path
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}

// waitForHooks waits for all three dispatchers of a clone.
func waitForHooks(t *testing.T, clone string) {
	t.Helper()
	eventually(t, "no dispatchers in "+clone, func() bool {
		for _, hook := range []string{"post-commit", "post-rewrite", "pre-push"} {
			if _, err := os.Stat(filepath.Join(clone, ".git", "hooks", hook)); err != nil {
				return false
			}
		}
		return true
	})
}

// The daemon's registry ensures a clone's hooks when it is registered, the
// service's sweep covers the clones a registry already holds, and a clone that
// is removed gets its own hook back; the CLI's registry installs none.
func TestDaemonRegistryEnsuresGitHooks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cli, kept, added := newHooksClone(t, "cli"), newHooksClone(t, "kept"), newHooksClone(t, "added")
	r, err := repos.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Add(t.Context(), cli, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cli, ".git", "hooks", "post-commit")); err == nil {
		t.Fatal("a registration without the daemon installed hooks")
	}
	service := gitHooks(r, slog.Default())
	service.Every = time.Hour
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	defer func() { stop(); <-done }()
	// The sweep at start covers the clone already registered.
	waitForHooks(t, cli)
	if _, err := r.Add(t.Context(), kept, nil); err != nil {
		t.Fatal(err)
	}
	waitForHooks(t, kept)
	own := filepath.Join(added, ".git", "hooks", "pre-push")
	if err := os.WriteFile(own, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	clone, err := r.AddFor(t.Context(), added, "owner/added", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForHooks(t, added)
	if err := r.Remove(t.Context(), clone.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the removed clone's hook did not come back", func() bool {
		data, err := os.ReadFile(own)
		return err == nil && string(data) == "#!/bin/sh\necho mine\n"
	})
}

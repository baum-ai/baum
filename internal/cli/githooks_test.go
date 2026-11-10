package cli

import (
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
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	return path
}

func waitForHook(t *testing.T, clone string) {
	t.Helper()
	hook := filepath.Join(clone, ".git", "hooks", "post-commit")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(hook); err == nil {
			return
		}
	}
	t.Fatalf("no dispatcher in %s", hook)
}

// The daemon's registry ensures a clone's hooks when it is registered, and the
// service's sweep covers the available clones a registry already holds; the
// CLI's registry installs none.
func TestDaemonRegistryEnsuresGitHooks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	go service.Run(t.Context())
	// The sweep at start covers the clone already registered.
	waitForHook(t, cli)
	if _, err := r.Add(t.Context(), kept, nil); err != nil {
		t.Fatal(err)
	}
	waitForHook(t, kept)
	if _, err := r.AddFor(t.Context(), added, "owner/added", nil); err != nil {
		t.Fatal(err)
	}
	waitForHook(t, added)
}

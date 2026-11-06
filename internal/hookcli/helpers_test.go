//go:build !windows

package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

var systemBinaryPath = os.Getenv("PATH")

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "baum-hooks-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", dir)
	_ = os.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	_ = os.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.Setenv("XDG_STATE_HOME", dir)
	_ = os.Setenv("XDG_RUNTIME_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func runCLI(t *testing.T, args ...string) (string, string, error) {
	return runCLIWithInput(t, "", args...)
}

func runCLIWithInput(t *testing.T, input string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut strings.Builder
	err := runContext(context.Background(), args, strings.NewReader(input), &out, &errOut)
	return out.String(), errOut.String(), err
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeAgentSettings(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHookTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func useTestHookDaemon(t *testing.T) daemon.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	paths := daemon.Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "d.sock")}
	oldPaths, oldSpawn := hookDaemonPaths, hookDaemonSpawn
	hookDaemonPaths = func() (daemon.Paths, error) { return paths, nil }
	hookDaemonSpawn = func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		return nil, errors.New("test: no daemon")
	}
	t.Cleanup(func() { hookDaemonPaths, hookDaemonSpawn = oldPaths, oldSpawn; _ = os.RemoveAll(dir) })
	return paths
}

func spooledHookEvents(t *testing.T, paths daemon.Paths) ([]hookevents.Event, [][]byte) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(hookevents.SpoolDir(paths), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	var events []hookevents.Event
	var raw [][]byte
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var event hookevents.Event
		if err = json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		events, raw = append(events, event), append(raw, data)
	}
	return events, raw
}

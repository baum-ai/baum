//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/shellenv"
)

func stubLoginShell(t *testing.T, path string, err error) {
	t.Helper()
	old := resolveShell
	t.Cleanup(func() { resolveShell = old })
	resolveShell = func(context.Context, shellenv.Options) (shellenv.Result, error) {
		if err != nil {
			return shellenv.Result{Shell: "/bin/zsh"}, err
		}
		return shellenv.Result{Shell: "/bin/zsh", Env: map[string]string{"PATH": path}}, nil
	}
}

func loginPathJSON(t *testing.T) loginPath {
	t.Helper()
	stdout, _, err := run(t, "daemon", "login-path", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report loginPath
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	return report
}

func writeScript(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// The first executable baum on the login shell's PATH is reported with its
// version; a file that cannot run and a dangling link before it are skipped,
// as a shell skips them.
func TestLoginPathFindsTheBaumHooksRun(t *testing.T) {
	notExecutable, dangling, installed := t.TempDir(), t.TempDir(), t.TempDir()
	writeScript(t, filepath.Join(notExecutable, "baum"), "exit 1", 0o644)
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(dangling, "baum")); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(installed, "baum"), `[ "$1" = --version ] && echo "baum version v9.9.9"`, 0o755)
	stubLoginShell(t, strings.Join([]string{notExecutable, "relative/bin", dangling, installed}, ":"), nil)

	report := loginPathJSON(t)
	if !report.LoginShell || report.Shell != "/bin/zsh" || report.Error != "" {
		t.Errorf("report = %+v", report)
	}
	if len(report.Path) != 4 || report.Path[3] != installed {
		t.Errorf("path = %q", report.Path)
	}
	want := filepath.Join(installed, "baum")
	resolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if report.Baum == nil || report.Baum.Path != want || report.Baum.Resolved != resolved || report.Baum.Version != "v9.9.9" || report.Baum.Self {
		t.Errorf("baum = %+v, want %s v9.9.9", report.Baum, want)
	}
}

// A link to this very executable is reported as itself, without running it.
func TestLoginPathRecognisesItself(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, "baum")); err != nil {
		t.Fatal(err)
	}
	stubLoginShell(t, dir, nil)
	report := loginPathJSON(t)
	resolved, _ := filepath.EvalSymlinks(self)
	if report.Baum == nil || !report.Baum.Self || report.Baum.Resolved != resolved || report.Baum.Version != version() {
		t.Errorf("baum = %+v", report.Baum)
	}
}

// Without a login shell the inherited PATH is used and the reason given.
func TestLoginPathFallsBackToTheInheritedPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	stubLoginShell(t, "", errors.New("login shell /bin/zsh did not finish within 10s"))
	report := loginPathJSON(t)
	if report.LoginShell || !strings.Contains(report.Error, "did not finish") {
		t.Errorf("report = %+v", report)
	}
	if len(report.Path) != 1 || report.Path[0] != dir || report.Baum != nil {
		t.Errorf("path = %q, baum = %+v", report.Path, report.Baum)
	}
	stdout, _, err := run(t, "daemon", "login-path")
	if err != nil || !strings.Contains(stdout, "baum: not found on PATH") {
		t.Errorf("text output: %v\n%s", err, stdout)
	}
}

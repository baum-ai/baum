//go:build unix

package shellenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPrintEnvHelper is the printer the shells under test run: this test
// binary, started by the shell with ResolvingVariable set.
func TestPrintEnvHelper(t *testing.T) {
	if os.Getenv(ResolvingVariable) != "1" {
		t.Skip("printer process")
	}
	// The test binary's own output follows the closing marker and is ignored.
	if err := PrintEnv(os.Stdout, os.Args[len(os.Args)-1]); err != nil {
		t.Fatal(err)
	}
}

func printer(t *testing.T) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{self, "-test.run=^TestPrintEnvHelper$", "--"}
}

// fakeShell writes an executable named name that records its arguments, runs
// body, then runs its last argument with /bin/sh.
func fakeShell(t *testing.T, name, body string) (shell, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	shell = filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\n" + body + "\nfor a; do last=$a; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shell, argsFile
}

func TestResolveFakeShellsIgnoreNoiseAndInheritedEnvironment(t *testing.T) {
	noise := `echo "Welcome back"; echo '__baum_env_not_the_marker__{"GITHUB_TOKEN":"forged"}__baum_env_not_the_marker__'; echo rc warning >&2; export GITHUB_TOKEN=from-shell; export PATH="/opt/tools/bin:$PATH"`
	for _, name := range []string{"bash", "zsh", "fish"} {
		t.Run(name, func(t *testing.T) {
			shell, argsFile := fakeShell(t, name, noise)
			inherited := []string{"HOME=" + t.TempDir(), "SHELL=/bin/false", "PATH=/agent/bin", "GITHUB_TOKEN=from-agent", "AGENT_ONLY=1", "LC_ALL=C.UTF-8"}
			result, err := Resolve(context.Background(), Options{Shell: shell, Printer: printer(t), Environ: inherited})
			if err != nil {
				t.Fatal(err)
			}
			if result.Shell != shell || result.Env["GITHUB_TOKEN"] != "from-shell" || result.Env[ResolvingVariable] != "1" || result.Env["LC_ALL"] != "C.UTF-8" {
				t.Fatalf("env %v", result.Env)
			}
			if _, ok := result.Env["AGENT_ONLY"]; ok {
				t.Fatal("inherited variable reached the shell")
			}
			if !strings.HasPrefix(result.Env["PATH"], "/opt/tools/bin:"+defaultPath) {
				t.Fatalf("PATH %q", result.Env["PATH"])
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(string(args)), "\n"); !slices.Equal(got[:3], []string{"-i", "-l", "-c"}) || len(got) != 4 {
				t.Fatalf("arguments %q", got)
			}
		})
	}
}

func TestResolveSlowShellTimesOut(t *testing.T) {
	shell, _ := fakeShell(t, "zsh", "sleep 30 & wait")
	start := time.Now()
	_, err := Resolve(context.Background(), Options{Shell: shell, Printer: printer(t), Timeout: 300 * time.Millisecond, Environ: []string{}})
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %s for a killed shell", elapsed)
	}
}

func TestResolveKillsWhatStartupFilesLeaveRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	shell, _ := fakeShell(t, "bash", "sleep 300 >/dev/null 2>&1 & echo $! > '"+pidFile+"'")
	if _, err := Resolve(context.Background(), Options{Shell: shell, Printer: printer(t), Environ: []string{}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("a process the startup files started outlived the resolution")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reports a running process; a zombie nobody reaps counts as dead.
func alive(pid int) bool {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		return len(fields) > 0 && fields[0] != "Z"
	}
	return syscall.Kill(pid, 0) == nil
}

func TestResolveFailingShell(t *testing.T) {
	for name, body := range map[string]string{
		"exits":       "echo broken rc >&2; exit 3",
		"no printer":  "echo only noise; exit 0",
		"cut short":   "printf '__baum_env_'; exit 0",
		"bad command": "exec /nonexistent",
	} {
		t.Run(name, func(t *testing.T) {
			shell, _ := fakeShell(t, "bash", body)
			if result, err := Resolve(context.Background(), Options{Shell: shell, Printer: printer(t), Environ: []string{}}); err == nil {
				t.Fatalf("resolved %v", result.Env)
			}
		})
	}
	if _, err := Resolve(context.Background(), Options{Shell: "relative-shell", Printer: printer(t), Environ: []string{}}); err == nil {
		t.Fatal("relative shell accepted")
	}
}

// TestResolveInstalledShells runs the shells this machine has, with startup
// files that print noise and export a token the way users do.
func TestResolveInstalledShells(t *testing.T) {
	for _, tc := range []struct{ name, file, line string }{
		{"bash", ".bash_profile", "echo bash says hi; export GITHUB_TOKEN=from-bash"},
		{"zsh", ".zshrc", "echo zsh says hi; export GITHUB_TOKEN=from-zsh"},
		{"fish", ".config/fish/config.fish", "echo fish says hi; set -gx GITHUB_TOKEN from-fish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell, err := exec.LookPath(tc.name)
			if err != nil {
				t.Skip(tc.name + " is not installed")
			}
			home := t.TempDir()
			rc := filepath.Join(home, tc.file)
			if err = os.MkdirAll(filepath.Dir(rc), 0o700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(rc, []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := Resolve(context.Background(), Options{Shell: shell, Printer: printer(t), Environ: []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Env["GITHUB_TOKEN"] != "from-"+tc.name {
				t.Fatalf("GITHUB_TOKEN %q", result.Env["GITHUB_TOKEN"])
			}
		})
	}
}

func TestArguments(t *testing.T) {
	for shell, want := range map[string][]string{
		"/bin/bash": {"-i", "-l", "-c"}, "/usr/bin/zsh": {"-i", "-l", "-c"}, "/opt/homebrew/bin/fish": {"-i", "-l", "-c"}, "/bin/tcsh": {"-i", "-c"},
	} {
		if got := Arguments(shell); !slices.Equal(got, want) {
			t.Errorf("%s: %v", shell, got)
		}
	}
}

func TestPasswdShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(path, []byte("root:x:0:0:root:/root:/bin/bash\nada:x:1000:1000:Ada:/home/user:/usr/bin/fish\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := passwdShell(path, 1000); got != "/usr/bin/fish" {
		t.Fatal(got)
	}
	if got := passwdShell(path, 1001); got != "" {
		t.Fatal(got)
	}
	if got := detectShell("/bin/zsh"); got != "/bin/zsh" {
		t.Fatal(got)
	}
}

func TestQuoteAllSurvivesQuotes(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", "printf '%s|' "+quoteAll([]string{"it's", "a b", `back\slash`})).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `it's|a b|back\slash|` {
		t.Fatal(string(out))
	}
}

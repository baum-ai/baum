package codex

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/internal/proc"
)

// minimumVersion is the oldest Codex the Local profile runs. It is the release
// the hosted review agent image pins (tools/codex-cli/package.json), which runs
// every flag Run passes in production apart from --sandbox. Checked by
// installing @openai/codex 0.156.1 and 0.162.1: both list --ephemeral,
// --ignore-user-config, --ignore-rules and --output-schema for `codex exec`, and
// both accept `codex sandbox -P :read-only`, which the preflight uses. An older
// release that lacks one of the flags exits with an unknown-option error, which
// Run maps to the same upgrade request.
const minimumVersion = "0.156.1"

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// localBinary resolves the executable and, once per Backend, checks that it is
// recent enough and that its read-only sandbox works on this machine. A failed
// check is repeated by the next run, which may follow an upgrade or a change to
// the machine's configuration.
func (c *Backend) localBinary(ctx context.Context) (string, error) {
	binary, err := exec.LookPath(cmp.Or(c.Binary, "codex"))
	if err != nil {
		return "", fmt.Errorf("finding Codex: %w; install it and sign in with codex login", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked {
		c.version, c.checkFailure = checkVersion(ctx, binary)
		if c.checkFailure == nil {
			c.checkFailure = checkSandbox(ctx, binary, runtime.GOOS)
		}
		c.checked = c.checkFailure == nil
	}
	return binary, c.checkFailure
}

// Check resolves the executable, checks its version and that its read-only
// sandbox starts, as the first Run does, and returns the installed version.
// Only the Local profile has a check; the Container profile returns "" and nil.
// A host calls it before assembling anything, so a missing or old installation
// fails at once instead of as a provider failure retried by the loop.
func (c *Backend) Check(ctx context.Context) (version string, err error) {
	if c.Profile != Local {
		return "", nil
	}
	if _, err := c.localBinary(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, nil
}

// localCommand prepares a short-lived command in the local environment. The
// timeout and the caller's cancellation kill the whole group, and WaitDelay
// stops a descendant that holds the pipe from stalling the wait.
func localCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = agent.LocalEnvironment(os.Environ(), "CODEX_HOME")
	proc.Configure(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func checkVersion(ctx context.Context, binary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := localCommand(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", binary, err)
	}
	version := versionPattern.FindString(string(out))
	if version == "" {
		return "", fmt.Errorf("could not read a version from %s --version (%q); upgrade Codex to %s or newer", binary, strings.TrimSpace(string(out)), minimumVersion)
	}
	if compareVersions(version, minimumVersion) < 0 {
		return "", fmt.Errorf("codex %s is older than %s, which baum needs; upgrade Codex", version, minimumVersion)
	}
	return version, nil
}

// sandboxPlatform reports whether baum has checked that `codex sandbox`
// enforces its read-only profile on goos. Windows is not: its sandbox is a
// separate mechanism that was not examined, so a local run is refused there
// rather than run unverified.
func sandboxPlatform(goos string) bool { return goos != "windows" }

// checkSandbox runs a trivial command under Codex's read-only profile
// (`codex sandbox -P :read-only -- cat <file>`), which starts the same
// sandbox a run uses without calling a model or the network. On Linux it
// fails where the kernel or container forbids the namespaces the sandbox needs.
func checkSandbox(ctx context.Context, binary, goos string) error {
	if !sandboxPlatform(goos) {
		return fmt.Errorf("codex could not start its read-only sandbox on this machine: baum cannot verify Codex's read-only sandbox on %s; baum will not run codex without it", goos)
	}
	dir, err := os.MkdirTemp("", "baum-codex-preflight-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	const content = "baum sandbox check"
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := localCommand(ctx, binary, "sandbox", "-P", ":read-only", "--", "cat", file)
	cmd.Dir = dir
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == content {
		return checkWriteBlocked(ctx, binary, dir)
	}
	reason := firstLine(stderr.String())
	if reason == "" {
		if err != nil {
			reason = err.Error()
		} else {
			reason = "the sandboxed command printed nothing"
		}
	}
	return fmt.Errorf("codex could not start its read-only sandbox on this machine: %s; baum will not run codex without it", reason)
}

// The sandboxed shell prints one of these after it tried the write, so a run
// that never reached the write proves nothing.
const (
	writeBlocked = "baum-write-blocked"
	writeAllowed = "baum-write-allowed"
)

// checkWriteBlocked proves the read-only profile is enforced, not just
// accepted: a sandboxed shell must try to create a file in dir, report that
// the write failed, and leave no file behind. The command is sh with the path
// as an argument, which works the same on Linux and macOS.
func checkWriteBlocked(ctx context.Context, binary, dir string) error {
	probe := filepath.Join(dir, "probe")
	defer func() { _ = os.Remove(probe) }()
	script := `if (echo x > "$1") 2>/dev/null; then echo ` + writeAllowed + `; else echo ` + writeBlocked + `; fi`
	cmd := localCommand(ctx, binary, "sandbox", "-P", ":read-only", "--", "sh", "-c", script, "sh", probe)
	cmd.Dir = dir
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	_, statErr := os.Stat(probe)
	result := strings.TrimSpace(string(out))
	switch {
	case result == writeAllowed || statErr == nil:
		return errors.New("codex ran a command without enforcing its read-only sandbox on this machine; baum will not run codex")
	case err == nil && result == writeBlocked:
		return nil
	}
	reason := firstLine(stderr.String())
	if reason == "" {
		if err != nil {
			reason = err.Error()
		} else {
			reason = "the sandboxed write check did not run"
		}
	}
	return fmt.Errorf("codex could not run its read-only sandbox check on this machine: %s; baum will not run codex without it", reason)
}

// compareVersions orders two dotted numeric versions of the same length.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range as {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return 0
}

// unknownOption reports the CLI refusing a flag it does not have, which an
// older release does when it predates one of the arguments, and returns the
// line that says so. Only the parser's own wording counts (clap's "unexpected
// argument", or "unknown option"/"unknown argument"): other "unknown" errors
// are not a version problem.
func unknownOption(stderr string) (string, bool) {
	for _, line := range strings.Split(stderr, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "unexpected argument") || strings.Contains(lower, "unknown option") || strings.Contains(lower, "unknown argument") {
			return boundedLine(line), true
		}
	}
	return "", false
}

// firstLine is the first non-empty line of text, bounded.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return boundedLine(line)
		}
	}
	return ""
}

func boundedLine(line string) string {
	line = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, strings.TrimSpace(line))
	return line[:min(len(line), 200)]
}

package codex

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/agent"
	"github.com/lastpersonlabs/baum/agent/internal/proc"
)

// minimumVersion is the oldest Codex the Local profile runs. It is the first
// release checked to have every flag and option Run passes (codex-cli 0.162.1:
// `codex exec --help` lists --json, --ephemeral, --ignore-user-config,
// --ignore-rules, --disable, --sandbox read-only, --skip-git-repo-check,
// --output-schema and --model, and `codex features list` names the hooks, apps
// and multi_agent features). Earlier releases were not examined, so the floor is
// the release that was.
const minimumVersion = "0.162.1"

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// localBinary resolves the executable and, once per Backend, checks its
// version. A failed check is repeated by the next run, which may follow an
// upgrade.
func (c *Backend) localBinary(ctx context.Context) (string, error) {
	binary, err := exec.LookPath(cmp.Or(c.Binary, "codex"))
	if err != nil {
		return "", fmt.Errorf("finding Codex: %w; install it and sign in with codex login", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.versionOK {
		c.versionErr = checkVersion(ctx, binary)
		c.versionOK = c.versionErr == nil
	}
	return binary, c.versionErr
}

func checkVersion(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = agent.LocalEnvironment(os.Environ(), "CODEX_HOME")
	// The timeout and the caller's cancellation kill the whole group, and
	// WaitDelay stops a descendant that holds the pipe from stalling the wait.
	proc.Configure(cmd)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("running %s --version: %w", binary, err)
	}
	version := versionPattern.FindString(string(out))
	if version == "" {
		return fmt.Errorf("could not read a version from %s --version (%q); upgrade Codex to %s or newer", binary, strings.TrimSpace(string(out)), minimumVersion)
	}
	if compareVersions(version, minimumVersion) < 0 {
		return fmt.Errorf("codex %s is older than %s, which baum needs; upgrade Codex", version, minimumVersion)
	}
	return nil
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
// older release does when it predates one of the arguments. Only the parser's
// own wording counts (clap's "unexpected argument", or "unknown option"/
// "unknown argument"): other "unknown" errors are not a version problem.
func unknownOption(stderr string) bool {
	stderr = strings.ToLower(stderr)
	return strings.Contains(stderr, "unexpected argument") || strings.Contains(stderr, "unknown option") || strings.Contains(stderr, "unknown argument")
}

// sandboxUnavailable reports Codex failing to set up its Linux sandbox
// (bubblewrap's namespaces, Landlock or seccomp), which happens on kernels or
// containers that forbid unprivileged user namespaces. The patterns are
// narrow: each needs the sandbox component's name together with a failure
// word, so an unrelated error is not mislabelled. It returns the first
// matching line, bounded.
func sandboxUnavailable(texts ...string) (string, bool) {
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			var component bool
			var failure []string
			switch {
			case strings.Contains(lower, "bwrap") || strings.Contains(lower, "bubblewrap"):
				component, failure = true, []string{"namespace", "uid map", "permission denied", "operation not permitted"}
			case strings.Contains(lower, "landlock"):
				component, failure = true, []string{"not supported", "unavailable", "failed", "restrict"}
			case strings.Contains(lower, "seccomp"):
				component, failure = true, []string{"failed", "unavailable", "not permitted", "not supported"}
			}
			if !component {
				continue
			}
			for _, word := range failure {
				if strings.Contains(lower, word) {
					line = strings.TrimSpace(line)
					return line[:min(len(line), 200)], true
				}
			}
		}
	}
	return "", false
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line[:min(len(line), 200)]
}

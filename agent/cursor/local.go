package cursor

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/proc"
)

// requiredHelp is what `cursor-agent --help` must list for a run to work: the
// flags args passes and the values it gives them. They were read from the help
// of cursor-agent 2026.10.16-e3cb207. Cursor's releases are dated rather than
// numbered by compatibility, so the installed CLI is checked for these instead
// of against a minimum version.
var requiredHelp = []string{"--print", "--output-format", "stream-json", "--mode", `"ask"`, "--sandbox", `"enabled"`, "--workspace", "--trust"}

// identity is how cursor-agent's help names the tool. A binary called agent is
// used only when its help says this.
const identity = "Cursor Agent"

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Check resolves the executable and checks that it is the Cursor Agent and
// lists every option a run passes, as the first Run does, and returns the
// installed version. A host calls it before assembling anything, so a missing
// or incompatible installation fails at once instead of as a provider failure
// retried by the loop. It does not check the login or call a model.
func (c *Backend) Check(ctx context.Context) (version string, err error) {
	if _, err := c.resolve(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, nil
}

// Path is the executable a successful Check or Run resolved, empty before.
func (c *Backend) Path() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

// resolve finds the executable and, once per Backend, checks it. A failed check
// is repeated by the next run, which may follow an upgrade.
func (c *Backend) resolve(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checked {
		c.path, c.version, c.failure = c.find(ctx)
		c.checked = c.failure == nil
	}
	return c.path, c.failure
}

// find resolves the executable: Binary when set, else cursor-agent, else agent.
func (c *Backend) find(ctx context.Context) (path, version string, err error) {
	candidates := []string{"cursor-agent", "agent"}
	if c.Binary != "" {
		candidates = []string{c.Binary}
	}
	var lookErr error
	for i, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			if i == 0 {
				lookErr = err
			}
			continue
		}
		help, err := run(ctx, path, "--help")
		if !strings.Contains(help, identity) {
			if name == "agent" {
				// Some other tool called agent: not an error, just not Cursor.
				continue
			}
			if err != nil {
				return "", "", fmt.Errorf("running %s --help: %w", path, err)
			}
			return "", "", fmt.Errorf("%s does not say it is the %s (its --help does not name it); baum runs only the Cursor Agent CLI", path, identity)
		}
		if missing := missingOptions(help); len(missing) > 0 {
			return "", "", fmt.Errorf("cursor-agent at %s does not list the options baum needs (%s); upgrade it with `cursor-agent update`", path, strings.Join(missing, ", "))
		}
		out, err := run(ctx, path, "--version")
		if err != nil {
			return "", "", fmt.Errorf("running %s --version: %w", path, err)
		}
		version = versionPattern.FindString(out)
		if version == "" {
			return "", "", fmt.Errorf("could not read a version from %s --version (%q)", path, boundedLine(out))
		}
		return path, version, nil
	}
	return "", "", fmt.Errorf("finding cursor-agent: %w; install the Cursor Agent CLI and sign in with cursor-agent login", lookErr)
}

// missingOptions are the required options and values the help does not list.
func missingOptions(help string) []string {
	var missing []string
	for _, option := range requiredHelp {
		if !strings.Contains(help, option) {
			missing = append(missing, strings.Trim(option, `"`))
		}
	}
	return missing
}

// run runs a short-lived command in the run's environment and returns its
// output. The timeout and the caller's cancellation kill the whole group, and
// WaitDelay stops a descendant that holds the pipe from stalling the wait.
func run(ctx context.Context, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = environment()
	proc.Configure(cmd)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	return string(out), err
}

// runFailure explains a run that exited with an error, from its stderr, which
// is the user's own and bounded, and classifies it into a fixed word for the
// run's error event.
func runFailure(waitErr error, stderr string) (status string, err error) {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "unknown option") || strings.Contains(lower, "unknown argument") || strings.Contains(lower, "invalid argument"):
		return "unsupported_option", fmt.Errorf("cursor-agent does not accept the options baum needs (%s); upgrade it with `cursor-agent update`", firstLine(stderr))
	case strings.Contains(lower, "sandbox mode is enabled but not available"):
		return "sandbox_unavailable", fmt.Errorf("cursor-agent could not start its sandbox on this machine (%s); baum runs cursor-agent only with its sandbox enabled", firstLine(stderr))
	case strings.Contains(lower, "not authenticated") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "login required") || strings.Contains(lower, "authentication required"):
		return "authentication_error", fmt.Errorf("cursor-agent is not signed in (%s); run cursor-agent login", firstLine(stderr))
	}
	status = agent.ErrorStatus(stderr)
	if line := firstLine(stderr); line != "" {
		return status, fmt.Errorf("cursor-agent failed: %w (%s)", waitErr, line)
	}
	return status, fmt.Errorf("cursor-agent failed: %w", waitErr)
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

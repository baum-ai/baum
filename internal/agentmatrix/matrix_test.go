package agentmatrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// result is one line of the results file the report is rendered from.
type result struct {
	Agent   string `json:"agent"`
	Version string `json:"version"`
	Support string `json:"support"`
	Check   string `json:"check"`
	Status  string `json:"status"` // pass, fail, skip or info
	Detail  string `json:"detail"`
}

// matrix is the release under test and the isolated machine it runs on.
type matrix struct {
	t       *testing.T
	agent   string // claude-code or codex
	version string // the requested release
	support string
	bin     string // the agent's executable
	prefix  string // its npm install
	baum    string // the baum executable
	repo    string // the oss/ directory
	results string
}

var resultsMu sync.Mutex

func newMatrix(t *testing.T) *matrix {
	t.Helper()
	m := &matrix{t: t, agent: os.Getenv("BAUM_MATRIX_AGENT"), version: os.Getenv("BAUM_MATRIX_VERSION"),
		support: os.Getenv("BAUM_MATRIX_SUPPORT"), bin: os.Getenv("BAUM_MATRIX_BIN"), prefix: os.Getenv("BAUM_MATRIX_PREFIX"),
		baum: os.Getenv("BAUM_MATRIX_BAUM"), repo: os.Getenv("BAUM_MATRIX_REPO"), results: os.Getenv("BAUM_MATRIX_RESULTS")}
	if m.agent == "" || m.bin == "" || m.baum == "" {
		t.Skip("set BAUM_MATRIX_AGENT, _VERSION, _BIN and _BAUM (scripts/agent-matrix/run.sh does)")
	}
	if m.agent != "claude-code" && m.agent != "codex" {
		t.Fatalf("unknown agent %q", m.agent)
	}
	return m
}

func (m *matrix) record(check, status, format string, args ...any) {
	m.t.Helper()
	r := result{m.agent, m.version, m.support, check, status, fmt.Sprintf(format, args...)}
	m.t.Logf("%s %s %s %s: %s: %s", r.Agent, r.Version, r.Support, r.Check, r.Status, r.Detail)
	if m.results == "" {
		return
	}
	line, _ := json.Marshal(r)
	resultsMu.Lock()
	defer resultsMu.Unlock()
	f, err := os.OpenFile(m.results, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// home is a fresh home directory for one check, with the agent's own
// configuration directory set and baum's state directories inside it.
type home struct {
	dir string
	env []string
}

func (m *matrix) newHome(extraPath ...string) home {
	m.t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("BAUM_MATRIX_HOMES"), "home")
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := append([]string{filepath.Dir(m.bin), filepath.Dir(m.baum)}, extraPath...)
	path = append(path, "/usr/local/go/bin", "/usr/local/bin", "/usr/bin", "/bin")
	env := []string{"HOME=" + dir, "PATH=" + strings.Join(path, ":"), "TMPDIR=" + filepath.Join(dir, ".tmp"), "TERM=dumb",
		"CODEX_HOME=" + filepath.Join(dir, ".codex"),
		"XDG_STATE_HOME=" + filepath.Join(dir, ".state"), "XDG_CONFIG_HOME=" + filepath.Join(dir, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(dir, ".cache"), "XDG_RUNTIME_DIR=" + filepath.Join(dir, ".run"),
		// Nothing here may reach a model or an account.
		"ANTHROPIC_BASE_URL=http://127.0.0.1:1", "DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for _, d := range []string{".tmp", ".codex", ".claude", ".state", ".config", ".cache", ".run"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			m.t.Fatal(err)
		}
	}
	return home{dir: dir, env: env}
}

// setenv makes this process see h, for the backends the test runs in-process.
func (h home) setenv(t *testing.T) {
	t.Helper()
	for _, kv := range h.env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

// run runs name with args in dir under h's environment and returns its
// combined output. A timeout kills it.
func (h home) run(dir string, timeout time.Duration, stdin string, name string, args ...string) (string, int, error) {
	out, _, code, err := h.run2(dir, timeout, stdin, name, args...)
	return out, code, err
}

// run2 is run that also returns standard output alone.
func (h home) run2(dir string, timeout time.Duration, stdin string, name string, args ...string) (combined, stdout string, code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = 2 * time.Second
	var only, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &only, &errs
	err = cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return only.String() + errs.String(), only.String(), code, err
}

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

func TestAgentMatrix(t *testing.T) {
	m := newMatrix(t)
	checks := []struct {
		name string
		run  func(*matrix)
	}{
		{"version", (*matrix).checkVersion},
		{"sandbox", (*matrix).checkSandbox},
		{"flags", (*matrix).checkFlags},
		{"events", (*matrix).checkHookEvents},
		{"plugin", (*matrix).checkPluginNative},
		{"trust", (*matrix).checkTrust},
		{"baum-install", (*matrix).checkBaumInstall},
		{"replay", (*matrix).checkReplay},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			sub := *m
			sub.t = t
			c.run(&sub)
		})
	}
}

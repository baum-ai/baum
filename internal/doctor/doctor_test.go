package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
)

// Synthetic secrets: none of them may appear in any output.
const (
	plantedEnvToken    = "ghp_PLANTEDenvTOKEN0000000000000000000000"
	plantedGHToken     = "ghp_PLANTEDghTOKEN00000000000000000000000"
	plantedFileToken   = "ghp_PLANTEDfileTOKEN0000000000000000000000"
	plantedKeyringTok  = "gho_PLANTEDkeyringTOKEN000000000000000000"
	plantedAnthropicAK = "sk-ant-PLANTEDapikey000000000000000000000"
)

// machine is an isolated fake home with a directory of fake executables that
// is the whole of PATH.
type machine struct {
	t      *testing.T
	bin    string
	config string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake executables are shell scripts")
	}
	home := t.TempDir()
	m := &machine{t: t, bin: t.TempDir(), config: filepath.Join(home, "config")}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", m.config)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("PATH", m.bin)
	for _, name := range []string{"GH_PATH", "GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "BAUM_BACKEND", "BAUM_MODEL", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "")
	}
	return m
}

func (m *machine) script(dir, name, body string) string {
	m.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		m.t.Fatal(err)
	}
	return path
}

func (m *machine) install(name, body string) string { return m.script(m.bin, name, body) }

func (m *machine) git() { m.install("git", `echo "git version 2.43.0"`) }

func (m *machine) claude(version string) string {
	return m.install("claude", `echo "`+version+` (Claude Code)"`)
}

// codex answers --version and runs the read-only sandbox preflight the way a
// working Codex does: the first command reads the file, the second reports
// that its write was blocked.
func (m *machine) codex(version string) string {
	return m.install("codex", `case "$1" in
--version) echo "codex-cli `+version+`";;
sandbox)
  shift 4
  case "$1" in
  cat) /bin/cat "$2";;
  sh) echo baum-write-blocked;;
  esac;;
esac`)
}

func (m *machine) writeConfig(body string) string {
	m.t.Helper()
	path := filepath.Join(m.config, "baum", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		m.t.Fatal(err)
	}
	return path
}

func run(t *testing.T, opts Options) Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return Run(ctx, opts)
}

func find(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %v", id, ids(r))
	return Check{}
}

func ids(r Report) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.ID)
	}
	return out
}

func wantStatus(t *testing.T, c Check, status Status) {
	t.Helper()
	if c.Status != status {
		t.Errorf("%s: status = %s, want %s\n%+v", c.ID, c.Status, status, c)
	}
}

func wantContains(t *testing.T, what, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Errorf("%s = %q, want it to contain %q", what, text, part)
		}
	}
}

func TestEveryProblemAndWarningHasAFix(t *testing.T) {
	newMachine(t) // nothing installed
	report := run(t, Options{})
	if len(report.Checks) == 0 {
		t.Fatal("empty report")
	}
	for _, c := range report.Checks {
		if (c.Status == StatusProblem || c.Status == StatusWarning) && c.Fix == "" {
			t.Errorf("%s is a %s without a fix: %+v", c.ID, c.Status, c)
		}
		if c.ID == "" || c.Label == "" || c.Detail == "" {
			t.Errorf("incomplete check %+v", c)
		}
	}
	if report.Status != StatusProblem {
		t.Errorf("status = %s, want problem", report.Status)
	}
}

func TestReportCoversEveryItem(t *testing.T) {
	m := newMachine(t)
	m.git()
	report := run(t, Options{InstanceDir: func() (string, error) { return "/instance", nil }})
	for _, id := range []string{"git", "gh", "claude", "codex", "cursor-agent", "backend-auto", "github-token-github.com", "anthropic-api-key", "prices", "config", "registry", "jobs", "discovery", "instance", "path"} {
		find(t, report, id)
	}
}

func TestGit(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{}), "git")
		wantStatus(t, c, StatusProblem)
		wantContains(t, "fix", c.Fix, "install git", "2.29")
	})
	t.Run("present", func(t *testing.T) {
		m := newMachine(t)
		path := m.install("git", `echo "git version 2.43.0"`)
		c := find(t, run(t, Options{}), "git")
		wantStatus(t, c, StatusOK)
		if c.Path != path || c.Version != "2.43.0" || c.Source != "PATH" {
			t.Errorf("%+v", c)
		}
	})
	t.Run("too old", func(t *testing.T) {
		m := newMachine(t)
		m.install("git", `echo "git version 2.28.0"`)
		c := find(t, run(t, Options{}), "git")
		wantStatus(t, c, StatusProblem)
		wantContains(t, "fix", c.Fix, "upgrade git", "2.29")
	})
	t.Run("does not run", func(t *testing.T) {
		m := newMachine(t)
		m.install("git", `exit 3`)
		c := find(t, run(t, Options{}), "git")
		wantStatus(t, c, StatusProblem)
		wantContains(t, "fix", c.Fix, "--version")
	})
}

func TestGH(t *testing.T) {
	t.Run("missing is a warning", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{}), "gh")
		wantStatus(t, c, StatusWarning)
		wantContains(t, "fix", c.Fix, "gh auth login", "GH_PATH")
	})
	t.Run("on PATH", func(t *testing.T) {
		m := newMachine(t)
		path := m.install("gh", `echo "gh version 2.40.1 (2024-01-01)"`)
		c := find(t, run(t, Options{}), "gh")
		wantStatus(t, c, StatusOK)
		if c.Path != path || c.Version != "2.40.1" || c.Source != "PATH" {
			t.Errorf("%+v", c)
		}
	})
	t.Run("GH_PATH wins over PATH", func(t *testing.T) {
		m := newMachine(t)
		m.install("gh", `echo "gh version 1.0.0"`)
		other := m.script(t.TempDir(), "gh-custom", `echo "gh version 2.50.0"`)
		t.Setenv("GH_PATH", other)
		c := find(t, run(t, Options{}), "gh")
		wantStatus(t, c, StatusOK)
		if c.Path != other || c.Version != "2.50.0" || c.Source != "GH_PATH" {
			t.Errorf("%+v", c)
		}
	})
	t.Run("GH_PATH to a missing file", func(t *testing.T) {
		m := newMachine(t)
		m.install("gh", `echo "gh version 1.0.0"`)
		t.Setenv("GH_PATH", filepath.Join(t.TempDir(), "missing"))
		c := find(t, run(t, Options{}), "gh")
		wantStatus(t, c, StatusWarning)
		wantContains(t, "fix", c.Fix, "GH_PATH")
	})
}

func TestClaudeAndCodexMissing(t *testing.T) {
	m := newMachine(t)
	m.git()
	path, _ := config.Path()
	report := run(t, Options{})
	for _, name := range []string{"claude", "codex"} {
		c := find(t, report, name)
		wantStatus(t, c, StatusWarning)
		wantContains(t, name+" fix", c.Fix, "install", "backend_paths."+name, path)
	}
	auto := find(t, report, "backend-auto")
	wantStatus(t, auto, StatusProblem)
	wantContains(t, "auto fix", auto.Fix, "backend_paths.claude", "backend_paths.codex")
	wantContains(t, "auto detail", auto.Detail, "no usable")
	if report.Status != StatusProblem {
		t.Errorf("status = %s", report.Status)
	}
}

func TestClaudePresentIsPickedByAuto(t *testing.T) {
	m := newMachine(t)
	m.git()
	claudePath := m.claude("2.1.7")
	report := run(t, Options{})
	c := find(t, report, "claude")
	wantStatus(t, c, StatusOK)
	if c.Path != claudePath || c.Version != "2.1.7" || c.Source != "PATH" {
		t.Errorf("%+v", c)
	}
	auto := find(t, report, "backend-auto")
	wantStatus(t, auto, StatusOK)
	wantContains(t, "auto", auto.Detail, "picks claude")
	if auto.Version != "2.1.7" {
		t.Errorf("auto version = %q", auto.Version)
	}
	wantStatus(t, find(t, report, "codex"), StatusWarning)
}

func TestCodexPresentIsPickedWhenClaudeIsMissing(t *testing.T) {
	m := newMachine(t)
	m.git()
	codexPath := m.codex("0.162.1")
	report := run(t, Options{})
	c := find(t, report, "codex")
	wantStatus(t, c, StatusOK)
	if c.Path != codexPath || c.Version != "0.162.1" {
		t.Errorf("%+v", c)
	}
	auto := find(t, report, "backend-auto")
	wantStatus(t, auto, StatusOK)
	wantContains(t, "auto", auto.Detail, "picks codex", "claude skipped", "finding Claude Code")
}

func TestClaudeBeatsCodexWhenBothAreUsable(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	m.codex("0.162.1")
	report := run(t, Options{})
	wantStatus(t, find(t, report, "claude"), StatusOK)
	wantStatus(t, find(t, report, "codex"), StatusOK)
	wantContains(t, "auto", find(t, report, "backend-auto").Detail, "picks claude")
	if report.Status != StatusWarning && report.Status != StatusOK {
		t.Errorf("status = %s, want no problem", report.Status)
	}
}

func TestTooOldBackendNamesTheUpgradeAndAutoFallsBack(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("1.0.0")
	m.codex("0.162.1")
	report := run(t, Options{})
	c := find(t, report, "claude")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "detail", c.Detail, "older than")
	wantContains(t, "fix", c.Fix, "upgrade Claude Code")
	if c.Version != "1.0.0" {
		t.Errorf("version = %q, want the installed 1.0.0", c.Version)
	}
	auto := find(t, report, "backend-auto")
	wantStatus(t, auto, StatusOK)
	wantContains(t, "auto", auto.Detail, "picks codex", "claude skipped", "older than")
}

func TestCodexSandboxFailureIsReported(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.install("codex", `case "$1" in --version) echo "codex-cli 0.162.1";; *) echo "no namespaces" >&2; exit 1;; esac`)
	c := find(t, run(t, Options{}), "codex")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "detail", c.Detail, "read-only sandbox", "no namespaces")
	wantContains(t, "fix", c.Fix, "sandbox")
}

func TestConfiguredBackendThatCannotRunIsAProblem(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	t.Setenv("BAUM_BACKEND", "codex")
	report := run(t, Options{})
	wantStatus(t, find(t, report, "codex"), StatusProblem)
	configured := find(t, report, "backend-configured")
	wantStatus(t, configured, StatusProblem)
	wantContains(t, "fix", configured.Fix, "backend = ", "BAUM_BACKEND")
	// auto is still reported, and it works.
	wantStatus(t, find(t, report, "backend-auto"), StatusOK)
}

func TestConfiguredBackendNotAvailableInThisVersion(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	t.Setenv("BAUM_BACKEND", "anthropic")
	c := find(t, run(t, Options{}), "backend-configured")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, "not available in this version")
}

func TestConfiguredBackendThatRunsIsOK(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.codex("0.162.1")
	t.Setenv("BAUM_BACKEND", "codex")
	c := find(t, run(t, Options{}), "backend-configured")
	wantStatus(t, c, StatusOK)
	if c.Version != "0.162.1" {
		t.Errorf("%+v", c)
	}
}

func TestExplicitBackendPathPointingAtAMissingFile(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7") // on PATH, and must not be used instead
	missing := filepath.Join(t.TempDir(), "no-such-claude")
	cfgPath := m.writeConfig("[backend_paths]\nclaude = '" + missing + "'\n")
	report := run(t, Options{})
	c := find(t, report, "claude")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, "backend_paths.claude", missing)
	wantContains(t, "fix", c.Fix, "backend_paths.claude", cfgPath)
	if c.Path != "" || !strings.Contains(c.Source, "backend_paths.claude") {
		t.Errorf("%+v", c)
	}
	auto := find(t, report, "backend-auto")
	if strings.Contains(auto.Detail, "picks claude") {
		t.Errorf("auto fell back to the claude on PATH: %s", auto.Detail)
	}
	wantContains(t, "auto detail", auto.Detail, "backend_paths.claude")
	if report.Status != StatusProblem {
		t.Errorf("status = %s", report.Status)
	}
}

func TestExplicitBackendPathIsUsedBeforePATH(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("1.0.0") // an old claude on PATH, which an explicit path must replace
	elsewhere := m.script(t.TempDir(), "claude-custom", `echo "2.2.0 (Claude Code)"`)
	m.writeConfig("[backend_paths]\nclaude = '" + elsewhere + "'\n")
	report := run(t, Options{})
	c := find(t, report, "claude")
	wantStatus(t, c, StatusOK)
	if c.Path != elsewhere || c.Version != "2.2.0" || !strings.Contains(c.Source, "backend_paths.claude") {
		t.Errorf("%+v", c)
	}
	wantContains(t, "auto", find(t, report, "backend-auto").Detail, "picks claude")
}

func TestExplicitCodexAndCursorAgentPaths(t *testing.T) {
	m := newMachine(t)
	m.git()
	dir := t.TempDir()
	codexPath := m.script(dir, "codex-custom", `case "$1" in
--version) echo "codex-cli 0.162.1";;
sandbox) shift 4; case "$1" in cat) /bin/cat "$2";; sh) echo baum-write-blocked;; esac;;
esac`)
	cursorPath := m.script(dir, "cursor-custom", `echo "2025.01.01-abc"`)
	m.writeConfig("[backend_paths]\ncodex = '" + codexPath + "'\ncursor-agent = '" + cursorPath + "'\n")
	report := run(t, Options{})
	wantStatus(t, find(t, report, "codex"), StatusOK)
	cursor := find(t, report, "cursor-agent")
	if cursor.Path != cursorPath || cursor.Version != "2025.01.01" {
		t.Errorf("%+v", cursor)
	}
}

func TestCursorAgent(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		c := find(t, run(t, Options{}), "cursor-agent")
		wantStatus(t, c, StatusInfo)
		wantContains(t, "detail", c.Detail, "not available in this version")
	})
	t.Run("present but not available", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		path := m.install("cursor-agent", `echo "2025.01.01-abc"`)
		report := run(t, Options{})
		c := find(t, report, "cursor-agent")
		wantStatus(t, c, StatusInfo)
		wantContains(t, "detail", c.Detail, "not available in this version")
		if c.Path != path || c.Version != "2025.01.01" {
			t.Errorf("%+v", c)
		}
		// Auto never picks it.
		wantStatus(t, find(t, report, "backend-auto"), StatusProblem)
	})
	t.Run("explicit path to a missing file", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		m.writeConfig("[backend_paths]\ncursor-agent = '" + filepath.Join(t.TempDir(), "gone") + "'\n")
		c := find(t, run(t, Options{}), "cursor-agent")
		wantStatus(t, c, StatusProblem)
		wantContains(t, "fix", c.Fix, "backend_paths.cursor-agent")
	})
}

func TestGitHubTokenSources(t *testing.T) {
	// fakeGH returns a gh that prints a token for `gh auth token --secure-storage`.
	fakeGH := func(t *testing.T, m *machine) string {
		m.install("gh", `echo "gh version 2.40.1"`)
		return m.script(t.TempDir(), "gh-keyring", `[ "$1 $2 $3" = "auth token --secure-storage" ] && echo `+plantedKeyringTok)
	}
	tests := []struct {
		name   string
		setup  func(t *testing.T, m *machine)
		source string
	}{
		{"GITHUB_TOKEN", func(t *testing.T, m *machine) { t.Setenv("GITHUB_TOKEN", plantedEnvToken) }, "environment variable GITHUB_TOKEN"},
		{"GH_TOKEN", func(t *testing.T, m *machine) { t.Setenv("GH_TOKEN", plantedGHToken) }, "environment variable GH_TOKEN"},
		{"config file", func(t *testing.T, m *machine) { m.writeConfig("github_token = '" + plantedFileToken + "'\n") }, "config file"},
		{"gh keyring", func(t *testing.T, m *machine) { t.Setenv("GH_PATH", fakeGH(t, m)) }, config.SourceGHKeyring},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			m.git()
			tt.setup(t, m)
			c := find(t, run(t, Options{}), "github-token-github.com")
			wantStatus(t, c, StatusOK)
			if c.Source != tt.source {
				t.Errorf("source = %q, want %q", c.Source, tt.source)
			}
		})
	}

	t.Run("none", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		c := find(t, run(t, Options{}), "github-token-github.com")
		wantStatus(t, c, StatusWarning)
		wantContains(t, "fix", c.Fix, "GITHUB_TOKEN", "github_token", "gh auth login")
	})

	t.Run("gh environment and hosts file labels pass through", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		for _, source := range []string{"environment variable GH_ENTERPRISE_TOKEN", config.SourceGHHostsFile} {
			c := find(t, run(t, Options{GitHubToken: func(context.Context, config.Config, string) (string, string, error) {
				return "t", source, nil
			}}), "github-token-github.com")
			if c.Status != StatusOK || c.Source != source {
				t.Errorf("%+v", c)
			}
		}
	})

	t.Run("lookup failure", func(t *testing.T) {
		m := newMachine(t)
		m.git()
		c := find(t, run(t, Options{GitHubToken: func(context.Context, config.Config, string) (string, string, error) {
			return "", "", errors.New("keyring locked")
		}}), "github-token-github.com")
		wantStatus(t, c, StatusProblem)
		wantContains(t, "fix", c.Fix, "gh auth status")
	})
}

func TestRegisteredCloneHostsGetTheirOwnTokenCheck(t *testing.T) {
	m := newMachine(t)
	m.git()
	registry := filepath.Join(m.config, "baum", "repos.sqlite")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	common := t.TempDir()
	clones := []repos.Clone{
		{ID: "a", Path: "/work/app", CommonDir: common, Repository: "acme/app", Available: true},
		{ID: "b", Path: "/work/ent", CommonDir: common, Repository: "ghe.example.com/acme/ent", Available: true},
		{ID: "c", Path: "/work/gone", CommonDir: common, Repository: "acme/gone", Available: false},
	}
	var asked []string
	report := run(t, Options{
		Registry: func(context.Context, string) ([]repos.Clone, error) { return clones, nil },
		GitHubToken: func(_ context.Context, cfg config.Config, host string) (string, string, error) {
			asked = append(asked, host)
			if host == "github.com" {
				return "t", "config file", nil
			}
			return cfg.ResolveGitHubTokenSource(context.Background(), host)
		},
	})
	// An unknown host is never asked for a token: baum would refuse it anyway.
	if strings.Join(asked, ",") != "github.com" {
		t.Errorf("asked for %v", asked)
	}
	ent := find(t, report, "github-token-ghe.example.com")
	wantStatus(t, ent, StatusProblem)
	wantContains(t, "detail", ent.Detail, "GH_HOST", "hosts.yml")
	wantContains(t, "fix", ent.Fix, "set GH_HOST=ghe.example.com or run `gh auth login --hostname ghe.example.com`")

	wantStatus(t, find(t, report, "store-a"), StatusInfo)
	gone := find(t, report, "store-c")
	wantStatus(t, gone, StatusWarning)
	wantContains(t, "fix", gone.Fix, "/work/gone", "baum repos remove c")
	wantContains(t, "registry", find(t, report, "registry").Detail, "3 registered clones")
}

func TestEnterpriseTokenFromTheEnvironmentIsFound(t *testing.T) {
	m := newMachine(t)
	m.git()
	registry := filepath.Join(m.config, "baum", "repos.sqlite")
	if err := os.MkdirAll(filepath.Dir(registry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", plantedGHToken)
	t.Setenv("GH_HOST", "ghe.example.com") // a host baum will talk to
	report := run(t, Options{Registry: func(context.Context, string) ([]repos.Clone, error) {
		return []repos.Clone{{ID: "b", Repository: "ghe.example.com/acme/ent"}}, nil
	}})
	c := find(t, report, "github-token-ghe.example.com")
	wantStatus(t, c, StatusOK)
	if c.Source != "environment variable GH_ENTERPRISE_TOKEN" {
		t.Errorf("source = %q", c.Source)
	}
}

func TestNoSecretAppearsInTheReport(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	m.install("gh", `echo "gh version 2.40.1"`)
	t.Setenv("GITHUB_TOKEN", plantedEnvToken)
	t.Setenv("GH_TOKEN", plantedGHToken)
	t.Setenv("ANTHROPIC_API_KEY", plantedAnthropicAK)
	t.Setenv("GH_PATH", m.script(t.TempDir(), "gh", `echo `+plantedKeyringTok))
	m.writeConfig("github_token = '" + plantedFileToken + "'\n")

	for _, tokens := range []map[string]string{
		{"GITHUB_TOKEN": plantedEnvToken, "GH_TOKEN": plantedGHToken},
		{"GITHUB_TOKEN": "", "GH_TOKEN": ""}, // then the file token
	} {
		for name, value := range tokens {
			t.Setenv(name, value)
		}
		report := run(t, Options{})
		var text bytes.Buffer
		if err := WriteText(&text, report); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{plantedEnvToken, plantedGHToken, plantedFileToken, plantedKeyringTok, plantedAnthropicAK, "PLANTED"} {
			if strings.Contains(text.String(), secret) || strings.Contains(string(data), secret) {
				t.Errorf("the report contains %q", secret)
			}
		}
		wantStatus(t, find(t, report, "github-token-github.com"), StatusOK)
		wantContains(t, "key", find(t, report, "anthropic-api-key").Detail, "set", "not available in this version", "do not receive")
	}

	// With no other source the keyring token is used, and still not shown.
	if err := os.Remove(filepath.Join(m.config, "baum", "config.toml")); err != nil {
		t.Fatal(err)
	}
	report := run(t, Options{})
	data, _ := json.Marshal(report)
	c := find(t, report, "github-token-github.com")
	if c.Source != config.SourceGHKeyring || strings.Contains(string(data), plantedKeyringTok) {
		t.Errorf("%+v", c)
	}
}

func TestAnthropicAPIKeyNotSet(t *testing.T) {
	newMachine(t)
	c := find(t, run(t, Options{}), "anthropic-api-key")
	wantStatus(t, c, StatusInfo)
	wantContains(t, "detail", c.Detail, "not set")
}

func TestPriceList(t *testing.T) {
	t.Run("embedded without a fetch", func(t *testing.T) {
		newMachine(t)
		refreshed := false
		c := find(t, run(t, Options{
			RefreshPrices: func(context.Context) error { refreshed = true; return nil },
			PriceSource:   func() pricing.Source { return pricing.Source{Origin: pricing.OriginEmbedded, Version: "2026-01-01"} },
		}), "prices")
		if refreshed {
			t.Error("fetched although FetchPrices was off")
		}
		wantStatus(t, c, StatusInfo)
		if c.Source != "embedded" || c.Version != "2026-01-01" {
			t.Errorf("%+v", c)
		}
		wantContains(t, "detail", c.Detail, "embedded", "keeps the newest one in its state directory")
	})
	t.Run("cached by baum ui", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{
			FetchPrices:   true,
			RefreshPrices: func(context.Context) error { return nil },
			PriceSource:   func() pricing.Source { return pricing.Source{Origin: pricing.OriginCached, Version: "2026-10-01"} },
		}), "prices")
		wantStatus(t, c, StatusOK)
		if c.Source != "cached" || c.Version != "2026-10-01" {
			t.Errorf("%+v", c)
		}
		wantContains(t, "detail", c.Detail, "stored earlier", "no newer")
	})
	t.Run("fetched", func(t *testing.T) {
		newMachine(t)
		fetchedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		fetched := false
		c := find(t, run(t, Options{
			FetchPrices:   true,
			RefreshPrices: func(context.Context) error { fetched = true; return nil },
			PriceSource: func() pricing.Source {
				return pricing.Source{Origin: pricing.OriginFetched, Version: "2026-10-01", FetchedAt: fetchedAt}
			},
		}), "prices")
		if !fetched {
			t.Error("did not fetch")
		}
		wantStatus(t, c, StatusOK)
		if c.Source != "fetched" || c.Version != "2026-10-01" {
			t.Errorf("%+v", c)
		}
		wantContains(t, "detail", c.Detail, "2026-10-01T12:00:00Z")
	})
	t.Run("fetched but not newer", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{
			FetchPrices:   true,
			RefreshPrices: func(context.Context) error { return nil },
			PriceSource:   func() pricing.Source { return pricing.Source{Origin: pricing.OriginEmbedded, Version: "2026-01-01"} },
		}), "prices")
		wantStatus(t, c, StatusOK)
		wantContains(t, "detail", c.Detail, "no newer")
	})
	t.Run("fetch failed", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{
			FetchPrices:   true,
			RefreshPrices: func(context.Context) error { return errors.New("pricing download unavailable") },
			PriceSource:   func() pricing.Source { return pricing.Source{Origin: pricing.OriginEmbedded, Version: "2026-01-01"} },
		}), "prices")
		wantStatus(t, c, StatusWarning)
		wantContains(t, "detail", c.Detail, "pricing download unavailable", "embedded")
		wantContains(t, "fix", c.Fix, "network")
	})
	t.Run("a recorded earlier failure is reported without fetching", func(t *testing.T) {
		newMachine(t)
		c := find(t, run(t, Options{
			PriceSource: func() pricing.Source {
				return pricing.Source{Origin: pricing.OriginEmbedded, Version: "2026-01-01", RefreshError: "price list signature is not trusted"}
			},
		}), "prices")
		wantStatus(t, c, StatusWarning)
		wantContains(t, "detail", c.Detail, "signature is not trusted")
	})
}

func TestConfigErrorIsAProblemAndTheReportContinues(t *testing.T) {
	m := newMachine(t)
	m.git()
	cfgPath := m.writeConfig("[backend_paths]\nclaude = 'relative/claude'\n")
	report := run(t, Options{})
	c := find(t, report, "config")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, "backend_paths.claude", "absolute path")
	wantContains(t, "fix", c.Fix, cfgPath)
	if c.Path != cfgPath {
		t.Errorf("path = %q", c.Path)
	}
	wantStatus(t, find(t, report, "git"), StatusOK)
	find(t, report, "backend-auto")
}

func TestDoctorCreatesNothing(t *testing.T) {
	m := newMachine(t)
	m.git()
	run(t, Options{InstanceDir: func() (string, error) { return filepath.Join(os.Getenv("XDG_STATE_HOME"), "baum"), nil }})
	home := os.Getenv("HOME")
	var created []string
	_ = filepath.WalkDir(home, func(path string, _ os.DirEntry, _ error) error {
		if path != home {
			created = append(created, path)
		}
		return nil
	})
	if len(created) != 0 {
		t.Errorf("doctor created %v", created)
	}
	registry := find(t, run(t, Options{}), "registry")
	wantContains(t, "registry", registry.Detail, "not created yet")
	wantStatus(t, registry, StatusInfo)
}

func TestLocations(t *testing.T) {
	m := newMachine(t)
	m.git()
	cfgPath := m.writeConfig("backend = 'auto'\n")
	report := run(t, Options{InstanceDir: func() (string, error) { return "/run/user/1/baum", nil }})
	cfg := find(t, report, "config")
	if cfg.Path != cfgPath || cfg.Status != StatusInfo || !strings.Contains(cfg.Detail, "read from") {
		t.Errorf("%+v", cfg)
	}
	home := os.Getenv("HOME")
	if got := find(t, report, "jobs").Path; got != filepath.Join(home, "state", "baum", "jobs.json") {
		t.Errorf("jobs path = %q", got)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, report, "discovery").Path; got != filepath.Join(cacheDir, "baum", "discovery") {
		t.Errorf("discovery path = %q", got)
	}
	if got := find(t, report, "registry").Path; got != filepath.Join(m.config, "baum", "repos.sqlite") {
		t.Errorf("registry path = %q", got)
	}
	if got := find(t, report, "instance").Path; got != "/run/user/1/baum" {
		t.Errorf("instance path = %q", got)
	}
}

func TestPathEntries(t *testing.T) {
	m := newMachine(t)
	m.git()
	c := find(t, run(t, Options{}), "path")
	wantStatus(t, c, StatusInfo)
	wantContains(t, "detail", c.Detail, "1 entry", m.bin)

	c = find(t, run(t, Options{Getenv: func(name string) string {
		if name == "PATH" {
			return ""
		}
		return os.Getenv(name)
	}}), "path")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "fix", c.Fix, "backend_paths")
}

func TestOverallStatus(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	t.Setenv("GITHUB_TOKEN", plantedEnvToken)
	report := run(t, Options{})
	if report.Problems() != 0 || report.Status != StatusWarning {
		t.Errorf("status = %s with %d problems and %d warnings (gh and codex are missing)", report.Status, report.Problems(), report.Warnings())
	}
}

func TestWriteTextShowsMarkerDetailAndFix(t *testing.T) {
	var out bytes.Buffer
	err := WriteText(&out, Report{Status: StatusProblem, Checks: []Check{
		{ID: "a", Label: "alpha", Status: StatusOK, Detail: "fine", Path: "/bin/a", Version: "1.2.3", Source: "PATH"},
		{ID: "b", Label: "beta", Status: StatusProblem, Detail: "broken\nsecond line", Fix: "do the thing"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, "text", out.String(), "[ok]", "alpha", "path: /bin/a; version: 1.2.3; source: PATH", "[problem]", "    second line", "fix: do the thing", "problem: 1 problem, 0 warnings")
}

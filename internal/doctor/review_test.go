package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
)

// recordEnv is a fake tool body that writes its environment to file and then
// prints out.
func recordEnv(file, out string) string {
	return `/usr/bin/env > '` + file + `'
echo "` + out + `"`
}

func TestToolsDoNotReceiveSecretsFromTheEnvironment(t *testing.T) {
	m := newMachine(t)
	secrets := map[string]string{
		"ANTHROPIC_API_KEY":   plantedAnthropicAK,
		"GITHUB_TOKEN":        plantedEnvToken,
		"GH_TOKEN":            plantedGHToken,
		"OPENAI_API_KEY":      "sk-PLANTEDopenai00000000000000000000000",
		"CODEX_API_KEY":       "PLANTEDcodex000000000000000000000000000",
		"CURSOR_API_KEY":      "PLANTEDcursor00000000000000000000000000",
		"AWS_SECRET_KEY":      "PLANTEDaws0000000000000000000000000000",
		"SSH_AUTH_SOCK":       "/tmp/PLANTED-agent.sock",
		"GIT_ASKPASS":         "/tmp/PLANTED-askpass",
		"GH_ENTERPRISE_TOKEN": "PLANTEDentoken0000000000000000000000",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	dir := t.TempDir()
	envFile := func(name string) string { return filepath.Join(dir, name+".env") }
	m.install("git", recordEnv(envFile("git"), "git version 2.43.0"))
	m.install("gh", recordEnv(envFile("gh"), "gh version 2.40.1"))
	m.install("claude", recordEnv(envFile("claude"), "2.1.7 (Claude Code)"))
	m.install("cursor-agent", recordEnv(envFile("cursor-agent"), "2025.01.01"))
	// A codex that records its environment on every call.
	m.install("codex", `/usr/bin/env >> '`+envFile("codex")+`'
case "$1" in
--version) echo "codex-cli 0.162.1";;
sandbox) shift 4; case "$1" in cat) /bin/cat "$2";; sh) echo baum-write-blocked;; esac;;
esac`)
	run(t, Options{})

	for _, tool := range []string{"git", "gh", "claude", "cursor-agent", "codex"} {
		data, err := os.ReadFile(envFile(tool))
		if err != nil {
			t.Fatalf("%s did not run: %v", tool, err)
		}
		for name, value := range secrets {
			if strings.Contains(string(data), name+"=") || strings.Contains(string(data), value) {
				t.Errorf("%s received %s", tool, name)
			}
		}
	}
}

func TestAFailingBackendCheckDoesNotRunTheToolAgain(t *testing.T) {
	m := newMachine(t)
	m.git()
	count := filepath.Join(t.TempDir(), "count")
	m.install("claude", `echo run >> '`+count+`'
echo "1.0.0 (Claude Code)"`)
	c := find(t, run(t, Options{}), "claude")
	wantStatus(t, c, StatusWarning)
	if c.Version != "1.0.0" {
		t.Errorf("version = %q, want it from the check's own message", c.Version)
	}
	data, _ := os.ReadFile(count)
	if n := strings.Count(string(data), "run"); n != 1 {
		t.Errorf("claude ran %d times for one failing check, want 1", n)
	}
}

func TestAnUnknownEnterpriseHostIsAProblem(t *testing.T) {
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
	report := run(t, Options{Registry: func(context.Context, string) ([]repos.Clone, error) {
		return []repos.Clone{{ID: "b", Repository: "ghe.example.com/acme/ent"}, {ID: "t", Repository: "tenant.ghe.com/acme/app"}}, nil
	}})
	unknown := find(t, report, "github-token-ghe.example.com")
	wantStatus(t, unknown, StatusProblem)
	wantContains(t, "fix", unknown.Fix, "set GH_HOST=ghe.example.com or run `gh auth login --hostname ghe.example.com`")
	if unknown.Source != "" {
		t.Errorf("a refused host has a source: %+v", unknown)
	}
	// A *.ghe.com tenant is always known; its token comes from the environment.
	tenant := find(t, report, "github-token-tenant.ghe.com")
	if tenant.Status == StatusProblem && strings.Contains(tenant.Detail, "will not read") {
		t.Errorf("a ghe.com tenant was refused: %+v", tenant)
	}
}

func TestAConfigThatDoesNotLoadSaysItsTokenWasNotRead(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.writeConfig("github_token = '" + plantedFileToken + "'\n[backend_paths]\nclaude = 'relative'\n")
	report := run(t, Options{})
	c := find(t, report, "github-token-github.com")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "detail", c.Detail, "config file did not load", "github_token was not read")
	cfgCheck := find(t, report, "config")
	wantContains(t, "config fix", cfgCheck.Fix, "default settings")
	if strings.Contains(cfgCheck.Fix, "use its settings") {
		t.Errorf("config fix = %q", cfgCheck.Fix)
	}
	if strings.Contains(c.Fix, "add github_token") {
		t.Errorf("fix tells the user to add a github_token that is already there: %q", c.Fix)
	}
	wantContains(t, "fix", c.Fix, "config check", "GITHUB_TOKEN", "gh auth login")

	// A token from the environment is still found, and the note stays.
	t.Setenv("GITHUB_TOKEN", plantedEnvToken)
	c = find(t, run(t, Options{}), "github-token-github.com")
	wantStatus(t, c, StatusOK)
	wantContains(t, "detail", c.Detail, "did not load")
	if c.Source != "environment variable GITHUB_TOKEN" {
		t.Errorf("source = %q", c.Source)
	}
}

func TestAnIntegerDurationInTheConfigIsReportedByKeyAndPosition(t *testing.T) {
	m := newMachine(t)
	m.git()
	cfgPath := m.writeConfig("[budgets]\nrun_timeout = 10\n")
	c := find(t, run(t, Options{}), "config")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, cfgPath, "budgets.run_timeout", "line 2, column", `write it like "10m"`)
}

func TestAnUnquotedTokenIsNeverEchoedByAParseError(t *testing.T) {
	m := newMachine(t)
	m.git()
	cfgPath := m.writeConfig("backend = 'auto'\ngithub_token = ghp_PLANTEDunquoted0000000000\n")
	report := run(t, Options{})
	c := find(t, report, "config")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, cfgPath, "line 2", "column")
	var text bytes.Buffer
	if err := WriteText(&text, report); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report)
	for _, fragment := range []string{"ghp_", "PLANTED", "unquoted", "ghp"} {
		if strings.Contains(text.String(), fragment) || strings.Contains(string(data), fragment) {
			t.Errorf("the report holds a piece (%q) of the token", fragment)
		}
	}
}

func TestModelCaveatAppearsWhenAutoFallsBackToCodex(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.codex("0.162.1")
	t.Setenv("BAUM_MODEL", "claude-sonnet-4-5")
	auto := find(t, run(t, Options{}), "backend-auto")
	wantStatus(t, auto, StatusOK)
	wantContains(t, "auto", auto.Detail, "picks codex", "claude skipped",
		`the model "claude-sonnet-4-5" was meant for claude; codex uses its own default model`)
}

func TestCodexTooOld(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.codex("0.100.0")
	c := find(t, run(t, Options{}), "codex")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "detail", c.Detail, "older than")
	wantContains(t, "fix", c.Fix, "upgrade Codex")
	if c.Version != "0.100.0" {
		t.Errorf("version = %q", c.Version)
	}
}

func TestExplicitCodexPathPointingAtAMissingFile(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.codex("0.162.1") // on PATH, and must not be used instead
	missing := filepath.Join(t.TempDir(), "no-such-codex")
	cfgPath := m.writeConfig("[backend_paths]\ncodex = '" + missing + "'\n")
	report := run(t, Options{})
	c := find(t, report, "codex")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, "backend_paths.codex", missing)
	wantContains(t, "fix", c.Fix, "backend_paths.codex", cfgPath)
	if c.Path != "" {
		t.Errorf("path = %q", c.Path)
	}
	wantStatus(t, find(t, report, "backend-auto"), StatusProblem)
}

func TestConfiguredBackendThatIsNotAvailableHasADirectFix(t *testing.T) {
	for backend, wants := range map[string][]string{
		"anthropic": {"export ANTHROPIC_API_KEY", `backend = "auto", "claude", "codex" or "cursor-agent"`, "config.toml"},
	} {
		t.Run(backend, func(t *testing.T) {
			m := newMachine(t)
			m.git()
			t.Setenv("BAUM_BACKEND", backend)
			c := find(t, run(t, Options{}), "backend-configured")
			wantStatus(t, c, StatusProblem)
			wantContains(t, "fix", c.Fix, wants...)
			if strings.Contains(c.Fix, "check above") {
				t.Errorf("fix points at a check that does not exist: %q", c.Fix)
			}
		})
	}
}

func TestPriceDetailSaysWhoUsesWhichList(t *testing.T) {
	newMachine(t)
	c := find(t, run(t, Options{
		FetchPrices:   true,
		RefreshPrices: func(context.Context) error { return nil },
		PriceSource:   func() pricing.Source { return pricing.Source{Origin: pricing.OriginFetched, Version: "2099-01-01"} },
	}), "prices")
	wantContains(t, "detail", c.Detail, "baum internal generate", "embedded list (version "+pricing.Embedded().Version()+")", "the baum daemon fetches", "only shows whether fetching works", "2099-01-01")
}

func TestRealRegistryListsClonesAndTheirStores(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	m := newMachine(t)
	// The real git registers and lists the clones, and the report runs it too.
	t.Setenv("PATH", filepath.Dir(gitPath)+string(os.PathListSeparator)+m.bin)
	sh := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	makeClone := func(remote string) string {
		dir := t.TempDir()
		sh(dir, "init")
		sh(dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "fixture")
		sh(dir, "remote", "add", "origin", remote)
		return dir
	}
	available, gone := makeClone("https://github.com/acme/app"), makeClone("https://github.com/acme/other")
	registryPath, _ := repos.Path()
	registry, err := repos.Open(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := registry.Add(context.Background(), available, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := registry.Add(context.Background(), gone, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = registry.Close()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(registryPath)
	names, _ := os.ReadDir(filepath.Dir(registryPath))

	report := run(t, Options{})

	reg := find(t, report, "registry")
	wantStatus(t, reg, StatusInfo)
	wantContains(t, "registry", reg.Detail, "2 registered clones")
	store := find(t, report, "store-"+a.ID)
	wantStatus(t, store, StatusInfo)
	// the store's root is under the clone's common git directory, and not created
	if !strings.HasSuffix(store.Path, filepath.Join(".git", "baum", "reviews")) {
		t.Errorf("store path = %q", store.Path)
	}
	if _, err := os.Stat(store.Path); err == nil {
		t.Errorf("doctor created the store at %s", store.Path)
	}
	wantContains(t, "store", store.Detail, "no versions stored yet")
	missing := find(t, report, "store-"+g.ID)
	wantStatus(t, missing, StatusWarning)
	wantContains(t, "fix", missing.Fix, gone, "baum internal repos remove "+g.ID)

	after, _ := os.ReadFile(registryPath)
	afterNames, _ := os.ReadDir(filepath.Dir(registryPath))
	if !bytes.Equal(before, after) || len(names) != len(afterNames) {
		t.Error("doctor changed the registry or its directory")
	}
}

func TestAnUnreadableRegistryIsReportedClearly(t *testing.T) {
	m := newMachine(t)
	m.git()
	registryPath, _ := repos.Path()
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, bytes.Repeat([]byte("not a database "), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	c := find(t, run(t, Options{}), "registry")
	wantStatus(t, c, StatusWarning)
	wantContains(t, "detail", c.Detail, "cannot read the registry")
	wantContains(t, "fix", c.Fix, registryPath, "baum internal repos list")
}

func TestAHungVersionCheckIsBoundedAndItsChildrenDie(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	m := newMachine(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	m.install("git", `sleep 300 &
echo $! > '`+pidFile+`'
wait`)
	t.Setenv("PATH", m.bin+string(os.PathListSeparator)+filepath.Dir(sleepPath))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := runCommand(ctx, os.Environ(), filepath.Join(m.bin, "git"), "--version")
	_ = out
	if err == nil {
		t.Fatal("the hung tool succeeded")
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Skip("the child never started")
	}
	pid := strings.TrimSpace(string(data))
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc to look at the child in")
	}
	// A dead child may linger as a zombie until its new parent reaps it.
	// Read errors during exit can be transient; retry them, and do not read
	// again after observing that the child is gone or a zombie.
	deadline := time.Now().Add(3 * time.Second)
	for {
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		if os.IsNotExist(err) || (err == nil && strings.Contains(string(stat), ") Z")) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child %s outlived its tool: stat = %q, err = %v", pid, stat, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAKeyringThatWaitsForAPromptGetsATailoredFix(t *testing.T) {
	m := newMachine(t)
	m.git()
	c := find(t, run(t, Options{GitHubToken: func(context.Context, config.Config, string) (string, string, error) {
		return "", "", fmt.Errorf("asking gh for the token of github.com: %w", context.DeadlineExceeded)
	}}), "github-token-github.com")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "detail", c.Detail, "timed out after 30s")
	wantContains(t, "fix", c.Fix, "unlock", "allow", "run `gh auth token` once", "GITHUB_TOKEN")
	if tokenTimeout != 30*time.Second {
		t.Errorf("the keyring step is bounded at %v, want 30s", tokenTimeout)
	}
}

func TestGitKeepsDeveloperDirButNoSecrets(t *testing.T) {
	m := newMachine(t)
	file := filepath.Join(t.TempDir(), "git.env")
	m.install("git", recordEnv(file, "git version 2.43.0"))
	t.Setenv("DEVELOPER_DIR", "/Applications/Fixture.app/Contents/Developer")
	t.Setenv("GITHUB_TOKEN", plantedEnvToken)
	run(t, Options{})
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, "git's environment", string(data), "DEVELOPER_DIR=/Applications/Fixture.app/Contents/Developer")
	if strings.Contains(string(data), plantedEnvToken) {
		t.Error("git received the token")
	}
}

func TestAValidFileIsReadWhenAnEnvironmentVariableBreaksTheLoad(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.codex("0.162.1")
	m.writeConfig("github_token = '" + plantedFileToken + "'\n")
	t.Setenv("BAUM_BACKEND", "gpt")
	t.Setenv("BAUM_MODEL", "claude-sonnet-4-5")
	report := run(t, Options{})

	cfg := find(t, report, "config")
	wantStatus(t, cfg, StatusProblem)
	wantContains(t, "config detail", cfg.Detail, "unknown backend", "gpt")
	wantContains(t, "config fix", cfg.Fix, "BAUM_BACKEND", "the file itself parses", "use its settings")
	if strings.Contains(cfg.Fix, "default settings") {
		t.Errorf("fix says defaults are used although the file was read: %q", cfg.Fix)
	}
	// BAUM_MODEL applies as config.Load would have applied it.
	wantContains(t, "auto", find(t, report, "backend-auto").Detail, "picks codex",
		`the model "claude-sonnet-4-5" was meant for claude; codex uses its own default model`)

	// The file parses, so its token counts and nothing claims it was unread.
	token := find(t, report, "github-token-github.com")
	wantStatus(t, token, StatusOK)
	if token.Source != "config file" || strings.Contains(token.Detail, "did not load") || strings.Contains(token.Detail, "not read") {
		t.Errorf("%+v", token)
	}

	// An environment token still takes precedence over the file's.
	t.Setenv("GITHUB_TOKEN", plantedEnvToken)
	token = find(t, run(t, Options{}), "github-token-github.com")
	if token.Source != "environment variable GITHUB_TOKEN" {
		t.Errorf("source = %q", token.Source)
	}
}

func TestAnUnknownBackendNameInTheFileGetsADirectFix(t *testing.T) {
	m := newMachine(t)
	m.git()
	cfgPath := m.writeConfig("backend = 'bogus'\n")
	report := run(t, Options{})
	c := find(t, report, "backend-configured")
	wantStatus(t, c, StatusProblem)
	wantContains(t, "fix", c.Fix, "unknown backend bogus", `set backend = "auto", "claude", "codex", "cursor-agent" or "anthropic"`, cfgPath)
	if strings.Contains(c.Fix, "check above") || strings.Contains(c.Detail, "cannot run") {
		t.Errorf("an unknown name was treated as a backend that failed a check: %+v", c)
	}
	wantStatus(t, find(t, report, "config"), StatusProblem)
}

func TestNoHomeMeansNoConfigFileIsRead(t *testing.T) {
	m := newMachine(t)
	m.git()
	stray := t.TempDir()
	if err := os.WriteFile(filepath.Join(stray, "config.toml"), []byte("backend = 'codex'\ngithub_token = '"+plantedFileToken+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(stray); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	_ = m

	report := run(t, Options{})
	cfg := find(t, report, "config")
	wantStatus(t, cfg, StatusProblem)
	wantContains(t, "fix", cfg.Fix, "HOME", "XDG_CONFIG_HOME")
	if cfg.Path != "" {
		t.Errorf("path = %q, want none", cfg.Path)
	}
	for _, c := range report.Checks {
		if c.ID == "backend-configured" {
			t.Errorf("the stray file's backend was applied: %+v", c)
		}
	}
	token := find(t, report, "github-token-github.com")
	if token.Source == "config file" {
		t.Errorf("the stray file's token was used: %+v", token)
	}
}

func TestFileBackendIsNormalisedWhenOnlyTheEnvironmentBreaksTheLoad(t *testing.T) {
	m := newMachine(t)
	m.git()
	m.claude("2.1.7")
	m.writeConfig("backend = 'Claude'\n")
	t.Setenv("BAUM_BACKEND", "gpt")
	report := run(t, Options{})
	cfg := find(t, report, "config")
	wantStatus(t, cfg, StatusProblem)
	wantContains(t, "config", cfg.Detail+cfg.Fix, "BAUM_BACKEND")
	configured := find(t, report, "backend-configured")
	if configured.Label != "backend: claude" || strings.Contains(configured.Detail+configured.Fix, "not a known backend") {
		t.Errorf("%+v", configured)
	}
	wantStatus(t, configured, StatusOK)
}

package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func setTempConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestPathHonoursXDGConfigHome(t *testing.T) {
	dir := setTempConfigHome(t)
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "baum", "config.toml"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestLoadWithoutFileUsesDefaults(t *testing.T) {
	setTempConfigHome(t)
	cfg, err := load(Overrides{}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != BackendAuto || cfg.Budgets != Default().Budgets || cfg.Model != "" {
		t.Errorf("cfg = %+v, want defaults", cfg)
	}
}

func TestPrecedence(t *testing.T) {
	setTempConfigHome(t)
	path, _ := Path()
	fileCfg := Default()
	fileCfg.Backend = BackendCodex
	fileCfg.Model = "file-model"
	fileCfg.GitHubToken = "ghp_from_file"
	fileCfg.Budgets.MaxCalls = 7
	fileCfg.Caps = map[string]int{"sessions": 3}
	if err := Save(path, fileCfg); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		overrides Overrides
		env       map[string]string
		want      func(*Config)
	}{
		{
			name: "file over defaults",
			want: func(c *Config) {
				c.Backend = BackendCodex
				c.Model = "file-model"
				c.GitHubToken = "ghp_from_file"
				c.Budgets.MaxCalls = 7
				c.Caps = map[string]int{"sessions": 3}
			},
		},
		{
			name: "env over file",
			env: map[string]string{
				EnvBackend:         "claude",
				EnvModel:           "env-model",
				EnvGitHubToken:     "ghp_from_env",
				EnvAnthropicAPIKey: "sk-ant-test",
			},
			want: func(c *Config) {
				c.Backend = BackendClaude
				c.Model = "env-model"
				c.GitHubToken = "ghp_from_env"
				c.AnthropicAPIKey = "sk-ant-test"
				c.Budgets.MaxCalls = 7
				c.Caps = map[string]int{"sessions": 3}
			},
		},
		{
			name: "GH_TOKEN when GITHUB_TOKEN is unset",
			env:  map[string]string{EnvGHToken: "gho_from_gh_env"},
			want: func(c *Config) {
				c.Backend = BackendCodex
				c.Model = "file-model"
				c.GitHubToken = "gho_from_gh_env"
				c.Budgets.MaxCalls = 7
				c.Caps = map[string]int{"sessions": 3}
			},
		},
		{
			name:      "flags over env",
			overrides: Overrides{Backend: "Anthropic", Model: "flag-model"},
			env:       map[string]string{EnvBackend: "claude", EnvModel: "env-model"},
			want: func(c *Config) {
				c.Backend = BackendAnthropic
				c.Model = "flag-model"
				c.GitHubToken = "ghp_from_file"
				c.Budgets.MaxCalls = 7
				c.Caps = map[string]int{"sessions": 3}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(tt.overrides, env(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			want := Default()
			tt.want(&want)
			if got.Backend != want.Backend || got.Model != want.Model || got.GitHubToken != want.GitHubToken ||
				got.AnthropicAPIKey != want.AnthropicAPIKey || got.Budgets != want.Budgets ||
				len(got.Caps) != len(want.Caps) || got.Caps["sessions"] != want.Caps["sessions"] {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestLoadRejectsUnknownBackend(t *testing.T) {
	setTempConfigHome(t)
	if _, err := load(Overrides{}, env(map[string]string{EnvBackend: "gpt"})); err == nil {
		t.Fatal("unknown backend from env must fail")
	}
	if _, err := load(Overrides{Backend: "nope"}, env(nil)); err == nil {
		t.Fatal("unknown backend from flag must fail")
	}
}

func TestLoadWithExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.toml")
	if err := os.WriteFile(path, []byte("backend = \"cursor-agent\"\n[budgets]\npass_timeout = \"3m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(Overrides{Path: path}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != BackendCursorAgent || cfg.Budgets.PassTimeout != Duration(3*time.Minute) {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Budgets.RunTimeout != Default().Budgets.RunTimeout {
		t.Errorf("unset budget lost its default: %v", cfg.Budgets.RunTimeout)
	}
}

func TestSaveCreatesUserOnlyFile(t *testing.T) {
	dir := setTempConfigHome(t)
	path, _ := Path()
	cfg := Default()
	cfg.GitHubToken = "ghp_secret"
	cfg.AnthropicAPIKey = "sk-ant-must-not-persist"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	// Windows has no POSIX permission bits; Stat reports 0666/0777 there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600", perm)
		}
		dirInfo, err := os.Stat(filepath.Join(dir, "baum"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("dir mode = %o, want 700", perm)
		}
	}

	data, _ := os.ReadFile(path)
	if len(data) == 0 || bytes.Contains(data, []byte("sk-ant-must-not-persist")) {
		t.Errorf("file content %q must hold the config but never the API key", data)
	}

	got, ok, err := ReadFile(path)
	if err != nil || !ok {
		t.Fatalf("read back: ok = %v, err = %v", ok, err)
	}
	if got.GitHubToken != "ghp_secret" || got.Budgets != cfg.Budgets {
		t.Errorf("round trip = %+v, want %+v", got, cfg)
	}
}

func TestSaveTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("backend = \"auto\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestResolveGitHubTokenFallsBackToGH(t *testing.T) {
	restore := ghAuthToken
	t.Cleanup(func() { ghAuthToken = restore })

	ghAuthToken = func(context.Context) (string, error) { return "gho_from_gh\n", nil }
	token, err := (Config{}).ResolveGitHubToken(context.Background())
	if err != nil || token != "gho_from_gh\n" {
		t.Errorf("gh fallback: token = %q, err = %v", token, err)
	}

	token, err = (Config{GitHubToken: "ghp_configured"}).ResolveGitHubToken(context.Background())
	if err != nil || token != "ghp_configured" {
		t.Errorf("configured token: %q, %v", token, err)
	}

	ghAuthToken = func(context.Context) (string, error) { return "", errors.New("not logged in") }
	if _, err := (Config{}).ResolveGitHubToken(context.Background()); !errors.Is(err, ErrNoGitHubToken) {
		t.Errorf("err = %v, want ErrNoGitHubToken", err)
	}
}

func TestDetect(t *testing.T) {
	onPath := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	tests := []struct {
		name    string
		cfg     Config
		path    []string
		want    Backend
		wantErr error
	}{
		{name: "explicit backend wins", cfg: Config{Backend: BackendCodex}, path: []string{"claude"}, want: BackendCodex},
		{name: "claude first", cfg: Config{Backend: BackendAuto}, path: []string{"cursor-agent", "codex", "claude"}, want: BackendClaude},
		{name: "codex before cursor-agent", cfg: Config{Backend: BackendAuto}, path: []string{"cursor-agent", "codex"}, want: BackendCodex},
		{name: "cursor-agent alone", cfg: Config{Backend: BackendAuto}, path: []string{"cursor-agent"}, want: BackendCursorAgent},
		{name: "api key fallback", cfg: Config{Backend: BackendAuto, AnthropicAPIKey: "sk"}, want: BackendAnthropic},
		{name: "nothing available", cfg: Config{Backend: BackendAuto}, wantErr: ErrNoBackend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cfg.detect(onPath(tt.path...))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("backend = %q, want %q", got, tt.want)
			}
		})
	}
}

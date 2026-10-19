package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
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

func TestResolveGitHubTokenKeepsBaumSourcesForGitHubCom(t *testing.T) {
	restore := ghToken
	t.Cleanup(func() { ghToken = restore })
	var asked []string
	ghToken = func(_ context.Context, host string) (string, string, error) {
		asked = append(asked, host)
		return map[string]string{"github.com": "gho_from_gh\n", "ghe.example.com": "ghe_from_gh"}[host], "stand-in", nil
	}
	ctx := context.Background()

	if token, err := (Config{GitHubToken: "ghp_configured"}).ResolveGitHubToken(ctx, "github.com"); err != nil || token != "ghp_configured" {
		t.Errorf("configured token: %q, %v", token, err)
	}
	if len(asked) != 0 {
		t.Errorf("gh was asked for %v although baum had a token", asked)
	}
	if token, err := (Config{}).ResolveGitHubToken(ctx, "API.GitHub.com"); err != nil || token != "gho_from_gh" {
		t.Errorf("gh fallback: token = %q, err = %v", token, err)
	}
	// baum's token belongs to github.com: another host gets only its own.
	if token, err := (Config{GitHubToken: "ghp_configured"}).ResolveGitHubToken(ctx, "ghe.example.com"); err != nil || token != "ghe_from_gh" {
		t.Errorf("Enterprise host: token = %q, err = %v", token, err)
	}
	if _, err := (Config{GitHubToken: "ghp_configured"}).ResolveGitHubToken(ctx, "other.example.com"); !errors.Is(err, ErrNoGitHubToken) {
		t.Errorf("unknown host: err = %v, want ErrNoGitHubToken", err)
	}
	if want := []string{"github.com", "ghe.example.com", "other.example.com"}; !slices.Equal(asked, want) {
		t.Errorf("gh asked for %v, want %v", asked, want)
	}
	if _, err := (Config{}).ResolveGitHubToken(ctx, ""); !errors.Is(err, ErrNoGitHubToken) {
		t.Errorf("no host: err = %v, want ErrNoGitHubToken", err)
	}
}

// standInGH puts a gh on PATH that prints token only for exactly wantArgs,
// and isolates gh's configuration and environment tokens.
func standInGH(t *testing.T, wantArgs, token string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in gh is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$*\" = \"" + wantArgs + "\" ] || { echo \"unexpected arguments: $*\" >&2; exit 2; }\n" +
		"echo " + token + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("GH_PATH", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(name, "")
	}
}

// TestGHTokenIsAskedForTheRepositoryHost runs a stand-in gh that prints a
// token only when asked for the requested host's, as GH_HOST pointing
// elsewhere would otherwise switch the account.
func TestGHTokenIsAskedForTheRepositoryHost(t *testing.T) {
	standInGH(t, "auth token --secure-storage --hostname github.com", "gho_github_com")
	t.Setenv("GH_HOST", "github.example.com")
	token, err := (Config{}).ResolveGitHubToken(context.Background(), "github.com")
	if err != nil || token != "gho_github_com" {
		t.Fatalf("token = %q, err = %v; want github.com's token", token, err)
	}
}

func TestGHTokenForAnEnterpriseHost(t *testing.T) {
	standInGH(t, "auth token --secure-storage --hostname ghe.example.com", "ghe_from_keyring")
	ctx := context.Background()
	if token, err := (Config{GitHubToken: "ghp_github_com"}).ResolveGitHubToken(ctx, "ghe.example.com"); err != nil || token != "ghe_from_keyring" {
		t.Fatalf("token = %q, err = %v; want the Enterprise host's", token, err)
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "ghe_from_env")
	t.Setenv("GITHUB_TOKEN", "ghp_github_com")
	if token, err := (Config{}).ResolveGitHubToken(ctx, "ghe.example.com"); err != nil || token != "ghe_from_env" {
		t.Fatalf("token = %q, err = %v; want GH_ENTERPRISE_TOKEN", token, err)
	}
}

// TestGHKeyringLookupStopsWhenCancelled runs a stand-in gh that waits, as a
// locked keyring would, and checks cancelling the context ends the lookup
// promptly with the context's error.
func TestGHKeyringLookupStopsWhenCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in gh is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+filepath.Dir(sleep))
	t.Setenv("GH_PATH", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(name, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = (Config{}).ResolveGitHubToken(ctx, "github.com")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the lookup took %v after cancellation", elapsed)
	}
}

func TestDurationFieldsOnlyTakeStrings(t *testing.T) {
	const hint = `write it like "10m"`
	cases := []struct {
		name    string
		toml    string
		field   func(Budgets) Duration
		want    time.Duration
		wantErr []string
	}{
		{"pass string", "[budgets]\npass_timeout = \"3m\"\n", func(b Budgets) Duration { return b.PassTimeout }, 3 * time.Minute, nil},
		{"pass integer", "[budgets]\npass_timeout = 10\n", nil, 0, []string{"wrong type or value for budgets.pass_timeout at line 2, column", hint}},
		{"pass float", "[budgets]\npass_timeout = 1.5\n", nil, 0, []string{"wrong type or value for budgets.pass_timeout at line 2, column", hint}},
		{"run string", "[budgets]\nrun_timeout = \"3m\"\n", func(b Budgets) Duration { return b.RunTimeout }, 3 * time.Minute, nil},
		{"run integer", "[budgets]\nrun_timeout = 10\n", nil, 0, []string{"wrong type or value for budgets.run_timeout at line 2, column", hint}},
		{"inline table integer", "budgets = { run_timeout = 10 }\n", nil, 0, []string{"budgets.run_timeout at line 1, column", hint}},
		{"dotted key integer", "budgets.pass_timeout = 10\n", nil, 0, []string{"budgets.pass_timeout at line 1, column", hint}},
		{"run float", "[budgets]\nrun_timeout = 1.5\n", nil, 0, []string{"wrong type or value for budgets.run_timeout at line 2, column", hint}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.toml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := ReadFile(path)
			t.Logf("err=%v pass=%v run=%v", err, time.Duration(cfg.Budgets.PassTimeout), time.Duration(cfg.Budgets.RunTimeout))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatal(err)
				}
				if got := time.Duration(tc.field(cfg.Budgets)); got != tc.want {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, s := range tc.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q lacks %q", err, s)
				}
			}
		})
	}
}

func TestDurationKeysCoverEveryDurationField(t *testing.T) {
	var found []string
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("toml"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			switch {
			case field.Type == reflect.TypeOf(Duration(0)):
				found = append(found, prefix+name)
			case field.Type.Kind() == reflect.Struct:
				walk(prefix+name+".", field.Type)
			}
		}
	}
	walk("", reflect.TypeOf(Config{}))
	slices.Sort(found)
	want := slices.Clone(durationKeys)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("Duration fields %v, durationKeys %v", found, want)
	}
}

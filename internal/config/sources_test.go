package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBackendPathsRoundTrip(t *testing.T) {
	setTempConfigHome(t)
	path, _ := Path()
	claudePath := filepath.Join(t.TempDir(), "claude")
	cfg := Default()
	cfg.BackendPaths = map[Backend]string{BackendClaude: claudePath}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := load(Overrides{}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.BackendPaths[BackendClaude]; got != claudePath || len(loaded.BackendPaths) != 1 {
		t.Errorf("BackendPaths = %v", loaded.BackendPaths)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("[backend_paths]")) {
		t.Errorf("config file lacks [backend_paths]:\n%s", data)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if data, _ = os.ReadFile(path); bytes.Contains(data, []byte("backend_paths")) {
		t.Errorf("empty BackendPaths must be omitted:\n%s", data)
	}
}

func TestLoadRejectsBadBackendPaths(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "bin")
	tests := []struct {
		name, body string
		want       []string
	}{
		{"unknown key", "[backend_paths]\ngpt = '" + abs + "'\n", []string{"backend_paths.gpt", "claude, codex or cursor-agent"}},
		{"anthropic has no executable", "[backend_paths]\nanthropic = '" + abs + "'\n", []string{"backend_paths.anthropic"}},
		{"relative path", "[backend_paths]\nclaude = 'bin/claude'\n", []string{"backend_paths.claude", "absolute path", "bin/claude"}},
		{"empty path", "[backend_paths]\ncodex = ''\n", []string{"backend_paths.codex", "absolute path"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := load(Overrides{Path: path}, env(nil))
			if err == nil {
				t.Fatal("load succeeded")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestGitHubTokenSourceIsTracked(t *testing.T) {
	setTempConfigHome(t)
	path, _ := Path()
	fileCfg := Default()
	fileCfg.GitHubToken = "ghp_from_file"
	if err := Save(path, fileCfg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tt := range []struct {
		name   string
		env    map[string]string
		source string
	}{
		{"config file", nil, "config file"},
		{"GITHUB_TOKEN", map[string]string{EnvGitHubToken: "ghp_env", EnvGHToken: "ghp_other"}, "environment variable GITHUB_TOKEN"},
		{"GH_TOKEN", map[string]string{EnvGHToken: "ghp_env"}, "environment variable GH_TOKEN"},
	} {
		cfg, err := load(Overrides{}, env(tt.env))
		if err != nil {
			t.Fatal(err)
		}
		token, source, err := cfg.ResolveGitHubTokenSource(ctx, "github.com")
		if err != nil || source != tt.source || token == "" {
			t.Errorf("%s: source = %q, err = %v", tt.name, source, err)
		}
	}
}

func TestResolveGitHubTokenSourceFromGH(t *testing.T) {
	ctx := context.Background()
	t.Run("gh environment variable", func(t *testing.T) {
		standInGH(t, "unused", "unused")
		t.Setenv("GH_ENTERPRISE_TOKEN", "ghe_env")
		_, source, err := (Config{}).ResolveGitHubTokenSource(ctx, "ghe.example.com")
		if err != nil || source != "environment variable GH_ENTERPRISE_TOKEN" {
			t.Errorf("source = %q, err = %v", source, err)
		}
	})
	t.Run("gh hosts.yml", func(t *testing.T) {
		// go-gh reads its configuration once per process, so a fresh process
		// reads this directory.
		if runtime.GOOS == "windows" {
			t.Skip("re-executing the test binary")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  oauth_token: gho_from_hosts\n  user: someone\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostsFileHelper$")
		cmd.Env = append(os.Environ(), "BAUM_HOSTS_HELPER=1", "GH_CONFIG_DIR="+dir, "GH_TOKEN=", "GITHUB_TOKEN=", "GH_PATH=", "PATH="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "source="+SourceGHHostsFile+" token=gho_from_hosts") {
			t.Errorf("helper: %v\n%s", err, out)
		}
	})
	t.Run("gh keyring", func(t *testing.T) {
		standInGH(t, "auth token --secure-storage --hostname github.com", "gho_keyring")
		_, source, err := (Config{}).ResolveGitHubTokenSource(ctx, "github.com")
		if err != nil || source != SourceGHKeyring {
			t.Errorf("source = %q, err = %v", source, err)
		}
	})
	t.Run("none", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("GH_PATH", "")
		t.Setenv("GH_CONFIG_DIR", t.TempDir())
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
			t.Setenv(name, "")
		}
		if _, _, err := (Config{}).ResolveGitHubTokenSource(ctx, "github.com"); !errors.Is(err, ErrNoGitHubToken) {
			t.Errorf("err = %v, want ErrNoGitHubToken", err)
		}
	})
}

func TestHostsFileHelper(t *testing.T) {
	if os.Getenv("BAUM_HOSTS_HELPER") == "" {
		t.Skip("run by TestResolveGitHubTokenSourceFromGH")
	}
	token, source, err := (Config{}).ResolveGitHubTokenSource(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("source=%s token=%s\n", source, token)
}

// TestParseErrorsNamePositionAndKeyNotValues: go-toml's own message quotes
// the line, which here may be an unquoted token.
func TestParseErrorsNamePositionAndKeyNotValues(t *testing.T) {
	tests := []struct {
		name, body string
		want       []string
		never      []string
	}{
		{"unquoted token", "backend = 'auto'\ngithub_token = ghp_PLANTEDunquoted0000\n",
			[]string{"invalid TOML syntax", "line 2", "column", "fix the syntax"},
			[]string{"ghp", "PLANTED", "unquoted", "github_token", "wrong type"}},
		{"text for an integer", "[budgets]\nmax_calls = \"forty\"\n",
			[]string{"wrong type or value for budgets.max_calls", "line 2", "column 13", "type int"},
			[]string{"forty", "fix the syntax"}},
		{"integer for a backend", "backend = 3\n",
			[]string{"wrong type or value for backend", "line 1", "config.Backend"},
			[]string{"fix the syntax"}},
		{"integer for a path", "[backend_paths]\nclaude = 5\n",
			[]string{"wrong type or value for backend_paths.claude", "line 2", "cannot decode TOML integer"},
			[]string{"fix the syntax"}},
		{"bad duration", "[budgets]\npass_timeout = \"10x\"\n",
			[]string{"wrong type or value for budgets.pass_timeout", "line 2", "invalid duration", `"10m"`},
			[]string{"10x", "fix the syntax"}},
		{"wrong type for the token", "github_token = [\"ghp_PLANTEDarray0000\"]\n",
			[]string{"wrong type or value for github_token", "line 1"},
			[]string{"ghp", "PLANTED", "array", "string"}},
		{"duplicate key", "backend = 'a'\nbackend = 'b'\n",
			[]string{"wrong type or value for backend", "line 2", "already defined"},
			[]string{"fix the syntax"}},
		{"unterminated table", "[budgets\n",
			[]string{"invalid TOML syntax", "line 1"},
			[]string{"wrong type"}},
	}
	// The directory is named after this test, not the case, so a case's own
	// name cannot match a leak check through the path.
	dir := t.TempDir()
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("case%d.toml", i))
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := ReadFile(path)
			if err == nil {
				t.Fatal("ReadFile accepted the file")
			}
			for _, want := range append(tt.want, path) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			for _, leak := range tt.never {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q holds %q", err, leak)
				}
			}
		})
	}
}

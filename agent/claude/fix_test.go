//go:build unix

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// fixScript is a fake Claude Code that records its arguments and environment
// in the working directory and finishes a fix. With refuseSandbox, a run
// whose --settings turn the sandbox on ends as Claude Code does when the
// sandbox cannot start.
func fixScript(t *testing.T, refuseSandbox bool) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.327 (Claude Code)'; exit 0; fi\n" +
		"n=$(ls runs 2>/dev/null | wc -l | tr -d ' '); mkdir -p runs/$n\nprintf '%s\\n' \"$@\" > runs/$n/arguments.txt\nenv > runs/$n/environment.txt\ncat > /dev/null\n"
	if refuseSandbox {
		body += "case \"$*\" in *'\"sandbox\"'*)\n" +
			"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"num_turns\":0,\"errors\":[\"Sandbox required but unavailable: bubblewrap (bwrap) not installed\"],\"startup_failure_reason\":\"sandbox_required_unavailable\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}'\n" +
			"echo 'Error: sandbox required but unavailable' >&2; exit 1;;\nesac\n"
	}
	// Claude Code 2.1.327 reports a refused command with a system notice
	// whose message is text, not an object.
	body += "printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"permission_denied\",\"tool_name\":\"Bash\",\"message\":\"A variable in this command cannot be checked\"}'\n"
	body += "printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"model\":\"fixture\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"  Resolved the conflict.  \",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func runArgs(t *testing.T, dir string, n int) ([]string, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "runs", string(rune('0'+n)), "arguments.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "runs", string(rune('0'+n)), "environment.txt"))
	return strings.Split(strings.TrimSpace(string(raw)), "\n"), string(env)
}

func flag(args []string, name string) string {
	if at := slices.Index(args, name); at >= 0 && at+1 < len(args) {
		return args[at+1]
	}
	return ""
}

type fixSettings struct {
	Permissions struct {
		BlockReads bool     `json:"blockReadsOutsideWorkingDirectories"`
		Allow      []string `json:"allow"`
	} `json:"permissions"`
	Sandbox *struct {
		Enabled           bool `json:"enabled"`
		FailIfUnavailable bool `json:"failIfUnavailable"`
		AutoAllow         bool `json:"autoAllowBashIfSandboxed"`
		AllowUnsandboxed  bool `json:"allowUnsandboxedCommands"`
		Network           *struct {
			AllowedDomains []string `json:"allowedDomains"`
		} `json:"network"`
		Filesystem struct {
			AllowWrite []string `json:"allowWrite"`
			AllowRead  []string `json:"allowRead"`
			DenyRead   []string `json:"denyRead"`
		} `json:"filesystem"`
	} `json:"sandbox"`
}

func settingsOf(t *testing.T, args []string) fixSettings {
	t.Helper()
	var settings fixSettings
	decoder := json.NewDecoder(strings.NewReader(flag(args, "--settings")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		t.Fatalf("settings %q: %v", flag(args, "--settings"), err)
	}
	return settings
}

// A fix runs Bash in Claude Code's sandbox: writes confined to the fix's
// directories, credential files unreadable, the network open or closed as
// the request says, and no token in the environment. It keeps the final
// message as its summary rather than a structured document.
func TestFixRunsCommandsInTheSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "/agent.sock")
	t.Setenv("GIT_ASKPASS", "/askpass")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("GOCACHE", "/home/user/cache")
	for _, network := range []bool{true, false} {
		dir, scratch, gitDir, clone := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"), []byte(clone+"/.git/objects\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		backend := &Backend{Binary: fixScript(t, false), Model: "sonnet", Env: []string{"NPM_TOKEN=npm_secret", "GH_TOKEN=gh", "ANTHROPIC_API_KEY=kept", "AWS_REGION=eu-west-1"}}
		result, err := backend.Fix(context.Background(), agent.Request{Directory: dir, Scratch: scratch, GitDir: gitDir, Network: network, Prompt: "fix", MaxCalls: 4, MaxTokens: 100})
		if err != nil {
			t.Fatal(err)
		}
		if string(result.Document) != "Resolved the conflict." || result.Calls != 1 {
			t.Fatalf("result %+v", result)
		}
		args, env := runArgs(t, dir, 0)
		if got := flag(args, "--tools"); got != "Read,Glob,Grep,Edit,Write,Bash" {
			t.Errorf("tools %q", got)
		}
		if got := flag(args, "--permission-mode"); got != "acceptEdits" {
			t.Errorf("permission mode %q", got)
		}
		if got := flag(args, "--setting-sources"); got != "" || !slices.Contains(args, "--setting-sources") {
			t.Errorf("setting sources in %q", args)
		}
		for _, name := range []string{"--restricted", "--no-session-persistence", "--strict-mcp-config"} {
			if !slices.Contains(args, name) {
				t.Errorf("missing %s in %q", name, args)
			}
		}
		for _, arg := range args {
			if arg == "--json-schema" || strings.Contains(arg, "bypassPermissions") || strings.Contains(arg, "dangerously") {
				t.Errorf("unexpected argument %q", arg)
			}
		}
		settings := settingsOf(t, args)
		s := settings.Sandbox
		if s == nil || !s.Enabled || !s.FailIfUnavailable || !s.AutoAllow || s.AllowUnsandboxed || settings.Permissions.BlockReads {
			t.Fatalf("sandbox %+v", settings)
		}
		if !slices.Equal(s.Filesystem.AllowWrite, []string{scratch, gitDir}) {
			t.Errorf("writable %q", s.Filesystem.AllowWrite)
		}
		if !slices.Equal(s.Filesystem.AllowRead, []string{home, filepath.Join(clone, ".git", "objects")}) {
			t.Errorf("readable %q", s.Filesystem.AllowRead)
		}
		for _, denied := range []string{".ssh", ".aws", ".config/gcloud", ".config/gh", ".netrc", ".git-credentials", ".docker/config.json", ".npmrc", ".pypirc", ".kube", ".gnupg", ".codex/auth.json", ".claude/.credentials.json"} {
			if !slices.Contains(s.Filesystem.DenyRead, filepath.Join(home, denied)) {
				t.Errorf("%s readable: %q", denied, s.Filesystem.DenyRead)
			}
		}
		if network {
			if s.Network != nil || !slices.Equal(settings.Permissions.Allow, []string{"Bash", "WebFetch(domain:*)"}) {
				t.Errorf("network on: %+v %q", s.Network, settings.Permissions.Allow)
			}
		} else if s.Network == nil || s.Network.AllowedDomains == nil || len(s.Network.AllowedDomains) != 0 || !slices.Equal(settings.Permissions.Allow, []string{"Bash"}) {
			t.Errorf("network off: %+v %q", s.Network, settings.Permissions.Allow)
		}
		for _, secret := range []string{"SSH_AUTH_SOCK", "GIT_ASKPASS", "GITHUB_TOKEN", "NPM_TOKEN", "GH_TOKEN"} {
			if strings.Contains(env, secret+"=") {
				t.Errorf("the agent received %s", secret)
			}
		}
		for _, kept := range []string{"ANTHROPIC_API_KEY=kept", "AWS_REGION=eu-west-1", "GOCACHE=" + filepath.Join(scratch, "cache", "go-build"), "npm_config_cache=" + filepath.Join(scratch, "cache", "npm"), "PIP_CACHE_DIR=", "GOMODCACHE=", "XDG_CACHE_HOME="} {
			if !strings.Contains(env, kept) {
				t.Errorf("environment lacks %s", kept)
			}
		}
		if strings.Contains(env, "TMPDIR="+scratch) {
			t.Error("TMPDIR points at the scratch directory, whose long path breaks the sandbox's sockets")
		}
	}
	dir := t.TempDir()
	if _, err := (&Backend{Binary: fixScript(t, false), Profile: Isolated, DefaultEndpoint: testEndpoint}).Fix(context.Background(), agent.Request{Directory: dir, MaxCalls: 1, MaxTokens: 1}); err == nil {
		t.Fatal("the isolated profile ran a fix")
	}
}

// When the sandbox cannot start, Claude Code refuses to run, and the fix
// runs again with the file tools alone, logging why.
func TestFixFallsBackToFileToolsWithoutTheSandbox(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	backend := &Backend{Binary: fixScript(t, true)}
	result, err := backend.Fix(context.Background(), agent.Request{Directory: dir, Scratch: t.TempDir(), Network: true, Prompt: "fix", MaxCalls: 4, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Document) != "Resolved the conflict." {
		t.Fatalf("result %+v", result)
	}
	first, _ := runArgs(t, dir, 0)
	if flag(first, "--tools") != "Read,Glob,Grep,Edit,Write,Bash" {
		t.Fatalf("first run %q", first)
	}
	second, _ := runArgs(t, dir, 1)
	if got := flag(second, "--tools"); got != "Read,Glob,Grep,Edit,Write" {
		t.Errorf("fallback tools %q", got)
	}
	if settings := settingsOf(t, second); settings.Sandbox != nil || !settings.Permissions.BlockReads {
		t.Errorf("fallback settings %+v", settings)
	}
	if !strings.Contains(flag(second, "--system-prompt"), "cannot run commands") {
		t.Errorf("fallback system prompt %q", flag(second, "--system-prompt"))
	}
	if !strings.Contains(logs.String(), "file tools only") || !strings.Contains(logs.String(), "bubblewrap") {
		t.Errorf("log %q", logs.String())
	}
}

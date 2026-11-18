//go:build unix

package codex

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const fixEvents = `{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"  Resolved the conflict in main.go.  "}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`

// Before permission profiles, a fix runs in the workspace-write sandbox:
// writes only to its checkout, scratch and git directories, the network as
// the request says, and the final message kept as its summary.
func TestFixRunsInTheWorkspaceWriteSandbox(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", strings.Replace(recording, successEvents, fixEvents, 1))
	for _, network := range []bool{false, true} {
		dir, scratch, gitDir := t.TempDir(), t.TempDir(), t.TempDir()
		t.Setenv("TMPDIR", t.TempDir())
		t.Setenv("GITHUB_TOKEN", "ghp_secret")
		result, err := (&Backend{Binary: script, Model: "m", Env: []string{"NPM_TOKEN=x"}}).Fix(context.Background(), agent.Request{Directory: dir, Scratch: scratch, GitDir: gitDir, Network: network, Prompt: "fix", Schema: []byte(`{}`), MaxCalls: 3, MaxTokens: 100})
		if err != nil {
			t.Fatal(err)
		}
		if string(result.Document) != "Resolved the conflict in main.go." {
			t.Fatalf("summary %q", result.Document)
		}
		args := readLines(t, dir, "arguments.txt")
		if at := slices.Index(args, "--sandbox"); at < 0 || args[at+1] != "workspace-write" {
			t.Errorf("arguments %q lack --sandbox workspace-write", args)
		}
		for _, setting := range []string{"sandbox_workspace_write.network_access=" + strconv.FormatBool(network), "sandbox_workspace_write.exclude_slash_tmp=true",
			"sandbox_workspace_write.exclude_tmpdir_env_var=true", `sandbox_workspace_write.writable_roots=["` + scratch + `","` + gitDir + `"]`, `approval_policy="never"`} {
			if !slices.Contains(args, setting) {
				t.Errorf("missing -c %s in %q", setting, args)
			}
		}
		for _, arg := range args {
			if strings.Contains(arg, "danger-full-access") || arg == "--output-schema" || strings.Contains(arg, "default_permissions") {
				t.Errorf("unexpected argument %q", arg)
			}
		}
		checkFixEnvironment(t, environmentOf(t, dir), scratch)
	}
}

// From PermissionProfileVersion on, a fix runs under a permission profile
// that also keeps the credential paths unreadable.
func TestFixRunsUnderAPermissionProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	script := fakeCodex(t, "codex-cli "+PermissionProfileVersion, strings.Replace(recording, successEvents, fixEvents, 1))
	for _, network := range []bool{true, false} {
		dir, scratch, gitDir := t.TempDir(), t.TempDir(), t.TempDir()
		t.Setenv("GITHUB_TOKEN", "ghp_secret")
		if _, err := (&Backend{Binary: script, Model: "m", Env: []string{"NPM_TOKEN=x"}}).Fix(context.Background(), agent.Request{Directory: dir, Scratch: scratch, GitDir: gitDir, Network: network, Prompt: "fix", MaxCalls: 3, MaxTokens: 100}); err != nil {
			t.Fatal(err)
		}
		args := readLines(t, dir, "arguments.txt")
		for _, arg := range args {
			if arg == "--sandbox" || strings.Contains(arg, "sandbox_workspace_write") || strings.Contains(arg, "sandbox_mode") {
				t.Errorf("a sandbox mode beside the profile: %q", arg)
			}
		}
		if !slices.Contains(args, `default_permissions="baum_fix"`) || !slices.Contains(args, "permissions.baum_fix.network.enabled="+strconv.FormatBool(network)) {
			t.Errorf("profile in %q", args)
		}
		var filesystem string
		for _, arg := range args {
			if value, ok := strings.CutPrefix(arg, "permissions.baum_fix.filesystem="); ok {
				filesystem = value
			}
		}
		for _, want := range []string{`"/"="read"`, `"` + dir + `"="write"`, `"` + scratch + `"="write"`, `"` + gitDir + `"="write"`,
			`"` + filepath.Join(home, ".ssh") + `"="none"`, `"` + filepath.Join(home, ".codex", "auth.json") + `"="none"`, `"` + filepath.Join(home, ".config", "gh") + `"="none"`} {
			if !strings.Contains(filesystem, want) {
				t.Errorf("filesystem %s lacks %s", filesystem, want)
			}
		}
		checkFixEnvironment(t, environmentOf(t, dir), scratch)
	}
}

func checkFixEnvironment(t *testing.T, env map[string]string, scratch string) {
	t.Helper()
	if env["TMPDIR"] != scratch {
		t.Errorf("TMPDIR %q, want the scratch directory", env["TMPDIR"])
	}
	if env["GOCACHE"] != filepath.Join(scratch, "cache", "go-build") || env["npm_config_cache"] != filepath.Join(scratch, "cache", "npm") || env["XDG_CACHE_HOME"] == "" || env["PIP_CACHE_DIR"] == "" || env["GOMODCACHE"] == "" {
		t.Errorf("caches outside the scratch directory: %v", env)
	}
	for name := range env {
		if name == "SSH_AUTH_SOCK" || strings.HasPrefix(name, "GIT_") || name == "GITHUB_TOKEN" || name == "NPM_TOKEN" {
			t.Errorf("the agent received %s", name)
		}
	}
}

func TestFixNeedsTheLocalProfileAndAFinishedTurn(t *testing.T) {
	if _, err := (&Backend{Profile: Container}).Fix(context.Background(), agent.Request{MaxCalls: 1, MaxTokens: 1}); err == nil {
		t.Fatal("the container profile ran a fix")
	}
	script := fakeCodex(t, "codex-cli 0.162.1", `cat >/dev/null; echo '{"type":"turn.started"}'`)
	if _, err := (&Backend{Binary: script}).Fix(context.Background(), agent.Request{Directory: t.TempDir(), MaxCalls: 3, MaxTokens: 100}); err == nil {
		t.Fatal("an unfinished fix succeeded")
	}
}

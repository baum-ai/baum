//go:build unix

package claude

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// A fix may edit files but run nothing, and keeps the final message as its
// summary rather than a structured document.
func TestFixEditsFilesButRunsNoCommand(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.310 (Claude Code)'; exit 0; fi\nprintf '%s\\n' \"$@\" > arguments.txt\nenv > environment.txt\ncat > /dev/null\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"model\":\"fixture\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"  Resolved the conflict.  \",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "/agent.sock")
	t.Setenv("GIT_ASKPASS", "/askpass")
	dir := t.TempDir()
	backend := &Backend{Binary: script, Model: "sonnet"}
	result, err := backend.Fix(context.Background(), agent.Request{Directory: dir, Prompt: "fix", MaxCalls: 4, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Document) != "Resolved the conflict." || result.Calls != 1 {
		t.Fatalf("result %+v", result)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "arguments.txt"))
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if at := slices.Index(args, "--tools"); at < 0 || args[at+1] != "Read,Glob,Grep,Edit,Write" {
		t.Errorf("tools in %q", args)
	}
	if at := slices.Index(args, "--permission-mode"); at < 0 || args[at+1] != "acceptEdits" {
		t.Errorf("permission mode in %q", args)
	}
	for _, flag := range []string{"--restricted", "--no-session-persistence", "--strict-mcp-config"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s in %q", flag, args)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "Bash") || arg == "--json-schema" || strings.Contains(arg, "bypassPermissions") || strings.Contains(arg, "dangerously") {
			t.Errorf("unexpected argument %q", arg)
		}
	}
	env, _ := os.ReadFile(filepath.Join(dir, "environment.txt"))
	if strings.Contains(string(env), "SSH_AUTH_SOCK") || strings.Contains(string(env), "GIT_ASKPASS") {
		t.Errorf("the agent received git credentials: %s", env)
	}
	if _, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Fix(context.Background(), agent.Request{Directory: dir, MaxCalls: 1, MaxTokens: 1}); err == nil {
		t.Fatal("the isolated profile ran a fix")
	}
}

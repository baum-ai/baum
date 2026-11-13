//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// A conversation turn keeps the person's own configuration: none of the flags
// that confine every other run, the access it asks for (as config on exec
// resume, which takes no --sandbox), approvals never asked for, and baum's
// server beside the person's own.
func TestConverseArguments(t *testing.T) {
	mcp := []string{"-c", `mcp_servers.baum.url="http://127.0.0.1:43123/api/mcp"`, "-c", `mcp_servers.baum.bearer_token_env_var="BAUM_MCP_TOKEN"`}
	for _, c := range []struct {
		name   string
		access agent.Access
		resume string
		want   []string
	}{
		{"full, first turn", agent.AccessFull, "", []string{"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"}},
		{"full, follow-up", agent.AccessFull, testSession, []string{"exec", "resume", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"}},
		{"workspace, first turn", agent.AccessWorkspace, "", []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "workspace-write", "-c", `approval_policy="never"`}},
		{"workspace, follow-up", agent.AccessWorkspace, testSession, []string{"exec", "resume", "--json", "--skip-git-repo-check", "-c", `sandbox_mode="workspace-write"`, "-c", `approval_policy="never"`}},
	} {
		args, env, err := (&Backend{Model: "gpt-5", Effort: "high"}).converseCommand(agent.Request{ResumeSession: c.resume, MCP: testMCP()}, c.access)
		if err != nil {
			t.Fatal(err)
		}
		want := slices.Concat(c.want, mcp, []string{"--model", "gpt-5"})
		if !slices.Equal(args, want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, args, want)
		}
		if !slices.Contains(env, agent.MCPTokenEnv+"="+mcpToken) {
			t.Errorf("%s: the token is not in the environment", c.name)
		}
	}
	args, _, err := (&Backend{}).converseCommand(agent.Request{}, agent.AccessFull)
	if err != nil || slices.Contains(args, "--model") || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "mcp_servers") }) {
		t.Errorf("no model and no server: %q, %v", args, err)
	}
}

// A turn reports the thread as soon as Codex starts it, resumes the session
// it is given before the stdin marker, and ends with the last message as its
// document; full access needs no sandbox check.
func TestConverseRunsThePersonsSession(t *testing.T) {
	events := `{"type":"thread.started","thread_id":"` + testSession + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"First"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}
{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3}}`
	script := fakeCodexSandbox(t, "codex-cli 0.167.1", "exit 1;", threadScript(events))
	kept := []string{"SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "GIT_SSH_COMMAND", "GIT_AUTHOR_NAME"}
	stripped := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY"}
	for _, name := range append(slices.Clone(kept), stripped...) {
		t.Setenv(name, "inherited")
	}
	dir := t.TempDir()
	var started, messages []string
	request := agent.Request{Directory: dir, Prompt: "go", ResumeSession: testSession, MCP: testMCP(),
		SessionStarted: func(id string) { started = append(started, id) },
		Transcript: func(entry agent.TranscriptEntry) {
			if entry.Kind == "message" {
				messages = append(messages, entry.Text)
			}
		}}
	result, err := (&Backend{Binary: script}).Converse(context.Background(), request, agent.AccessFull)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Document) != "Done." || result.SessionID != testSession || !slices.Equal(started, []string{testSession}) || !slices.Equal(messages, []string{"First", "Done."}) {
		t.Fatalf("result %q %q, started %q, messages %q", result.Document, result.SessionID, started, messages)
	}
	if result.Usage.Speed != nil || result.Usage.CacheRead != 4 || result.Usage.Output != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	args := readLines(t, dir, "arguments.txt")
	if !slices.Equal(args[len(args)-2:], []string{testSession, "-"}) {
		t.Fatalf("the session must precede the stdin marker: %q", args)
	}
	for _, flag := range []string{"--ignore-user-config", "--ignore-rules", "--ephemeral", "--disable", "--output-schema"} {
		if slices.Contains(args, flag) {
			t.Errorf("a conversation passed %s: %q", flag, args)
		}
	}
	env := environmentOf(t, dir)
	for _, name := range kept {
		if env[name] != "inherited" {
			t.Errorf("the person's session lost %s", name)
		}
	}
	for _, name := range stripped {
		if _, ok := env[name]; ok {
			t.Errorf("the person's session got %s", name)
		}
	}
	// A review's environment keeps none of what a session adds.
	reviewDir := t.TempDir()
	if _, err := (&Backend{Binary: fakeCodex(t, "codex-cli 0.167.1", threadScript(successEvents))}).Run(context.Background(), schemaRequest(reviewDir, nil)); err != nil {
		t.Fatal(err)
	}
	env = environmentOf(t, reviewDir)
	for _, name := range append(slices.Clone(kept), stripped...) {
		if _, ok := env[name]; ok {
			t.Errorf("a review got %s", name)
		}
	}
	// Workspace access runs in the sandbox, so it is checked first.
	if _, err := (&Backend{Binary: script}).Converse(context.Background(), agent.Request{Directory: t.TempDir(), Prompt: "go"}, agent.AccessWorkspace); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("workspace access without a working sandbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "schema-path.txt")); err == nil {
		t.Fatal("a conversation wrote an output schema")
	}
}

//go:build unix

package codex

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const fixEvents = `{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"  Resolved the conflict in main.go.  "}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`

// A fix writes only to its checkout and scratch directory, with the network
// off, and keeps the final message as its summary.
func TestFixRunsInTheWorkspaceWriteSandbox(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", strings.Replace(recording, successEvents, fixEvents, 1))
	dir, scratch := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	result, err := (&Backend{Binary: script, Model: "m"}).Fix(context.Background(), agent.Request{Directory: dir, Scratch: scratch, Prompt: "fix", Schema: []byte(`{}`), MaxCalls: 3, MaxTokens: 100})
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
	for _, setting := range []string{"sandbox_workspace_write.network_access=false", "sandbox_workspace_write.exclude_slash_tmp=true",
		"sandbox_workspace_write.exclude_tmpdir_env_var=true", `sandbox_workspace_write.writable_roots=["` + scratch + `"]`, `approval_policy="never"`} {
		if !slices.Contains(args, setting) {
			t.Errorf("missing -c %s in %q", setting, args)
		}
	}
	for _, arg := range args {
		if strings.Contains(arg, "danger-full-access") || arg == "--output-schema" {
			t.Errorf("unexpected argument %q", arg)
		}
	}
	env := environmentOf(t, dir)
	if env["TMPDIR"] != scratch {
		t.Errorf("TMPDIR %q, want the scratch directory", env["TMPDIR"])
	}
	for name := range env {
		if name == "SSH_AUTH_SOCK" || strings.HasPrefix(name, "GIT_") {
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

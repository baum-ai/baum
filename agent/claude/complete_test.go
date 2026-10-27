package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions"
)

func TestCompleteRunsOneToollessCall(t *testing.T) {
	out := t.TempDir()
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "/arguments.txt\nls -A > " + out + "/files.txt\nenv > " + out + "/environment.txt\ncat > " + out + "/prompt.txt\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"model\":\"claude-haiku-5-5\",\"stop_reason\":\"tool_use\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"structured_output\":{\"decisions\":[]},\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Binary: script, Model: "claude-haiku-5-5", versionOK: true}
	completion, err := backend.Complete(context.Background(), agent.CompleteRequest{System: "Extract decisions.", User: "## Turn 1", Schema: []byte(`{"type":"object"}`), MaxOutputTokens: 16384})
	if err != nil || string(completion.Document) != `{"decisions":[]}` || completion.Model != "claude-haiku-5-5" || completion.Usage.Input != 100 || completion.Usage.Output != 20 {
		t.Fatalf("%+v %v", completion, err)
	}
	arguments, _ := os.ReadFile(filepath.Join(out, "arguments.txt"))
	for _, flag := range []string{"--tools\n\n", "--json-schema\n{\"type\":\"object\"}\n", "--no-session-persistence\n", "--setting-sources\n\n",
		"--strict-mcp-config\n", "--mcp-config\n{\"mcpServers\":{}}\n", "--max-turns\n3\n", "--model\nclaude-haiku-5-5\n", "--system-prompt\nExtract decisions."} {
		if !bytes.Contains(arguments, []byte(flag)) {
			t.Fatalf("missing %q in %s", flag, arguments)
		}
	}
	if files, _ := os.ReadFile(filepath.Join(out, "files.txt")); len(bytes.TrimSpace(files)) != 0 {
		t.Fatalf("the run directory is not empty: %s", files)
	}
	prompt, _ := os.ReadFile(filepath.Join(out, "prompt.txt"))
	if !strings.HasPrefix(string(prompt), sessions.GenerationRunMarker+"\n") || !strings.HasSuffix(string(prompt), "## Turn 1") {
		t.Fatalf("prompt = %q", prompt)
	}
	environment, _ := os.ReadFile(filepath.Join(out, "environment.txt"))
	if !bytes.Contains(environment, []byte("CLAUDE_CODE_MAX_OUTPUT_TOKENS=16384\n")) || bytes.Contains(environment, []byte("CLAUDE_CODE_MAX_OUTPUT_TOKENS=32000\n")) {
		t.Fatalf("output cap not applied: %s", environment)
	}
}

func TestCompleteReportsUsageOfAFailedCall(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\ncat >/dev/null\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"id\":\"m1\",\"model\":\"claude-haiku-5-5\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"structured_output\":null,\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	completion, err := (&Backend{Binary: script, versionOK: true}).Complete(context.Background(), agent.CompleteRequest{System: "s", User: "u", Schema: []byte(`{}`)})
	if err == nil || completion.Usage.Input != 100 || completion.Model != "claude-haiku-5-5" {
		t.Fatalf("%+v %v", completion, err)
	}
}

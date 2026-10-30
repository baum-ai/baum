//go:build unix

package codex

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

func TestCompleteRunsOneToollessTurn(t *testing.T) {
	out := t.TempDir()
	script := filepath.Join(t.TempDir(), "codex")
	body := `#!/bin/sh
printf '%s\n' "$@" > ` + out + `/arguments.txt
ls -A > ` + out + `/files.txt
cat > ` + out + `/prompt.txt
for a in "$@"; do case "$a" in model_instructions_file=*) cat "$(echo "${a#model_instructions_file=}" | tr -d '"')" > ` + out + `/instructions.txt;; esac; done
printf '%s\n' '{"type":"turn.started"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"decisions\":[]}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10}}'
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Binary: script, Model: "gpt-6-luna", checked: true}
	completion, err := backend.Complete(context.Background(), agent.CompleteRequest{System: "Extract decisions.", User: "## Turn 1", Schema: []byte(`{"type":"object","properties":{"decisions":{"type":"array"}}}`)})
	if err != nil || string(completion.Document) != `{"decisions":[]}` || completion.Model != "gpt-6-luna" || completion.Usage != (agent.Usage{Input: 60, Output: 10, CacheRead: 40}) {
		t.Fatalf("%+v %v", completion, err)
	}
	arguments, _ := os.ReadFile(filepath.Join(out, "arguments.txt"))
	for _, flag := range []string{"exec\n", "--ephemeral\n", "--ignore-user-config\n", "--disable\nhooks\n", "--disable\nshell_tool\n", "--disable\nunified_exec\n",
		"--sandbox\nread-only\n", "--output-schema\n", "-c\nmodel_reasoning_effort=\"low\"\n", "--model\ngpt-6-luna\n"} {
		if !bytes.Contains(arguments, []byte(flag)) {
			t.Fatalf("missing %q in %s", flag, arguments)
		}
	}
	if instructions, _ := os.ReadFile(filepath.Join(out, "instructions.txt")); string(instructions) != "Extract decisions." {
		t.Fatalf("instructions = %q", instructions)
	}
	if files, _ := os.ReadFile(filepath.Join(out, "files.txt")); len(bytes.TrimSpace(files)) != 0 {
		t.Fatalf("the run directory is not empty: %s", files)
	}
	prompt, _ := os.ReadFile(filepath.Join(out, "prompt.txt"))
	if !strings.HasPrefix(string(prompt), sessions.GenerationRunMarker+"\n") {
		t.Fatalf("prompt = %q", prompt)
	}
}

//go:build unix

package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
		"--strict-mcp-config\n", "--mcp-config\n{\"mcpServers\":{}}\n", "--max-turns\n2\n", "--model\nclaude-haiku-5-5\n", "--system-prompt\nExtract decisions."} {
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
	if !bytes.Contains(environment, []byte("CLAUDE_CODE_MAX_OUTPUT_TOKENS=16384\n")) {
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

// A rejected reply is counted and its detail kept. The run ended with a
// failed result, so its usage stays unreported, as review accounting treats it.
func TestCompleteReportsRejectedReplies(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	events := []string{
		`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-5-5","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"tool_use","id":"so1","name":"StructuredOutput","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"so1","is_error":true,"content":"Output validation failed: choice"}]}}`,
		`{"type":"result","subtype":"error_max_turns","usage":{"input_tokens":10,"output_tokens":2}}`,
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(events, "\n")+"\nEVENTS\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	completion, _ := (&Backend{Binary: script, versionOK: true}).Complete(context.Background(),
		agent.CompleteRequest{System: "s", User: "u", Schema: []byte(`{}`)})
	if completion.Rejections != 1 || completion.RejectionDetail != "Output validation failed: choice" || !completion.Usage.Unreported {
		t.Fatalf("%+v", completion)
	}
}

func TestCompleteSendsImagesAsOneStreamJSONMessage(t *testing.T) {
	out := t.TempDir()
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "/arguments.txt\ncat > " + out + "/prompt.txt\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"structured_output\":{\"verdict\":\"A\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(image, []byte("picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Binary: script, versionOK: true}
	completion, err := backend.Complete(context.Background(), agent.CompleteRequest{System: "Judge.", User: "Compare A and B.", Schema: []byte(`{"type":"object"}`),
		Images: []agent.CompleteImage{{Path: image, MediaType: "image/png"}}})
	if err != nil || string(completion.Document) != `{"verdict":"A"}` {
		t.Fatalf("%+v %v", completion, err)
	}
	if arguments, _ := os.ReadFile(filepath.Join(out, "arguments.txt")); !bytes.Contains(arguments, []byte("--input-format\nstream-json\n")) {
		t.Fatalf("the prompt is not sent as stream-json: %s", arguments)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "prompt.txt"))
	var message struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type, MediaType, Data string
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &message); err != nil || bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatalf("not one stream-json line: %q %v", raw, err)
	}
	content := message.Message.Content
	if message.Type != "user" || message.Message.Role != "user" || len(content) != 2 || content[0].Type != "text" ||
		!strings.HasPrefix(content[0].Text, sessions.GenerationRunMarker+"\n") || !strings.HasSuffix(content[0].Text, "Compare A and B.") ||
		content[1].Type != "image" || content[1].Source.Data != base64.StdEncoding.EncodeToString([]byte("picture")) {
		t.Fatalf("message = %s", raw)
	}
}

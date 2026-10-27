package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/baum-ai/baum/agent"
)

func TestCompleteMakesOneCallWithOnlyTheStructuredOutputTool(t *testing.T) {
	check := func(t *testing.T, request sent) {
		var body struct {
			Tools      []struct{ Name string } `json:"tools"`
			ToolChoice map[string]any          `json:"tool_choice"`
			MaxTokens  int                     `json:"max_tokens"`
			Model      string                  `json:"model"`
		}
		if err := json.Unmarshal(request.raw, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Name != structuredOutputTool || body.ToolChoice["type"] != "auto" ||
			body.ToolChoice["disable_parallel_tool_use"] != true || body.MaxTokens != 16384 || body.Model != "claude-haiku-5-5" {
			t.Errorf("request = %s", request.raw)
		}
	}
	s := newStub(t, then(check, reply("tool_use", `{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5}`, submit("t1", validDocument))))
	b := s.backend()
	b.Model = "claude-haiku-5-5"
	completion, err := b.Complete(context.Background(), agent.CompleteRequest{System: "Extract.", User: "## Turn 1", Schema: testSchema, MaxOutputTokens: 16384})
	if err != nil || string(completion.Document) != validDocument || completion.Usage.Input != 100 || completion.Usage.CacheRead != 5 || completion.Model != "claude-opus-5-5" {
		t.Fatalf("%+v %v", completion, err)
	}
	if n := len(s.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestCompleteFailsWithoutAValidDocumentAndKeepsUsage(t *testing.T) {
	for name, step := range map[string]step{
		"no call":   reply("end_turn", smallUsage, `{"type":"text","text":"here it is"}`),
		"invalid":   reply("tool_use", smallUsage, submit("t1", `{"summary":"x"}`)),
		"truncated": reply("max_tokens", smallUsage, submit("t1", validDocument)),
	} {
		t.Run(name, func(t *testing.T) {
			completion, err := newStub(t, step).backend().Complete(context.Background(), agent.CompleteRequest{System: "s", User: "u", Schema: testSchema})
			var named *Error
			if !errors.As(err, &named) || completion.Usage.Input != 10 || completion.Document != nil {
				t.Fatalf("%+v %v", completion, err)
			}
		})
	}
}

package cursorsession

import (
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func TestBuildPortableMapsReducedFidelityCursorSession(t *testing.T) {
	input := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>now</timestamp><user_query>change it</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"working"},{"type":"tool_use","name":"Read","input":{"path":"/repo/main.go"}},{"type":"tool_use","name":"ApplyPatch","input":"*** Begin Patch\n*** Update File: /repo/main.go\n*** Move to: /repo/renamed.go\n@@\n-old\n+new\n*** End Patch"}]}}`,
		`{"type":"turn_ended","status":"error","error":"user aborted"}`,
	}, "\n")
	records, err := cursortranscript.DecodePortable(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	graph, report, err := BuildPortable("composer-1", "full.jsonl", records, time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Balanced() || report.Records.Total != 3 || report.Records.Provenance != 1 || report.Blocks.Mapped != 4 {
		t.Fatalf("report = %+v", report)
	}
	if len(graph.UserInputs) != 1 || graph.UserInputs[0].Text != "change it" || !graph.UserInputs[0].Interrupted {
		t.Fatalf("user input = %+v", graph.UserInputs)
	}
	if len(graph.ModelInvocations) != 1 || len(graph.AssistantMessages) != 1 || len(graph.ToolUses) != 2 || len(graph.ToolResults) != 0 {
		t.Fatalf("graph rows = %+v", graph)
	}
	for _, use := range graph.ToolUses {
		if use.Status != session.StatusNoResult {
			t.Errorf("tool %s status = %q", use.Name, use.Status)
		}
	}
	if len(graph.FileTouches) != 3 || graph.FileTouches[2].Path != "/repo/renamed.go" || graph.FileTouches[2].Kind != session.TouchEdit || len(graph.Attributes) != 1 || graph.Attributes[0].Key != "cursor.fallback_store" {
		t.Fatalf("touches/attributes = %+v / %+v", graph.FileTouches, graph.Attributes)
	}
}

func TestBuildPortableDoesNotCarryOwnershipAcrossTurnBoundary(t *testing.T) {
	records, err := cursortranscript.DecodePortable(strings.NewReader(strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>go</user_query>"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"unowned"}]}}`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildPortable("composer-1", "full.jsonl", records, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "before a user record") {
		t.Fatalf("BuildPortable error = %v", err)
	}
}

func TestBuildPortableEstimatesEachInvocation(t *testing.T) {
	records, err := cursortranscript.DecodePortable(strings.NewReader(strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>abcd</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"abcdefgh"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	graph, report, err := BuildPortable("composer-estimate", "full.jsonl", records, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	invocation := graph.ModelInvocations[0]
	if invocation.InputTokens != 1 || invocation.OutputTokens != 2 || invocation.TotalTokens != 3 {
		t.Fatalf("estimated invocation = %+v, want 1 input and 2 output tokens", invocation)
	}
	if invocation.TokenBasis != session.TokenBasisEstimated || report.EstimatedInvocations != 1 {
		t.Fatalf("estimate basis/report = %q / %+v", invocation.TokenBasis, report)
	}
}

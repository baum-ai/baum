package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ParseReview reads a baum.review/2 document as it reads baum.review/1:
// its sections with their steps and risks, the index's hunks, sessions and
// attachments, and the verdict as the recommendation.
func TestParseReviewV2(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "reviewv2", "testdata", "v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseReview(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sections) != 3 || r.Sections[0].ID != "sec_retry_policy" || len(r.Sections[0].Steps) != 2 || len(r.Sections[0].Risks) != 1 || r.Sections[0].Risks[0].Hunks[0] != "h_a00000000001" {
		t.Fatalf("sections = %+v", r.Sections)
	}
	if !r.HasHunk("h_a00000000004") || len(r.Sessions) != 1 || len(r.Attachments) != 1 || r.Recommendation == nil || r.Recommendation.Decision != "look_first" {
		t.Fatalf("review = %+v", r)
	}
	if len(r.Context) != 3 || r.Risk == nil || r.Risk.Score != 3 {
		t.Fatalf("context = %+v, risk = %+v", r.Context, r.Risk)
	}
}

func TestCheckNative(t *testing.T) {
	has := func(id string) bool { return id == "h_000000000001" }
	data := json.RawMessage(`{"source": [{"type": "hunk", "id": "h_000000000001"}, {"type": "hunk", "id": "h_000000000009"}], "lanes": [{"steps": [{"title": "` + strings.Repeat("x", 70) + `"}]}]}`)
	out, problem, report := CheckNative(TypeFlow, data, has)
	if problem != "" || report.Counts()[FixCut] != 1 || report.Counts()[FixAnchor] != 1 || !strings.Contains(string(out), "…") {
		t.Fatalf("out %s problem %q counts %v", out, problem, report.Counts())
	}
	if _, problem, _ := CheckNative(TypeColumns, json.RawMessage(`{"columns": []}`), has); problem == "" {
		t.Fatal("a columns tile without columns must fail")
	}
	if _, problem, _ := CheckNative("pie", json.RawMessage(`{}`), has); problem == "" {
		t.Fatal("an unknown type must fail")
	}
}

// A branch keeps the first lane's step it leaves after; a branch from a
// step that is not there, or a first lane that names one, has none.
func TestCheckNativeFlowBranches(t *testing.T) {
	data := json.RawMessage(`{"source": [], "lanes": [
		{"from": 1, "steps": [{"title": "Read"}, {"title": "Check"}, {"title": "Save"}]},
		{"label": "No source", "from": 2, "steps": [{"title": "Skipped"}]},
		{"label": "Lost", "from": 4, "steps": [{"title": "Nowhere"}]}]}`)
	out, problem, _ := CheckNative(TypeFlow, data, nil)
	if problem != "" {
		t.Fatal(problem)
	}
	var flow FlowData
	if err := json.Unmarshal(out, &flow); err != nil {
		t.Fatal(err)
	}
	if got := []int{flow.Lanes[0].From, flow.Lanes[1].From, flow.Lanes[2].From}; got[0] != 0 || got[1] != 2 || got[2] != 0 {
		t.Fatalf("from = %v, want [0 2 0]", got)
	}
}

// A reply's branch reaches the tile's data.
func TestDiagramDataFlowFrom(t *testing.T) {
	from, label := 2, "No source"
	data, ok := diagramData(ReplyDiagram{Type: TypeFlow, Lanes: []replyLane{
		{Steps: []replyFlowStep{{Title: "Read"}, {Title: "Check"}}},
		{Label: &label, From: &from, Steps: []replyFlowStep{{Title: "Skipped"}}},
	}})
	if !ok || data.(FlowData).Lanes[1].From != 2 || data.(FlowData).Lanes[0].From != 0 {
		t.Fatalf("data %+v ok %v", data, ok)
	}
}

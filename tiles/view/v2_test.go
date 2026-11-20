package view

import (
	"encoding/json"
	"strings"
	"testing"
)

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

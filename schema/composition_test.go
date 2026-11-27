package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// The composition schema reuses the tile schema's definitions; a name both
// define is an error.
func TestMergeCompositionRejectsSharedDefinitions(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(ViewComposition, &root); err != nil {
		t.Fatal(err)
	}
	root["$defs"].(map[string]any)["tileId"] = map[string]any{"type": "string"}
	composition, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	tiles, err := MergedTiles()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mergeSchema("composition-2.json", composition, tiles); err == nil || !strings.Contains(err.Error(), "tileId") {
		t.Errorf("err = %v, want a shared-definition error", err)
	}
}

func TestCompileViewComposition(t *testing.T) {
	compiled, err := CompileViewComposition()
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Schema().ID != ViewCompositionURL {
		t.Errorf("$id = %q, want %q", compiled.Schema().ID, ViewCompositionURL)
	}
	const empty = `"overview": {"screenshots": [], "intent": []}, "sections": []`
	flow := `{"id": "dg_shape", "type": "flow", "title": "How a phone reaches the daemon", "data": {"source": [{"type": "hunk", "id": "h_000000000001"}], "lanes": [{"label": "Phone", "steps": [{"title": "Phone app"}, {"title": "Relay", "tone": "muted", "join": "both"}]}]}}`
	for _, tc := range []struct {
		name  string
		doc   string
		valid bool
	}{
		{"empty", `{"schema": "baum.composition/2", ` + empty + `}`, true},
		{"v1", `{"schema": "baum.composition/1", ` + empty + `}`, false},
		{"a flow", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [` + flow + `]}`, true},
		{"placed", `{"schema": "baum.composition/2", "overview": {"screenshots": ["att_ab"], "intent": [{"asked": "A", "got": "B", "session": "s1", "prompt": 1}], "diagram": "dg_shape", "verdict": {"text": "Decide first.", "points": [{"text": "Decide it.", "section": "sec_a"}]}, "risk_summary": "Why."}, "sections": [{"id": "context", "tiles": []}, {"id": "sec_a", "tiles": ["schema_change"]}], "diagrams": [` + flow + `]}`, true},
		{"a verdict without points", `{"schema": "baum.composition/2", "overview": {"screenshots": [], "intent": [], "verdict": {"text": "Decide.", "points": []}}, "sections": []}`, false},
		{"a section of another id", `{"schema": "baum.composition/2", "overview": {"screenshots": [], "intent": []}, "sections": [{"id": "overview", "tiles": []}]}`, false},
		{"a flow step too long", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [{"id": "dg_x", "type": "flow", "title": "X", "data": {"source": [], "lanes": [{"steps": [{"title": "` + strings.Repeat("x", 61) + `"}]}]}}]}`, false},
		{"a table of one column", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [{"id": "dg_x", "type": "table", "title": "X", "data": {"source": [], "columns": ["A"], "rows": [["a"]]}}]}`, false},
		{"a sequence", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [{"id": "dg_x", "type": "sequence", "title": "X", "data": {"source": [], "actors": ["Phone", "Relay"], "steps": [{"from": 0, "to": 1, "text": "hello"}]}}]}`, true},
		{"columns of another type's shape", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [{"id": "dg_x", "type": "columns", "title": "X", "data": {"source": [], "actors": ["Phone", "Relay"]}}]}`, false},
		{"a file anchor", `{"schema": "baum.composition/2", ` + empty + `, "diagrams": [{"id": "dg_x", "type": "sequence", "title": "X", "data": {"source": [{"type": "file", "path": "a.go"}], "actors": ["Phone", "Relay"], "steps": [{"from": 0, "to": 1, "text": "hello"}]}}]}`, false},
	} {
		var instance any
		if err := json.Unmarshal([]byte(tc.doc), &instance); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if err := compiled.Validate(instance); (err == nil) != tc.valid {
			t.Errorf("%s: err = %v, want valid %v", tc.name, err, tc.valid)
		}
	}
}

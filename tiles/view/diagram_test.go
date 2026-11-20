package view_test

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/tiles/view"
)

func TestDiagramSchema(t *testing.T) {
	for _, kind := range view.NativeTypes {
		schema := view.DiagramSchema(kind)
		var s map[string]any
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if _, hasID := s["properties"].(map[string]any)["id"]; hasID {
			t.Errorf("%s: a tile's schema has an id", kind)
		}
	}
	if view.DiagramSchema("code") != nil {
		t.Error("a schema for a type that is not native")
	}
}

func TestCheckDiagram(t *testing.T) {
	has := func(id string) bool { return id == "h_aaaaaaaaaaaa" }
	var d view.ReplyDiagram
	_ = json.Unmarshal([]byte(`{"type":"table","title":"How **long** a job waits","caption":null,"source":["h_aaaaaaaaaaaa","h_bbbbbbbbbbbb"],
		"columns":["Attempt","Wait"],"rows":[["1","Base"],["2"]]}`), &d)
	title, data, report, problem := view.CheckDiagram(d, has)
	if problem != "" || title != "How long a job waits" {
		t.Fatalf("title %q problem %q", title, problem)
	}
	var table view.TableData
	_ = json.Unmarshal(data, &table)
	if len(table.Rows) != 1 || len(table.Source) != 1 || report.Total() == 0 {
		t.Errorf("table: %+v, fixes %d", table, report.Total())
	}
	_ = json.Unmarshal([]byte(`{"type":"flow","title":"A flow","caption":null,"source":[],"lanes":[]}`), &d)
	if _, _, _, problem := view.CheckDiagram(d, has); problem == "" {
		t.Error("a flow without lanes passed")
	}
}

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

func TestCheckDiagramComparedColumns(t *testing.T) {
	var d view.ReplyDiagram
	_ = json.Unmarshal([]byte(`{"type":"columns","title":"Clips","caption":null,"source":[],
		"columns":[{"label":"Image","tone":"muted","items":[{"title":"Dropped","note":null}]},{"label":"Clip","tone":"new","items":null}],
		"rows":[[{"title":"Failure says Image unavailable","note":null},{"title":"Failure says Video unavailable","note":"Retry reloads it"}],
			[null,{"title":"Plays in place","note":null}],[null,null],[{"title":"Cut","note":null}]]}`), &d)
	_, data, _, problem := view.CheckDiagram(d, nil)
	if problem != "" {
		t.Fatalf("problem %q", problem)
	}
	var columns view.ColumnsData
	_ = json.Unmarshal(data, &columns)
	if columns.Columns[0].Items != nil {
		t.Errorf("a compared column kept its own items: %+v", columns.Columns[0])
	}
	// The row of gaps is dropped and the short row padded.
	if len(columns.Rows) != 3 || columns.Rows[1][0] != nil || columns.Rows[1][1].Title != "Plays in place" ||
		len(columns.Rows[2]) != 2 || columns.Rows[2][1] != nil || columns.Rows[0][1].Note != "Retry reloads it" {
		t.Errorf("rows %s", data)
	}
	_ = json.Unmarshal([]byte(`{"type":"columns","title":"Lists","caption":null,"source":[],
		"columns":[{"label":"Can","tone":"good","items":[{"title":"Read","note":null}]},{"label":"Can't","tone":"bad","items":[{"title":"Write","note":null}]}],"rows":null}`), &d)
	_, data, _, problem = view.CheckDiagram(d, nil)
	var lists view.ColumnsData
	if _ = json.Unmarshal(data, &lists); problem != "" || len(lists.Rows) != 0 || len(lists.Columns[1].Items) != 1 {
		t.Errorf("lists: problem %q, %s", problem, data)
	}
}

package view_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles/view"
)

// A cell without a was is a plain string in the JSON, as stored data and
// its readers have it; with a was it is an object.
func TestCellJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(view.TableData{Source: []view.Anchor{}, Columns: []string{"Case", "Wait"},
		Rows: [][]view.Cell{{{Text: "First"}, {Text: "1 s", Was: "none"}}, {{Text: "Next"}, {Text: "2 s"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"source":[],"columns":["Case","Wait"],"rows":[["First",{"text":"1 s","was":"none"}],["Next","2 s"]]}`
	if string(data) != want {
		t.Fatalf("encoded\n%s\nwant\n%s", data, want)
	}
	var back view.TableData
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Rows[0][1] != (view.Cell{Text: "1 s", Was: "none"}) || back.Rows[1][0] != (view.Cell{Text: "Next"}) {
		t.Fatalf("decoded %+v", back.Rows)
	}
	// Data stored before cells could carry a was reads, and writes, as it was.
	var stored view.TableData
	old := `{"source":[],"columns":["A","B"],"rows":[["a","b"]]}`
	if err := json.Unmarshal([]byte(old), &stored); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(stored); string(again) != old {
		t.Errorf("stored data changed: %s", again)
	}
	// A null was is no was; an object needs its text; anything else is not a cell.
	var cell view.Cell
	if err := json.Unmarshal([]byte(`{"text":"x","was":null}`), &cell); err != nil || cell != (view.Cell{Text: "x"}) {
		t.Errorf("null was: %+v %v", cell, err)
	}
	for _, bad := range []string{`{"was":"x"}`, `3`, `["a"]`, `null`} {
		if bad == "null" {
			continue // a JSON null leaves a value as it was
		}
		if err := json.Unmarshal([]byte(bad), &cell); err == nil {
			t.Errorf("%s decoded as a cell", bad)
		}
	}
}

// Each part of a cell is checked to MaxCell; a flow step's more to MaxMore.
func TestCheckNativeCellsAndMore(t *testing.T) {
	long := strings.Repeat("word ", 80)
	table, _ := json.Marshal(map[string]any{"source": []any{}, "columns": []string{"Case", "Wait"},
		"rows": [][]any{{"First", map[string]any{"text": long, "was": long}}, {"Next", map[string]any{"text": "2 s", "was": " 1 s "}}}})
	data, problem, report := view.CheckNative(view.TypeTable, table, nil)
	if problem != "" {
		t.Fatal(problem)
	}
	var got view.TableData
	_ = json.Unmarshal(data, &got)
	if n := len([]rune(got.Rows[0][1].Text)); n > view.MaxCell || !strings.HasSuffix(got.Rows[0][1].Text, "…") {
		t.Errorf("text of %d characters", n)
	}
	if n := len([]rune(got.Rows[0][1].Was)); n > view.MaxCell || !strings.HasSuffix(got.Rows[0][1].Was, "…") {
		t.Errorf("was of %d characters", n)
	}
	if got.Rows[1][1] != (view.Cell{Text: "2 s", Was: "1 s"}) || report.Counts()[view.FixCut] != 2 {
		t.Errorf("rows %+v, fixes %v", got.Rows, report.Counts())
	}

	flow, _ := json.Marshal(map[string]any{"source": []any{}, "lanes": []any{map[string]any{"steps": []any{
		map[string]any{"title": "Deliver", "more": "Gets  the\nevent out."},
		map[string]any{"title": "Retry", "more": long, "join": "arrow"}}}}})
	data, problem, report = view.CheckNative(view.TypeFlow, flow, nil)
	if problem != "" {
		t.Fatal(problem)
	}
	var f view.FlowData
	_ = json.Unmarshal(data, &f)
	steps := f.Lanes[0].Steps
	if steps[0].More != "Gets the event out." || len([]rune(steps[1].More)) > view.MaxMore || !strings.HasSuffix(steps[1].More, "…") || report.Counts()[view.FixCut] != 1 {
		t.Errorf("steps %+v, fixes %v", steps, report.Counts())
	}
	// A step's more that names a hunk drops the tile, as any text does.
	hunky, _ := json.Marshal(map[string]any{"source": []any{}, "lanes": []any{map[string]any{"steps": []any{map[string]any{"title": "Deliver", "more": "See h_a00000000001."}}}}})
	if _, problem, _ := view.CheckNative(view.TypeFlow, hunky, nil); problem == "" {
		t.Error("a more naming a hunk passed")
	}
}

// The tile agent's reply schema takes a step's more and a cell that is a
// string or {text, was}; the data it gives is the stored shape.
func TestDiagramReplyWithMoreAndWas(t *testing.T) {
	has := func(string) bool { return true }
	var flow view.ReplyDiagram
	if err := json.Unmarshal([]byte(`{"type":"flow","title":"Retry","caption":null,"source":[],"lanes":[{"label":null,"from":null,"steps":[
		{"title":"Deliver","note":null,"more":"Hands the event to the client.","tone":null,"join":null},{"title":"Retry","note":null,"more":null,"tone":null,"join":"arrow"}]}]}`), &flow); err != nil {
		t.Fatal(err)
	}
	_, data, _, problem := view.CheckDiagram(flow, has)
	var f view.FlowData
	if problem != "" || json.Unmarshal(data, &f) != nil || f.Lanes[0].Steps[0].More != "Hands the event to the client." || f.Lanes[0].Steps[1].More != "" {
		t.Fatalf("flow %s: %s", data, problem)
	}
	var table view.ReplyDiagram
	if err := json.Unmarshal([]byte(`{"type":"table","title":"Waits","caption":null,"source":[],"columns":["Try","Wait"],"rows":[["1",{"text":"1 s","was":null}],["2",{"text":"2 s","was":"1 s"}]]}`), &table); err != nil {
		t.Fatal(err)
	}
	_, data, _, problem = view.CheckDiagram(table, has)
	if problem != "" || !strings.Contains(string(data), `"rows":[["1","1 s"],["2",{"text":"2 s","was":"1 s"}]]`) {
		t.Fatalf("table %s: %s", data, problem)
	}
	schema := string(view.DiagramSchema(view.TypeFlow))
	if !strings.Contains(schema, `"more":{"type":["string","null"],"maxLength":300}`) || !strings.Contains(string(view.DiagramSchema(view.TypeTable)), `"was"`) {
		t.Errorf("the schemas lack more or was")
	}
}

package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

func TestReview2Revision(t *testing.T) {
	rev, err := Review2Revision()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.\d+$`).MatchString(rev) {
		t.Errorf("revision %q is not date-based", rev)
	}
}

func TestCompileReview2RecordsID(t *testing.T) {
	compiled, err := CompileReview2()
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Schema().ID != Review2URL {
		t.Errorf("$id = %q, want %q", compiled.Schema().ID, Review2URL)
	}
}

// review2Example is the completed hand-made baum.review/2 example of the
// document layer (oss/reviewv2), which checks it against its own rules.
func review2Example(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "reviewv2", "testdata", "v2.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestValidateReview2(t *testing.T) {
	doc := review2Example(t)
	encoded, _ := json.Marshal(doc)
	if err := ValidateReview2(encoded); err != nil {
		t.Fatalf("the example does not validate: %v", err)
	}
	if doc["schema"] != Review2Name {
		t.Errorf("schema = %v", doc["schema"])
	}
	tile := func(doc map[string]any, i int) map[string]any { return doc["tiles"].([]any)[i].(map[string]any) }
	section := func(doc map[string]any, i int) map[string]any { return doc["sections"].([]any)[i].(map[string]any) }
	block := func(doc map[string]any, s, b int) map[string]any {
		return section(doc, s)["body"].([]any)[b].(map[string]any)
	}
	for name, mutate := range map[string]func(doc map[string]any){
		"wrong schema name":       func(doc map[string]any) { doc["schema"] = Name },
		"unknown top-level field": func(doc map[string]any) { doc["view"] = map[string]any{} },
		"missing context":         func(doc map[string]any) { delete(doc, "context") },
		"section id":              func(doc map[string]any) { section(doc, 0)["id"] = "retry" },
		"missing body":            func(doc map[string]any) { delete(section(doc, 0), "body") },
		"block type":              func(doc map[string]any) { block(doc, 0, 0)["type"] = "chart" },
		"prose without text":      func(doc map[string]any) { delete(block(doc, 0, 0), "text") },
		"points without items":    func(doc map[string]any) { delete(block(doc, 0, 1), "items") },
		"point without cites":     func(doc map[string]any) { delete(block(doc, 0, 1)["items"].([]any)[0].(map[string]any), "cites") },
		"tile block without tile": func(doc map[string]any) { delete(block(doc, 0, 2), "tile") },
		"tile block id":           func(doc map[string]any) { block(doc, 0, 2)["tile"] = "Wait Code" },
		"unknown block field":     func(doc map[string]any) { block(doc, 0, 0)["html"] = "<b>x</b>" },
		"step id in an older section": func(doc map[string]any) {
			section(doc, 0)["steps"] = []any{map[string]any{"id": "beh_x", "title": "t", "text": "x", "cites": []any{}}}
		},
		"flow step more too long": func(doc map[string]any) {
			tile(doc, 0)["data"].(map[string]any)["lanes"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["more"] = string(make([]byte, 301))
		},
		"risk tier":  func(doc map[string]any) { section(doc, 0)["risks"].([]any)[0].(map[string]any)["tier"] = "severe" },
		"level":      func(doc map[string]any) { section(doc, 0)["level"] = 6 },
		"tile owner": func(doc map[string]any) { tile(doc, 0)["owner"] = "somewhere" },
		"tile state": func(doc map[string]any) { tile(doc, 0)["state"] = "done" },
		"flow shape": func(doc map[string]any) { tile(doc, 0)["data"] = map[string]any{"lanes": []any{}} },
		"flow step title": func(doc map[string]any) {
			tile(doc, 0)["data"].(map[string]any)["lanes"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["title"] = string(make([]byte, 61))
		},
		"code without hunk": func(doc map[string]any) { tile(doc, 1)["data"] = map[string]any{"lines": []any{1, 2}} },
		"verdict decision": func(doc map[string]any) {
			doc["summary"].(map[string]any)["verdict"].(map[string]any)["decision"] = "merge"
		},
		"header": func(doc map[string]any) { doc["review"].(map[string]any)["pull_request"] = "42" },
		"risk score": func(doc map[string]any) {
			doc["summary"].(map[string]any)["risk_score"].(map[string]any)["rubric"] = "v1"
		},
		"screenshot": func(doc map[string]any) { doc["overview"].(map[string]any)["screenshots"] = []any{"shot"} },
	} {
		t.Run(name, func(t *testing.T) {
			doc := review2Example(t)
			mutate(doc)
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateReview2(encoded); err == nil {
				t.Fatal("document must not validate")
			}
		})
	}
	// A flow step may carry a detail; a table cell is a string or {text, was}.
	doc = review2Example(t)
	tile(doc, 0)["data"].(map[string]any)["lanes"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["more"] = "What happens when it is opened."
	tile(doc, 2)["type"], tile(doc, 2)["data"] = "table", map[string]any{"columns": []any{"Case", "Wait"},
		"rows": []any{[]any{"First", map[string]any{"text": "1 s", "was": "none"}}, []any{"Next", "2 s"}}}
	encoded, _ = json.Marshal(doc)
	if err := ValidateReview2(encoded); err != nil {
		t.Fatalf("a flow step's more and a cell with was must validate: %v", err)
	}
	tile(doc, 2)["data"].(map[string]any)["rows"] = []any{[]any{"First", map[string]any{"was": "none"}}}
	encoded, _ = json.Marshal(doc)
	if ValidateReview2(encoded) == nil {
		t.Error("a cell object without text validated")
	}
	// A document written before bodies still validates when it has one too;
	// summary and steps are optional.
	doc = review2Example(t)
	section(doc, 0)["summary"], section(doc, 0)["steps"] = "Older text.", []any{}
	encoded, _ = json.Marshal(doc)
	if err := ValidateReview2(encoded); err != nil {
		t.Fatalf("an older section's summary and steps must still be allowed: %v", err)
	}
	// A frame tile's data is its own type's: any object.
	doc = review2Example(t)
	tile(doc, 2)["type"], tile(doc, 2)["data"] = "schema_change", map[string]any{"anything": []any{1}}
	encoded, _ = json.Marshal(doc)
	if err := ValidateReview2(encoded); err != nil {
		t.Fatalf("a frame tile's data must be open: %v", err)
	}
}

// The native tiles' data shapes are the review view composition's, without
// the caption and source a baum.review/2 tile carries beside its data, so
// the limits tiles/view checks hold for both.
func TestReview2NativeShapesMatchComposition(t *testing.T) {
	var v2, composition struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(Review2, &v2); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ViewComposition, &composition); err != nil {
		t.Fatal(err)
	}
	strip := func(def map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range def {
			if k != "description" {
				out[k] = v
			}
		}
		props := map[string]any{}
		for k, v := range def["properties"].(map[string]any) {
			if k != "caption" && k != "source" {
				props[k] = v
			}
		}
		out["properties"] = props
		var required []any
		for _, r := range def["required"].([]any) {
			if r != "source" {
				required = append(required, r)
			}
		}
		out["required"] = required
		encoded, _ := json.Marshal(out)
		encoded = regexp.MustCompile(`nativeTone|v2Tone`).ReplaceAll(encoded, []byte("tone"))
		var normalized map[string]any
		_ = json.Unmarshal(encoded, &normalized)
		var drop func(any)
		drop = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				delete(v, "description")
				for _, item := range v {
					drop(item)
				}
			case []any:
				for _, item := range v {
					drop(item)
				}
			}
		}
		drop(normalized)
		return normalized
	}
	for _, typ := range []string{"flow", "columns", "sequence", "table"} {
		got, want := strip(v2.Defs["v2TileData_"+typ]), strip(composition.Defs["nativeData_"+typ])
		if !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			t.Errorf("%s:\n got %s\nwant %s", typ, gotJSON, wantJSON)
		}
	}
}

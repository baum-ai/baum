package schema

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRevision(t *testing.T) {
	rev, err := Revision()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.\d+$`).MatchString(rev) {
		t.Errorf("revision %q is not date-based", rev)
	}
}

func TestCompileRecordsID(t *testing.T) {
	compiled, err := Compile()
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Schema().ID != URL {
		t.Errorf("$id = %q, want %q", compiled.Schema().ID, URL)
	}
}

func TestExampleValidates(t *testing.T) {
	raw, doc := readExample(t)
	if err := Validate(raw); err != nil {
		t.Fatalf("review-example.json does not validate: %v", err)
	}
	if got := doc["schema"]; got != Name {
		t.Errorf("example schema field = %v, want %q", got, Name)
	}
}

func TestValidateRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]func(doc map[string]any){
		"wrong schema name":       func(doc map[string]any) { doc["schema"] = "baum.review/0" },
		"missing required field":  func(doc map[string]any) { delete(doc, "summary") },
		"unknown top-level field": func(doc map[string]any) { doc["extra"] = true },
		"wrong enum value": func(doc map[string]any) {
			doc["review"].(map[string]any)["pr_state"] = "paused"
		},
		"wrong type": func(doc map[string]any) {
			doc["review"].(map[string]any)["pull_request"] = "12"
		},
		"number below minimum": func(doc map[string]any) {
			doc["review"].(map[string]any)["pull_request"] = 0
		},
		"pattern mismatch": func(doc map[string]any) {
			doc["review"].(map[string]any)["head"] = "not-a-sha"
		},
		"wrong type in a referenced definition": func(doc map[string]any) {
			section := doc["sections"].([]any)[0].(map[string]any)
			section["title"] = 42
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, doc := readExample(t)
			mutate(doc)
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(encoded); err == nil {
				t.Fatal("document must not validate")
			}
		})
	}
}

// generator.prompt_version is optional: documents stored before the platform
// recorded it still validate, and a recorded value must not be empty.
func TestGeneratorPromptVersionIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"absent", nil, true},
		{"recorded", "review-v12", true},
		{"empty", "", false},
		{"not a string", 12, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, doc := readExample(t)
			generator := doc["review"].(map[string]any)["generator"].(map[string]any)
			delete(generator, "prompt_version")
			if tc.value != nil {
				generator["prompt_version"] = tc.value
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(encoded); (err == nil) != tc.valid {
				t.Fatalf("err = %v, want valid %v", err, tc.valid)
			}
		})
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	err := Validate([]byte(`{"schema":`))
	if err == nil || !strings.Contains(err.Error(), "decoding") {
		t.Fatalf("err = %v, want a decoding error", err)
	}
}

// firstBehaviorStep returns the first behaviour step of the example.
func firstBehaviorStep(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	for _, rawSection := range doc["sections"].([]any) {
		for _, rawBlock := range rawSection.(map[string]any)["blocks"].([]any) {
			block := rawBlock.(map[string]any)
			if block["kind"] == "behavior" {
				return block["steps"].([]any)[0].(map[string]any)
			}
		}
	}
	t.Fatal("example has no behavior step")
	return nil
}

func TestBehaviorStepTourFields(t *testing.T) {
	marshal := func(doc map[string]any) []byte {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	_, doc := readExample(t)
	step := firstBehaviorStep(t, doc)
	for _, key := range []string{"title", "before", "after", "question"} {
		if _, ok := step[key].(string); !ok {
			t.Fatalf("example step has no %s", key)
		}
	}
	// The fields are optional: a step without them stays valid.
	for _, key := range []string{"title", "before", "after", "question"} {
		delete(step, key)
	}
	if err := Validate(marshal(doc)); err != nil {
		t.Fatalf("step without tour fields rejected: %v", err)
	}
	step["title"] = strings.Repeat("x", 161)
	if err := Validate(marshal(doc)); err == nil {
		t.Error("a title over 160 characters was accepted")
	}
	step["title"] = 3
	if err := Validate(marshal(doc)); err == nil {
		t.Error("a non-string title was accepted")
	}
}

func readExample(t *testing.T) ([]byte, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("review-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

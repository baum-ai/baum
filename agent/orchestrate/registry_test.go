package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/tiles/library"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry(library.All())
	names := r.names()
	want := []string{"flow", "columns", "sequence", "table", "code", "schema_change", "api_change"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("types: %v", names)
	}
	flow, _ := r.Get("flow")
	if flow.Dedicated || flow.Host || flow.Fill(agent.ReviewV2Prompt{Natives: map[string]string{"flow": "F"}}) != "F" {
		t.Errorf("flow: %+v", flow)
	}
	code, _ := r.Get("code")
	if !code.Host || code.Fill(agent.ReviewV2Prompt{}) != "" {
		t.Errorf("code: %+v", code)
	}
	schema, ok := r.Get("schema_change")
	if !ok || !schema.Dedicated || schema.Entry == nil || schema.Fill(agent.ReviewV2Prompt{}) == "" || len(schema.Schema) == 0 {
		t.Errorf("schema_change: %+v", schema)
	}
	offer := r.Offer()
	if !strings.Contains(offer, "- `flow` (Flow, inline): ") || !strings.Contains(offer, "(dedicated)") && !strings.Contains(offer, ", dedicated)") {
		t.Errorf("offer: %s", offer)
	}
	for _, kind := range r.types {
		var s jsonschema.Schema
		if err := json.Unmarshal(kind.Schema, &s); err != nil {
			t.Errorf("%s schema: %v", kind.Name, err)
			continue
		}
		if _, err := s.Resolve(nil); err != nil {
			t.Errorf("%s schema does not resolve: %v", kind.Name, err)
		}
	}
	// A saved type that takes a native name is left out.
	clash := library.All()[0]
	clash.Definition.Name = "flow"
	if got := NewRegistry([]library.Entry{clash}).names(); len(got) != 5 {
		t.Errorf("a clashing saved type was offered: %v", got)
	}
}

func validates(t *testing.T, name string, schema []byte, reply string) {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s does not resolve: %v", name, err)
	}
	var value any
	if err := json.Unmarshal([]byte(reply), &value); err != nil {
		t.Fatalf("%s reply: %v", name, err)
	}
	if err := resolved.Validate(value); err != nil {
		t.Errorf("%s rejects its sample: %v", name, err)
	}
}

func TestReplySchemas(t *testing.T) {
	types := NewRegistry(nil).names()
	validates(t, "map", MapSchema(types), mapReplyJSON)
	// The sample body has every block type (prose, points with a detail, a
	// tile and a note); a proposal carries its id.
	validates(t, "section", SectionSchema(types), sectionReplyJSON(`[]`))
	validates(t, "section with a proposal", SectionSchema(types), strings.Replace(sectionReplyJSON(`[]`), `"propose":null`,
		`"propose":{"id":"tl_more_cases","type":"table","shows":"The cases.","cites":[],"focus":null}`, 1))
	validates(t, "context", ContextSchema(), contextReplyJSON)
	validates(t, "overview", OverviewSchema(), overviewReplyJSON)
	r := NewRegistry(nil)
	flow, _ := r.Get("flow")
	validates(t, "flow", flow.Schema, flowReply)
	table, _ := r.Get("table")
	validates(t, "table", table.Schema, tableReply)
	// Every object is closed and requires every property, as a strict
	// structured output needs.
	for name, schema := range map[string][]byte{"map": MapSchema(types), "section": SectionSchema(types), "context": ContextSchema(), "overview": OverviewSchema()} {
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if props, ok := v["properties"].(map[string]any); ok {
					required, _ := v["required"].([]any)
					if v["additionalProperties"] != false || len(required) != len(props) {
						t.Errorf("%s: an object is not closed or does not require every property: %v", name, v["required"])
					}
				}
				for _, item := range v {
					walk(item)
				}
			case []any:
				for _, item := range v {
					walk(item)
				}
			}
		}
		var value any
		_ = json.Unmarshal(schema, &value)
		walk(value)
	}
}

func TestMapChecks(t *testing.T) {
	f := newFacts(testBundle(), nil)
	r := NewRegistry(nil)
	p, findings := checkMap([]byte(mapReplyJSON), f, r, false)
	if findings != "" {
		t.Fatalf("a valid map: %s", findings)
	}
	if len(p.Sections) != 3 || p.Sections[2].Level != nil || len(p.Tiles) != 3 || p.Hunks["tl_backoff"][0] != hunkA {
		t.Fatalf("plan: %+v", p)
	}
	bad := strings.NewReplacer(`"kind":"mechanical"`, `"kind":"logical"`, `"level":null`, `"level":null`,
		`"owner":"sec_api"`, `"owner":"sec_nope"`, `"title":"Retry failed jobs"`, `"title":"Retry, and requeue"`).Replace(mapReplyJSON)
	_, findings = checkMap([]byte(bad), f, r, false)
	for _, want := range []string{"/sections/2/level", "/tiles/2/owner", "sections[0] (sec_retry).title"} {
		if !strings.Contains(findings, want) {
			t.Errorf("findings lack %s:\n%s", want, findings)
		}
	}
	missing := strings.Replace(mapReplyJSON, `"hunks":["`+hunkD+`"]`, `"hunks":[]`, 1)
	if _, findings = checkMap([]byte(missing), f, r, false); !strings.Contains(findings, hunkD) {
		t.Errorf("an uncovered hunk is not reported: %s", findings)
	}
}

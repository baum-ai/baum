package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/review/tilecatalogue"
)

func TestSchemaWithoutTilesChangesNothingButTiles(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	without, err := schemaWithoutTiles(v.Schema)
	checkNoError(t, err)
	var got, want map[string]any
	checkNoError(t, json.Unmarshal(without, &got))
	checkNoError(t, json.Unmarshal(v.Schema, &want))
	defs := want["$defs"].(map[string]any)
	section := defs["section"].(map[string]any)["properties"].(map[string]any)
	delete(section, "tiles")
	delete(section["risks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any), "tiles")
	for name := range defs {
		if strings.HasPrefix(name, "tile") {
			delete(defs, name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("removing tiles changed more of the model schema than its tiles")
	}
}

func TestModelSchemaOffersOnlyTheAllowedTileKinds(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	all, err := v.ModelSchema(tilecatalogue.Kinds())
	checkNoError(t, err)
	assertSameDocumentJSON(t, v.Schema, all)

	for _, kind := range tilecatalogue.Kinds() {
		single, err := v.ModelSchema([]string{kind})
		checkNoError(t, err)
		var resolved jsonschema.Schema
		checkNoError(t, json.Unmarshal(single, &resolved))
		if _, err := resolved.Resolve(nil); err != nil {
			t.Fatalf("model schema offering only %s does not resolve: %v", kind, err)
		}
	}

	schema, err := v.ModelSchema([]string{"bug-fix", "architecture"})
	checkNoError(t, err)
	var resolved jsonschema.Schema
	checkNoError(t, json.Unmarshal(schema, &resolved))
	if _, err := resolved.Resolve(nil); err != nil {
		t.Fatalf("model schema with two tile kinds does not resolve: %v", err)
	}
	var root map[string]any
	checkNoError(t, json.Unmarshal(schema, &root))
	defs := root["$defs"].(map[string]any)
	for _, name := range []string{"tile", "tileCodeFile", "tileData_bug-fix", "tileData_architecture", "tileArchitectureNode"} {
		if defs[name] == nil {
			t.Fatalf("model schema lost %s", name)
		}
	}
	for _, name := range []string{"tileData_database-schema", "tileSchemaColumn", "tileData_api-contract", "tileData_access-model"} {
		if defs[name] != nil {
			t.Fatalf("model schema still offers %s", name)
		}
	}
	kinds := defs["tile"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"]
	if !reflect.DeepEqual(kinds, []any{"architecture", "bug-fix"}) {
		t.Fatalf("model schema tile kinds = %v, want architecture and bug-fix", kinds)
	}
}

func TestDocumentWithTileKinds(t *testing.T) {
	for _, tc := range []struct {
		name, document string
		kinds          []string
		want           string
	}{
		{"keeps allowed kinds", `{"sections":[{"tiles":[{"kind":"bug-fix"}]}]}`, []string{"bug-fix"}, `{"sections":[{"tiles":[{"kind":"bug-fix"}]}]}`},
		{"drops other kinds", `{"sections":[{"tiles":[{"kind":"bug-fix"},{"kind":"architecture"},{}],"risks":[{"tiles":[{"kind":"architecture"}]}]}]}`, []string{"bug-fix"}, `{"sections":[{"tiles":[{"kind":"bug-fix"}],"risks":[{}]}]}`},
		{"leaves a malformed list for the validator", `{"sections":[{"tiles":"bug-fix"}]}`, []string{"bug-fix"}, `{"sections":[{"tiles":"bug-fix"}]}`},
		{"drops every tile without kinds", `{"sections":[{"tiles":[{"kind":"bug-fix"}],"risks":[{"tiles":"x"}]}]}`, nil, `{"sections":[{"risks":[{}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSameDocumentJSON(t, []byte(tc.want), DocumentWithTileKinds([]byte(tc.document), tc.kinds))
		})
	}
}

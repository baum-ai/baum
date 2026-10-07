package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ModelSchema is the schema a run gives the model for the offered kinds.
// Without kinds it has no tiles at all. Otherwise the authored contract keeps
// only those kinds' tile variants, and each definition only the removed
// variants used, before the model-facing contract is derived from it.
//
// It is exported for hosts that run the generate-and-repair loop themselves.
func (v *DocumentValidator) ModelSchema(kinds []string) ([]byte, error) {
	if len(kinds) == 0 {
		return schemaWithoutTiles(v.Schema)
	}
	encoded, err := json.Marshal(v.strict)
	if err != nil {
		return nil, err
	}
	var contract map[string]any
	if err := json.Unmarshal(encoded, &contract); err != nil {
		return nil, err
	}
	defs, _ := contract["$defs"].(map[string]any)
	tile, _ := defs["tile"].(map[string]any)
	variants, _ := tile["oneOf"].([]any)
	if variants == nil {
		return nil, fmt.Errorf("review contract has no tile variants")
	}
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	withoutNewlyUnreferencedDefinitions(contract, defs, func() {
		var kept []any
		for _, variant := range variants {
			properties, _ := variant.(map[string]any)["properties"].(map[string]any)
			kind, _ := properties["kind"].(map[string]any)
			if name, _ := kind["const"].(string); allowed[name] {
				kept = append(kept, variant)
			}
		}
		tile["oneOf"] = kept
	})
	if len(tile["oneOf"].([]any)) == 0 {
		return nil, fmt.Errorf("review contract offers none of the tile kinds %s", strings.Join(kinds, ", "))
	}
	lenient, err := lenientContract(contract)
	if err != nil {
		return nil, err
	}
	return json.Marshal(lenient)
}

// schemaWithoutTiles is the model schema for a prompt that offers no tiles:
// every tiles property is gone, and so is each definition only a tile used,
// so the model is neither shown tiles nor able to return them. Everything
// else is the model schema unchanged.
func schemaWithoutTiles(schema []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, err
	}
	defs, _ := root["$defs"].(map[string]any)
	withoutNewlyUnreferencedDefinitions(root, defs, func() { dropTileProperties(root) })
	return json.Marshal(root)
}

// withoutNewlyUnreferencedDefinitions applies change to the schema, then
// removes each definition that was in use before it and no longer is. One the
// schema already left unreferenced stays as it was.
func withoutNewlyUnreferencedDefinitions(root, defs map[string]any, change func()) {
	before := referencedDefinitions(root, defs)
	change()
	after := referencedDefinitions(root, defs)
	for name := range before {
		if !after[name] {
			delete(defs, name)
		}
	}
}

// referencedDefinitions names every definition reachable from the schema's
// root through $ref, following references inside definitions too.
func referencedDefinitions(root map[string]any, defs map[string]any) map[string]bool {
	reachable := map[string]bool{}
	var visit func(node any)
	visit = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				if def, found := defs[name]; found && !reachable[name] {
					reachable[name] = true
					visit(def)
				}
			}
			for key, child := range v {
				if key != "$defs" {
					visit(child)
				}
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(root)
	return reachable
}

func dropTileProperties(node any) {
	switch v := node.(type) {
	case map[string]any:
		if properties, ok := v["properties"].(map[string]any); ok {
			if _, has := properties["tiles"]; has {
				delete(properties, "tiles")
				if required, ok := v["required"].([]any); ok {
					kept := required[:0]
					for _, name := range required {
						if name != "tiles" {
							kept = append(kept, name)
						}
					}
					v["required"] = kept
				}
			}
		}
		for _, child := range v {
			dropTileProperties(child)
		}
	case []any:
		for _, child := range v {
			dropTileProperties(child)
		}
	}
}

// DocumentWithTileKinds removes every tile a model returned whose kind is not
// offered, from its sections and their risks, and a tiles list left empty.
// A document that is not the expected shape is returned unchanged for the
// validator to report.
//
// It is exported for hosts that run the generate-and-repair loop themselves.
func DocumentWithTileKinds(document []byte, kinds []string) []byte {
	var value map[string]any
	if json.Unmarshal(document, &value) != nil {
		return document
	}
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	removed := false
	filter := func(item any) {
		owner, _ := item.(map[string]any)
		if owner == nil {
			return
		}
		tiles, has := owner["tiles"]
		if !has {
			return
		}
		list, isList := tiles.([]any)
		var kept []any
		for _, tile := range list {
			object, _ := tile.(map[string]any)
			if kind, _ := object["kind"].(string); allowed[kind] {
				kept = append(kept, tile)
			}
		}
		// With kinds offered, a list with nothing to drop and a value that is
		// not a list are left for the validator; with none, any tiles go.
		if len(kinds) > 0 && (!isList || len(kept) == len(list)) {
			return
		}
		removed = true
		if len(kept) == 0 {
			delete(owner, "tiles")
		} else {
			owner["tiles"] = kept
		}
	}
	sections, _ := value["sections"].([]any)
	for _, section := range sections {
		filter(section)
		if section, _ := section.(map[string]any); section != nil {
			risks, _ := section["risks"].([]any)
			for _, risk := range risks {
				filter(risk)
			}
		}
	}
	if !removed {
		return document
	}
	stripped, err := json.Marshal(value)
	if err != nil {
		return document
	}
	return stripped
}

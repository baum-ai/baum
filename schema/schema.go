// Package schema embeds the baum.review/1 JSON Schema and compiles it for
// validation.
//
// review-1.json is the schema itself, published at URL. The local tool and the
// hosted service both read it from this package and support the same
// features. Additive schema edits bump the revision recorded at
// $defs/schemaRevision/const, which a generated document echoes in
// generator.schema_revision.
package schema

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// URL is the schema's $id and the address it is published at.
const URL = "https://static.usebaum.com/schemas/review/1.json"

// Name is the value of a document's top-level "schema" field.
const Name = "baum.review/1"

// Document is the vendored schema file.
//
//go:embed review-1.json
var Document []byte

// Revision returns the schema revision at $defs/schemaRevision/const.
func Revision() (string, error) {
	root, err := decode()
	if err != nil {
		return "", err
	}
	obj, _ := root.(map[string]any)
	defs, _ := obj["$defs"].(map[string]any)
	rev, _ := defs["schemaRevision"].(map[string]any)
	value, ok := rev["const"].(string)
	if !ok || value == "" {
		return "", errors.New("schema: $defs/schemaRevision/const is missing")
	}
	return value, nil
}

// Compile returns the vendored schema compiled as JSON Schema draft 2020-12.
func Compile() (*jsonschema.Schema, error) {
	root, err := decode()
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(URL, root); err != nil {
		return nil, fmt.Errorf("schema: adding resource: %w", err)
	}
	compiled, err := compiler.Compile(URL)
	if err != nil {
		return nil, fmt.Errorf("schema: compiling: %w", err)
	}
	return compiled, nil
}

// Validate checks a decoded JSON instance against the schema. Decode the
// instance with jsonschema.UnmarshalJSON so numbers keep their precision.
func Validate(instance any) error {
	compiled, err := Compile()
	if err != nil {
		return err
	}
	return compiled.Validate(instance)
}

func decode() (any, error) {
	root, err := jsonschema.UnmarshalJSON(bytes.NewReader(Document))
	if err != nil {
		return nil, fmt.Errorf("schema: decoding review-1.json: %w", err)
	}
	return root, nil
}

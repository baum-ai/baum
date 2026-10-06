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
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
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

// Compile returns the vendored schema resolved as JSON Schema draft 2020-12.
// The result is safe for concurrent use.
func Compile() (*jsonschema.Resolved, error) {
	var root jsonschema.Schema
	if err := json.Unmarshal(Document, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding review-1.json: %w", err)
	}
	resolved, err := root.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema: resolving: %w", err)
	}
	return resolved, nil
}

// Validate checks a JSON document against the schema.
//
// The library does not assert "format", so date-time and uri values are
// validated by the caller where it matters. It also treats a json.Number as a
// string, so the document is decoded to float64 numbers rather than with
// UseNumber; integers beyond 2^53 lose precision, which no keyword in the
// schema depends on.
func Validate(document []byte) error {
	compiled, err := Compile()
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(document, &instance); err != nil {
		return fmt.Errorf("schema: decoding document: %w", err)
	}
	return compiled.Validate(instance)
}

func decode() (any, error) {
	var root any
	if err := json.Unmarshal(Document, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding review-1.json: %w", err)
	}
	return root, nil
}

package schema

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// Review2URL is the baum.review/2 schema's $id.
const Review2URL = "https://static.usebaum.com/schemas/review/2.json"

// Review2 is the baum.review/2 schema as written: the desktop app's review
// document (oss/reviewv2). A $ref it does not define names a definition of
// Document, or one of the parts of Document that mergedReview2 copies into
// $defs (review2Parts); use CompileReview2 or ValidateReview2 rather than
// resolving it alone.
//
//go:embed review-2.json
var Review2 []byte

// review2Parts are the parts of the baum.review/1 schema that are not
// definitions of their own, which review-2.json reuses under these names.
var review2Parts = []struct {
	name string
	path []string
}{
	{name: "reviewHeader", path: []string{"properties", "review"}},
	{name: "reviewStats", path: []string{"properties", "stats"}},
	{name: "reviewIndex", path: []string{"properties", "index"}},
	{name: "reviewChangeKind", path: []string{"properties", "summary", "properties", "change_kind"}},
	{name: "reviewRiskScore", path: []string{"properties", "summary", "properties", "risk_score"}},
	{name: "reviewDerived", path: []string{"$defs", "section", "properties", "derived"}},
}

// mergedReview2 is the baum.review/2 schema with baum.review/1's definitions,
// and the parts of it review-2.json reuses, merged into its $defs, so every
// reference resolves within one document. A name defined by both is an error.
var mergedReview2 = sync.OnceValues(func() ([]byte, error) {
	return mergeReview2(Review2, Document)
})

func mergeReview2(review2, review1 []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(review2, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding review-2.json: %w", err)
	}
	var v1 map[string]any
	if err := json.Unmarshal(review1, &v1); err != nil {
		return nil, fmt.Errorf("schema: decoding review-1.json: %w", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	if defs == nil {
		return nil, errors.New("schema: review-2.json has no $defs")
	}
	add := func(name string, definition any) error {
		if _, ok := defs[name]; ok {
			return fmt.Errorf("schema: $defs/%s is defined by both review-2.json and review-1.json", name)
		}
		defs[name] = definition
		return nil
	}
	v1Defs, _ := v1["$defs"].(map[string]any)
	for name, definition := range v1Defs {
		if err := add(name, definition); err != nil {
			return nil, err
		}
	}
	for _, part := range review2Parts {
		var node any = v1
		for _, key := range part.path {
			object, _ := node.(map[string]any)
			node = object[key]
		}
		if node == nil {
			return nil, fmt.Errorf("schema: review-1.json has no %v", part.path)
		}
		if err := add(part.name, node); err != nil {
			return nil, err
		}
	}
	return json.Marshal(root)
}

// CompileReview2 returns the merged baum.review/2 schema (mergedReview2)
// resolved as JSON Schema draft 2020-12. The result is safe for concurrent
// use.
func CompileReview2() (*jsonschema.Resolved, error) {
	return compiledReview2()
}

var compiledReview2 = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	merged, err := mergedReview2()
	if err != nil {
		return nil, err
	}
	var root jsonschema.Schema
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, fmt.Errorf("schema: decoding the merged review-2.json: %w", err)
	}
	resolved, err := root.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema: resolving review-2.json: %w", err)
	}
	return resolved, nil
})

// ValidateReview2 checks a JSON document against the baum.review/2 schema,
// with Validate's caveats (no "format" assertions, float64 numbers).
func ValidateReview2(document []byte) error {
	compiled, err := CompileReview2()
	if err != nil {
		return err
	}
	var instance any
	if err := json.Unmarshal(document, &instance); err != nil {
		return fmt.Errorf("schema: decoding document: %w", err)
	}
	return compiled.Validate(instance)
}

// Review2Revision returns the baum.review/2 schema revision at
// $defs/v2SchemaRevision/const, which a document records in
// review.generator.schema_revision.
func Review2Revision() (string, error) {
	var root struct {
		Defs struct {
			Revision struct {
				Const string `json:"const"`
			} `json:"v2SchemaRevision"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(Review2, &root); err != nil {
		return "", fmt.Errorf("schema: decoding review-2.json: %w", err)
	}
	if root.Defs.Revision.Const == "" {
		return "", errors.New("schema: $defs/v2SchemaRevision/const is missing")
	}
	return root.Defs.Revision.Const, nil
}

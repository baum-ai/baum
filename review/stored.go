package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// The schema keeps its $id when a constraint tightens in place, so a document
// stored before the change can fail the schema it is read with now. Every
// reader of a stored document (refresh, stored validation, the previous
// version a generation carries, and hosts that serve stored documents)
// normalizes it first with normalizeStored. Each rule rewrites only what the
// current schema rejects, deterministically and idempotently, and keeps the
// references between parts of the document consistent, so a document that
// passes the current schema is left unchanged and every reader sees the same
// rewrite. A stored document a rule cannot repair still fails validation.

// MaxIdentifierLength is the maxLength the review schema gives every
// identifier, identifier reference and risk category (schema revision
// 2026-10-07.1). Documents stored before it can hold longer ones.
const MaxIdentifierLength = 128

// identifierHashLength is how many hexadecimal digits of the identifier's
// SHA-256 ShortenIdentifier keeps: 48 bits, so two identifiers of one
// document that share their first characters stay distinct.
const identifierHashLength = 12

// ShortenIdentifier returns an identifier as normalizeStored rewrites it: one
// of at most MaxIdentifierLength characters unchanged, and a longer one as
// its first characters, without trailing underscores, then an underscore and
// the first identifierHashLength hexadecimal digits of the SHA-256 of the
// whole identifier, MaxIdentifierLength characters at most. The result keeps
// the identifier's prefix and character set, so it matches the same schema
// pattern, and shortening it again changes nothing.
//
// A host that keys its own records by a document identifier (a reader's read
// marks, a section approval) compares the stored key through this function,
// so a record made before the document was normalized still matches it.
func ShortenIdentifier(id string) string {
	if utf8.RuneCountInString(id) <= MaxIdentifierLength {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	kept := []rune(id)[:MaxIdentifierLength-identifierHashLength-1]
	return strings.TrimRight(string(kept), "_") + "_" + hex.EncodeToString(sum[:])[:identifierHashLength]
}

// ShortenStoredIdentifiers applies the identifier rule of the stored-document
// rules (normalizeStored) to an encoded document, for a host that serves
// stored documents and keys records of its own by their identifiers, so what
// it serves names sections and risks as a refresh will store them. The other
// rules only make a stored document pass validation, which a reader of the
// served document does not repeat. It returns document itself when it has no
// identifier over MaxIdentifierLength, and an error only when it has one and
// is not a JSON object.
func ShortenStoredIdentifiers(document []byte) ([]byte, error) {
	if !hasLongIdentifier(document) {
		return document, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("stored review is not valid JSON: %w", err)
	}
	if value == nil {
		return nil, fmt.Errorf("stored review must be an object")
	}
	schema, err := storedSchema()
	if err != nil {
		return nil, err
	}
	if !shortenStoredIdentifiers(value, schema) {
		return document, nil
	}
	return json.Marshal(value)
}

// hasLongIdentifier reports whether an encoded document has a string of
// more than MaxIdentifierLength identifier characters, which every
// identifier ShortenIdentifier would change is; most stored documents have
// none and are never decoded.
func hasLongIdentifier(document []byte) bool {
	run := -1
	for _, c := range document {
		switch {
		case c == '"':
			if run > MaxIdentifierLength {
				return true
			}
			run = 0
		case run >= 0 && (c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'):
			run++
		default:
			run = -1
		}
	}
	return false
}

// normalizeStored applies every stored-document rule to a decoded document
// and reports whether it changed anything. A rule for another constraint
// tightened in place (a tile shape the current schema rejects, for example)
// belongs here too.
func normalizeStored(document map[string]any) (bool, error) {
	schema, err := storedSchema()
	if err != nil {
		return false, err
	}
	titled := labelBlankSectionTitles(document)
	shortened := shortenStoredIdentifiers(document, schema)
	return titled || shortened, nil
}

// decodeStored decodes a stored document and normalizes it. It returns the
// decoded value and its encoding, which is data itself when no rule applied.
func decodeStored(data []byte, name string) (map[string]any, []byte, error) {
	value, err := decodeObject(data, name)
	if err != nil {
		return nil, nil, err
	}
	changed, err := normalizeStored(value)
	if err != nil {
		return nil, nil, err
	}
	if changed {
		if data, err = json.Marshal(value); err != nil {
			return nil, nil, err
		}
	}
	return value, data, nil
}

// storedSchema is the full review schema as a decoded tree, which the
// schema-driven rules walk alongside a document.
var storedSchema = sync.OnceValues(func() (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(reviewSchema, &schema); err != nil {
		return nil, err
	}
	return schema, nil
})

// shortenStoredIdentifiers rewrites with ShortenIdentifier every value over
// MaxIdentifierLength characters that the schema closes with an identifier
// pattern: identifiers, the references that name them and risk categories,
// wherever the schema puts them. A string elsewhere that equals an
// identifier it shortened, such as a summary attention reference, which the
// schema leaves open, is rewritten the same way, so every reference keeps
// naming what it named.
func shortenStoredIdentifiers(document map[string]any, schema map[string]any) bool {
	defs, _ := schema["$defs"].(map[string]any)
	shortened := map[string]string{}
	walk := storedIdentifierWalk{defs: defs, normalizer: &authoredNormalizer{defs: defs}, shortened: shortened}
	walk.value(document, schema)
	if len(shortened) == 0 {
		return false
	}
	replaceShortened(document, shortened)
	return true
}

type storedIdentifierWalk struct {
	defs       map[string]any
	normalizer *authoredNormalizer
	shortened  map[string]string
}

func (w storedIdentifierWalk) value(value any, schema map[string]any) any {
	schema = resolveSchema(schema, w.defs)
	switch v := value.(type) {
	case map[string]any:
		if branch := w.normalizer.branch(v, schema); branch != nil {
			schema = branch
		}
		props, _ := schema["properties"].(map[string]any)
		for key, child := range v {
			if prop, ok := props[key].(map[string]any); ok {
				v[key] = w.value(child, prop)
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i := range v {
				v[i] = w.value(v[i], items)
			}
		}
	case string:
		if utf8.RuneCountInString(v) > MaxIdentifierLength && w.identifier(schema) {
			short := ShortenIdentifier(v)
			w.shortened[v] = short
			return short
		}
	}
	return value
}

// identifier reports whether a string under schema is an identifier the
// schema bounds: an identifier pattern of its own, or of one of the branches
// of its union (an optional section reference is a section ID or null).
func (w storedIdentifierWalk) identifier(schema map[string]any) bool {
	candidates := []map[string]any{schema}
	for _, key := range []string{"anyOf", "oneOf"} {
		branches, _ := schema[key].([]any)
		for _, raw := range branches {
			if branch, ok := raw.(map[string]any); ok {
				candidates = append(candidates, resolveSchema(branch, w.defs))
			}
		}
	}
	for _, candidate := range candidates {
		pattern, _ := candidate["pattern"].(string)
		if _, bounded := candidate["maxLength"]; bounded && identifierPattern(pattern) != nil {
			return true
		}
	}
	return false
}

// replaceShortened rewrites every string value, not object key, that equals
// an identifier shortenStoredIdentifiers shortened.
func replaceShortened(value any, shortened map[string]string) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = replaceShortened(child, shortened)
		}
	case []any:
		for i := range v {
			v[i] = replaceShortened(v[i], shortened)
		}
	case string:
		if short, ok := shortened[v]; ok {
			return short
		}
	}
	return value
}

// StoredDocumentError reports that a stored document does not pass the
// current schema, or the document's own rules, even after normalizeStored.
// Refreshing it cannot succeed until it is regenerated or a stored-document
// rule repairs it, so a host can tell it apart from a failure of the refresh
// itself.
type StoredDocumentError struct {
	Err error
}

func (e *StoredDocumentError) Error() string {
	return "the stored review does not pass the current schema: " + e.Err.Error()
}

func (e *StoredDocumentError) Unwrap() error { return e.Err }

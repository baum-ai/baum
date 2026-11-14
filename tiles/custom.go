package tiles

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// A custom tile embeds its definition. It is ready when its definition and
// data are valid, and unavailable only when:
//
//  1. its definition does not have the definition's shape, or its name is
//     custom or a built-in type's;
//  2. its definition does not match its digest;
//  3. it names a kit major this version does not serve;
//  4. its component holds a NUL character (a definition that is not valid
//     UTF-8 fails the first check);
//  5. its data schema is not in the dialect (CheckDataSchema);
//  6. its data is larger than MaxDataBytes or fails its schema;
//  7. its data has more than MaxCitations citations, or a citation does not
//     resolve against the document.
//
// A draft's pending custom tile may have empty data. Its covers are the
// hunks its data cites.

// The sizes of the caches of checked definitions (by their encoded bytes),
// of compiled data schemas (by definition digest) and of data verdicts (by
// definition digest and the SHA-256 of the data).
const (
	definitionCacheSize = 256
	schemaCacheSize     = 128
	verdictCacheSize    = 1024
)

// The limits of one document's custom tiles, far above any page people
// read (a page has a few dozen tiles): a custom tile past one is
// unavailable, and so is one whose data the document's budget does not
// reach.
const (
	maxCustomTiles = 64
	// maxCustomDefinitionBytes bounds the encoded definitions of a
	// document's custom tiles together, a definition counted once per tile.
	maxCustomDefinitionBytes = 32 << 20
	// maxCustomDataBytes bounds their encoded data together.
	maxCustomDataBytes = 1 << 20
	// documentBudget bounds the validation steps of a document's custom
	// tiles together; each tile also has validationBudget of its own.
	documentBudget = 8 * validationBudget
)

// maxDefinitionRest bounds a definition without its component and data
// schema, encoded without whitespace: names, title, use-when line, fill
// instructions (8000 characters, which an encoder may escape) and sizes.
const maxDefinitionRest = 128 << 10

// checkedDefinition is what checking a definition leaves for its tiles. It
// keeps nothing of the component, so the cache stays small.
type checkedDefinition struct {
	problems []string
	digest   string
	sizes    []Size
	schema   *compiledSchema
}

// dataVerdict is what checking a custom tile's data against its schema
// found: its problems, or its citations, which resolve per document.
type dataVerdict struct {
	problems  []string
	citations []citation
}

type verdictKey struct {
	digest string
	data   [32]byte
}

// definitionShape is the tile schema's $defs/tileTypeDefinition, compiled
// for the package's own validator, whose failures quote nothing.
var definitionShape = sync.OnceValues(func() (*schemaNode, error) {
	shared, err := reserved()
	if err != nil {
		return nil, err
	}
	compiler := &schemaCompiler{defs: shared.allDefs, compiled: map[string]*schemaNode{}, pending: map[string]bool{}}
	return compiler.def("tileTypeDefinition")
})

// CheckDefinition checks a custom tile's encoded definition and returns it
// decoded with its problems: every unavailable cause that does not depend on
// the tile's data. A definition with problems is returned as far as it
// decodes.
func (v *Validator) CheckDefinition(definition json.RawMessage) (TypeDefinition, []string) {
	checked := v.definition(definition)
	// kit may be written as any integral number, 1.0 among them.
	var wire struct {
		TypeDefinition
		Kit float64 `json:"kit"`
	}
	if len(definition) <= MaxDefinitionBytes {
		_ = json.Unmarshal(definition, &wire)
	}
	def := wire.TypeDefinition
	def.Kit = int(wire.Kit)
	return def, slices.Clone(checked.problems)
}

// CheckData checks a custom tile's data against its encoded definition and
// returns its problems: the definition's, or else the data's size, shape
// and number of citations. Whether its citations resolve depends on the
// document (CheckTiles).
func (v *Validator) CheckData(definition, data json.RawMessage) []string {
	checked := v.definition(definition)
	if len(checked.problems) > 0 {
		return slices.Clone(checked.problems)
	}
	verdict, _ := v.verdict(checked, data, validationBudget)
	return slices.Clone(verdict.problems)
}

// definition checks an encoded definition, through the cache.
func (v *Validator) definition(raw json.RawMessage) *checkedDefinition {
	key := sha256.Sum256(raw)
	if checked, ok := v.definitions.get(key); ok {
		return checked
	}
	checked := v.checkDefinition(raw)
	v.definitions.put(key, checked)
	return checked
}

// checkDefinition checks a definition. The parts whose size is unbounded by
// the definition's shape (the data schema and everything but the component)
// are measured before the definition is decoded, which it is once.
func (v *Validator) checkDefinition(raw json.RawMessage) *checkedDefinition {
	fail := func(format string, args ...any) *checkedDefinition {
		return &checkedDefinition{problems: []string{fmt.Sprintf(format, args...)}}
	}
	if len(raw) > MaxDefinitionBytes {
		return fail("the definition is larger than %d MiB", MaxDefinitionBytes>>20)
	}
	if !utf8.Valid(raw) {
		return fail("the definition is not valid: not valid UTF-8")
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return fail("the definition is not valid: it is not a JSON object")
	}
	if encodedSize(top["data_schema"], maxDataSchemaBytes) > maxDataSchemaBytes {
		return fail("the data schema is larger than %d KiB encoded without whitespace; make it smaller", maxDataSchemaBytes>>10)
	}
	rest := 0
	for name, member := range top {
		switch name {
		case "data_schema":
		case "source":
			var source map[string]json.RawMessage
			if json.Unmarshal(member, &source) != nil {
				rest += encodedSize(member, maxDefinitionRest)
				continue
			}
			for sourceName, part := range source {
				if sourceName != "html" {
					rest += len(sourceName) + encodedSize(part, maxDefinitionRest)
				}
			}
		default:
			rest += len(name) + encodedSize(member, maxDefinitionRest)
		}
		if rest > maxDefinitionRest {
			return fail("the definition is larger than %d KiB without its HTML and data schema", maxDefinitionRest>>10)
		}
	}
	value, err := decodeData(raw)
	if err != nil {
		return fail("the definition is not valid: %s", strings.TrimPrefix(err.Error(), "tiles: "))
	}
	shape, err := definitionShape()
	if err != nil {
		return fail("the definition cannot be checked")
	}
	if err := (&validation{}).validate(shape, value, nil); err != nil {
		if err == errBudget {
			return fail("the definition is too large to check")
		}
		return fail("the definition is not valid: %v", err)
	}
	// The shape guarantees these members and their types.
	object := value.(map[string]any)
	name := object["name"].(string)
	html := object["source"].(map[string]any)["html"].(string)
	kit := object["kit"].(float64)
	var sizes []Size
	for _, size := range object["sizes"].([]any) {
		sizes = append(sizes, Size(size.(string)))
	}
	if name == TypeCustom {
		return fail("the type's name is %q, which is reserved; give the type a name of its own", name)
	}
	if _, ok := Builtin(name); ok {
		return fail("the type's name is %q, a built-in tile type's; give the type a name of its own", name)
	}
	digest, err := digestOf(value)
	if err != nil || digest != object["digest"] {
		return fail("the definition doesn't match its digest")
	}
	if !KitSupported(int(kit)) {
		return fail("this tile was made with a newer version of baum (tile kit %s; this version has up to %d)", ecmaNumber(kit), KitLatest)
	}
	if strings.ContainsRune(html, 0) {
		return fail("the definition's HTML contains a NUL character")
	}
	checked := &checkedDefinition{digest: digest, sizes: sizes}
	compiled, ok := v.schemas.get(digest)
	if !ok {
		var problems []string
		compiled, problems = compileDataSchema(top["data_schema"])
		if len(problems) > 0 {
			for _, problem := range problems {
				checked.problems = append(checked.problems, "data schema "+problem)
			}
			return checked
		}
		v.schemas.put(digest, compiled)
	}
	checked.schema = compiled
	return checked
}

// verdict checks data against a checked definition's schema within limit
// steps, through the cache, and returns the steps it took. A verdict cut
// short by a limit below the tile's own budget is the document's, not the
// data's, and is not cached.
func (v *Validator) verdict(checked *checkedDefinition, data json.RawMessage, limit int) (*dataVerdict, int) {
	key := verdictKey{digest: checked.digest, data: sha256.Sum256(data)}
	if verdict, ok := v.verdicts.get(key); ok {
		return verdict, 0
	}
	if limit <= 0 {
		return &dataVerdict{problems: []string{documentTooCostly}}, 0
	}
	val := &validation{limit: limit}
	citations, problems := checkDataWith(val, checked.schema, data)
	if val.steps > limit && limit < validationBudget {
		return &dataVerdict{problems: []string{documentTooCostly}}, val.steps
	}
	verdict := &dataVerdict{problems: problems, citations: citations}
	v.verdicts.put(key, verdict)
	return verdict, val.steps
}

func checkDataWith(v *validation, schema *compiledSchema, data json.RawMessage) ([]citation, []string) {
	if encodedSize(data, MaxDataBytes) > MaxDataBytes {
		return nil, []string{fmt.Sprintf("the data is larger than %d KiB", MaxDataBytes>>10)}
	}
	value, err := decodeData(data)
	if err != nil {
		return nil, []string{"the data is not valid JSON"}
	}
	switch err := v.validate(schema.root, value, nil); err {
	case nil:
	case errBudget:
		return nil, []string{tooCostly}
	default:
		return nil, []string{boundedProblem("the data does not match its schema: " + err.Error())}
	}
	citations, err := findCitations(value, v)
	switch {
	case err != nil:
		return nil, []string{tooCostly}
	case len(citations) > MaxCitations:
		return nil, []string{fmt.Sprintf("the data has more than %d citations", MaxCitations)}
	}
	return citations, nil
}

const (
	tooCostly         = "the data takes too much work to check against its schema; make the data or its schema smaller"
	documentTooCostly = "the review's custom tiles take too much work to check together; this one was not checked"
)

// customProblems checks a custom tile and returns its problems, or the
// hunks it covers.
func (v *Validator) customProblems(refs *tileRefs, tile *Tile) ([]string, []string) {
	if builtin, ok := Builtin(tile.ID); ok && builtin.Single {
		return []string{fmt.Sprintf("a custom tile's ID cannot be %q, a built-in tile's; give it another ID", tile.ID)}, nil
	}
	if problem := refs.admission(tile); problem != "" {
		return []string{problem}, nil
	}
	checked := v.definition(tile.Definition)
	if len(checked.problems) > 0 {
		return slices.Clone(checked.problems), nil
	}
	// A draft's custom tile is pending before its data is written.
	if tile.State == StatePending && isEmptyObject(tile.Data) {
		return nil, nil
	}
	verdict, steps := v.verdict(checked, tile.Data, min(validationBudget, documentBudget-refs.spent))
	refs.spent += steps
	if len(verdict.problems) > 0 {
		return slices.Clone(verdict.problems), nil
	}
	if len(verdict.citations) == 0 {
		return nil, nil
	}
	problems, covers := citationProblems(verdict.citations, refs.customRefs())
	if len(problems) > 0 {
		return problems, nil
	}
	return nil, covers
}

// admission returns why a document's custom tile is past the document's
// limits, or "". The tiles are counted in document order; one past a limit
// does not count towards it.
func (refs *tileRefs) admission(tile *Tile) string {
	if refs.refused == nil {
		refs.refused = map[*Tile]string{}
		tiles, definitionBytes, dataBytes := 0, 0, 0
		for i := range refs.doc.Tiles {
			t := &refs.doc.Tiles[i]
			if t.Type != TypeCustom {
				continue
			}
			switch {
			case tiles+1 > maxCustomTiles:
				refs.refused[t] = fmt.Sprintf("the review has more than %d custom tiles; this one is past the limit", maxCustomTiles)
			case definitionBytes+len(t.Definition) > maxCustomDefinitionBytes:
				refs.refused[t] = fmt.Sprintf("the review's custom tile definitions are larger than %d MiB together; this one is past the limit", maxCustomDefinitionBytes>>20)
			case dataBytes+len(t.Data) > maxCustomDataBytes:
				refs.refused[t] = fmt.Sprintf("the review's custom tile data is larger than %d MiB together; this one is past the limit", maxCustomDataBytes>>20)
			default:
				tiles++
				definitionBytes += len(t.Definition)
				dataBytes += len(t.Data)
			}
		}
	}
	return refs.refused[tile]
}

// definitionSizes returns the sizes a custom tile's definition declares, or
// nil when it has problems.
func definitionSizes(definition json.RawMessage) []Size {
	checked := defaultValidator.definition(definition)
	if len(checked.problems) > 0 {
		return nil
	}
	return checked.sizes
}

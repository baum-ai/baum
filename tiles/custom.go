package tiles

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

// The sizes of the caches of checked definitions (by their encoded bytes)
// and of compiled data schemas (by definition digest).
const (
	definitionCacheSize = 256
	schemaCacheSize     = 128
)

// checkedDefinition is what checking a definition leaves for its tiles. It
// keeps nothing of the component, so the cache stays small.
type checkedDefinition struct {
	problems []string
	schema   *compiledSchema
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
	var def TypeDefinition
	if len(definition) <= MaxDefinitionBytes {
		_ = json.Unmarshal(definition, &def)
	}
	return def, checked.problems
}

// CheckData checks a custom tile's data against its encoded definition and
// returns its problems: the definition's, or else the data's size, shape
// and number of citations. Whether its citations resolve depends on the
// document (CheckTiles).
func (v *Validator) CheckData(definition, data json.RawMessage) []string {
	checked := v.definition(definition)
	if len(checked.problems) > 0 {
		return checked.problems
	}
	_, problems := checkData(checked.schema, data)
	return problems
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

func (v *Validator) checkDefinition(raw json.RawMessage) *checkedDefinition {
	fail := func(format string, args ...any) *checkedDefinition {
		return &checkedDefinition{problems: []string{fmt.Sprintf(format, args...)}}
	}
	if len(raw) > MaxDefinitionBytes {
		return fail("the definition is larger than %d MiB", MaxDefinitionBytes>>20)
	}
	value, err := decodeData(raw)
	if err != nil {
		return fail("the definition is not valid: %s", strings.TrimPrefix(err.Error(), "tiles: "))
	}
	shape, err := definitionShape()
	if err != nil {
		return fail("the definition cannot be checked: %v", err)
	}
	if err := (&validation{}).validate(shape, value, nil); err != nil {
		if err == errBudget {
			return fail("the definition is too large to check")
		}
		return fail("the definition is not valid: %v", err)
	}
	var def TypeDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		return fail("the definition is not valid: %v", err)
	}
	if def.Name == TypeCustom {
		return fail("the type's name is %q, which is reserved; give the type a name of its own", def.Name)
	}
	if _, ok := Builtin(def.Name); ok {
		return fail("the type's name is %q, a built-in tile type's; give the type a name of its own", def.Name)
	}
	digest, err := DefinitionDigest(raw)
	if err != nil || digest != def.Digest {
		return fail("the definition doesn't match its digest")
	}
	if !KitSupported(def.Kit) {
		return fail("this tile was made with a newer version of baum (tile kit %d; this version has up to %d)", def.Kit, KitLatest)
	}
	if strings.ContainsRune(def.Source.HTML, 0) {
		return fail("the definition's HTML contains a NUL character")
	}
	checked := &checkedDefinition{}
	compiled, ok := v.schemas.get(digest)
	if !ok {
		var problems []string
		compiled, problems = compileDataSchema(def.DataSchema)
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

// checkData validates data against a compiled data schema and returns its
// citations.
func checkData(schema *compiledSchema, data json.RawMessage) ([]citation, []string) {
	return checkDataWith(&validation{}, schema, data)
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
		return nil, []string{"the data does not match its schema: " + err.Error()}
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

const tooCostly = "the data takes too much work to check against its schema; make the data or its schema smaller"

// customProblems checks a custom tile and returns its problems, or the
// hunks it covers.
func (v *Validator) customProblems(refs *tileRefs, tile *Tile) ([]string, []string) {
	checked := v.definition(tile.Definition)
	if len(checked.problems) > 0 {
		return checked.problems, nil
	}
	// A draft's custom tile is pending before its data is written.
	if tile.State == StatePending && isEmptyObject(tile.Data) {
		return nil, nil
	}
	citations, problems := checkData(checked.schema, tile.Data)
	if len(problems) > 0 {
		return problems, nil
	}
	if len(citations) == 0 {
		return nil, nil
	}
	problems, covers := citationProblems(citations, refs.customRefs())
	if len(problems) > 0 {
		return problems, nil
	}
	return nil, covers
}

// definitionSizes returns the sizes a custom tile's definition declares,
// or nil.
func definitionSizes(definition json.RawMessage) []Size {
	var value struct {
		Sizes []Size `json:"sizes"`
	}
	if len(definition) > MaxDefinitionBytes || json.Unmarshal(definition, &value) != nil {
		return nil
	}
	for _, size := range value.Sizes {
		if size != SizeFull && size != SizeHalf && size != SizeThird {
			return nil
		}
	}
	return value.Sizes
}

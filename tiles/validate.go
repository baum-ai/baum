package tiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/schema"
)

// Validator checks tile documents. Its failures come at two levels:
//
//   - A document-level failure rejects the document (a *DocumentError): the
//     envelope and substrate shapes, unique change, risk, evidence, tile and
//     group IDs, risks and evidence naming an existing change, the hunks of
//     current changes being in the index, the layout placing every tile
//     exactly once with known and contiguous groups, landing within the
//     entries, and at most one tile of each single type.
//   - A tile-level failure marks only that tile unavailable, with its
//     problems: a single tile not under its type's ID, data that does not
//     have its type's shape (only a draft's pending context tile may have
//     empty data), references in the data that do not resolve, a decision ID
//     repeated in the decisions tile, an unknown type, and a custom tile
//     whose definition, data or citations fail (see CheckDefinition and
//     CheckData), which also gets its covers: the hunks its data cites.
//
// A host serving documents it converted runs only the cheap tile-level checks
// (CheckTiles); Validate runs everything. A Validator is safe for concurrent
// use.
type Validator struct {
	document func() (*jsonschema.Resolved, error)
	data     func() (map[string]*jsonschema.Resolved, error)
	// definitions caches checked custom tile definitions by the SHA-256 of
	// their encoding, and schemas their compiled data schemas by digest: a
	// saved review embeds a definition in every tile of its type.
	definitions *lru[[32]byte, *checkedDefinition]
	schemas     *lru[string, *compiledSchema]
	// verdicts caches what a custom tile's data checks to, by definition
	// digest and the data's SHA-256, so the same document costs nothing
	// to check again.
	verdicts *lru[verdictKey, *dataVerdict]
}

// NewValidator returns a Validator. The schemas are compiled on first use.
func NewValidator() *Validator {
	return &Validator{
		document: sync.OnceValues(schema.CompileTiles),
		data: sync.OnceValues(func() (map[string]*jsonschema.Resolved, error) {
			data := make(map[string]*jsonschema.Resolved, len(Builtins))
			for _, builtin := range Builtins {
				resolved, err := schema.CompileTilesDefinition("tileData_" + builtin.Name)
				if err != nil {
					return nil, err
				}
				data[builtin.Name] = resolved
			}
			return data, nil
		}),
		definitions: newLRU[[32]byte, *checkedDefinition](definitionCacheSize),
		schemas:     newLRU[string, *compiledSchema](schemaCacheSize),
		verdicts:    newLRU[verdictKey, *dataVerdict](verdictCacheSize),
	}
}

// DocumentError reports a document-level failure.
type DocumentError struct {
	Problems []string
}

func (e *DocumentError) Error() string {
	return "tiles: invalid document: " + strings.Join(e.Problems, "; ")
}

// Validate checks an encoded tile document against the schema and the
// document-level rules and returns it decoded, with every tile that fails a
// tile-level check marked unavailable.
func (v *Validator) Validate(data []byte) (*Document, error) {
	compiled, err := v.document()
	if err != nil {
		return nil, err
	}
	// The schema library reads a json.Number as a string, so the instance is
	// decoded with float64 numbers, as schema.Validate does.
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return nil, &DocumentError{Problems: []string{"not valid JSON: " + err.Error()}}
	}
	if err := compiled.Validate(instance); err != nil {
		return nil, &DocumentError{Problems: []string{conciseError(err)}}
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, &DocumentError{Problems: []string{err.Error()}}
	}
	if err := v.CheckDocument(&doc); err != nil {
		return nil, err
	}
	if err := v.CheckTiles(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// CheckDocument applies the document-level rules other than the schema to a
// decoded document, and returns a *DocumentError listing every failure.
func (v *Validator) CheckDocument(doc *Document) error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if doc.Schema != Name {
		add("schema is %q, not %q", doc.Schema, Name)
	}

	changes := map[string]*Change{}
	for i := range doc.Changes {
		change := &doc.Changes[i]
		if changes[change.ID] != nil {
			add("change %s is not unique", change.ID)
		}
		changes[change.ID] = change
	}
	unique := func(kind string, ids []string) {
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				add("%s %s is not unique", kind, id)
			}
			seen[id] = true
		}
	}
	var riskIDs, evidenceIDs, tileIDs, groupIDs []string
	for _, risk := range doc.Risks {
		riskIDs = append(riskIDs, risk.ID)
		if changes[risk.Change] == nil {
			add("risk %s names change %s, which the document does not have", risk.ID, risk.Change)
		}
	}
	for _, item := range doc.Evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
		if changes[item.Change] == nil {
			add("evidence %s names change %s, which the document does not have", item.ID, item.Change)
		}
	}
	for _, tile := range doc.Tiles {
		tileIDs = append(tileIDs, tile.ID)
	}
	for _, group := range doc.Layout.Groups {
		groupIDs = append(groupIDs, group.ID)
	}
	unique("risk", riskIDs)
	unique("evidence", evidenceIDs)
	unique("tile", tileIDs)
	unique("group", groupIDs)

	indexed, err := indexHunks(doc.Index)
	if err != nil {
		add("index: %v", err)
	}
	for _, change := range doc.Changes {
		if !currentChange(change) {
			continue
		}
		for _, hunk := range change.Hunks {
			if indexed != nil && !indexed[hunk] {
				add("change %s holds hunk %s, which the index does not list", change.ID, hunk)
			}
		}
	}

	singles := map[string]int{}
	for _, tile := range doc.Tiles {
		if builtin, ok := Builtin(tile.Type); ok && builtin.Single {
			if singles[tile.Type]++; singles[tile.Type] == 2 {
				add("more than one %s tile", tile.Type)
			}
		}
	}

	problems = append(problems, layoutProblems(doc)...)
	if len(problems) > 0 {
		return &DocumentError{Problems: problems}
	}
	return nil
}

func layoutProblems(doc *Document) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	tiles := map[string]bool{}
	for _, tile := range doc.Tiles {
		tiles[tile.ID] = true
	}
	groups := map[string]bool{}
	for _, group := range doc.Layout.Groups {
		groups[group.ID] = true
	}
	placed := map[string]int{}
	closed := map[string]bool{}
	previous := ""
	for _, entry := range doc.Layout.Entries {
		placed[entry.Tile]++
		if !tiles[entry.Tile] {
			add("the layout places tile %s, which the document does not have", entry.Tile)
		}
		if entry.Group != "" && !groups[entry.Group] {
			add("the layout names group %s, which it does not define", entry.Group)
		}
		if entry.Group != previous {
			if previous != "" {
				closed[previous] = true
			}
			if entry.Group != "" && closed[entry.Group] {
				add("the members of group %s are not contiguous", entry.Group)
			}
		}
		previous = entry.Group
	}
	for _, tile := range doc.Tiles {
		switch placed[tile.ID] {
		case 1:
		case 0:
			add("the layout does not place tile %s", tile.ID)
		default:
			add("the layout places tile %s more than once", tile.ID)
		}
	}
	if doc.Layout.Landing > len(doc.Layout.Entries) {
		add("landing %d is past the %d entries", doc.Layout.Landing, len(doc.Layout.Entries))
	}
	return problems
}

func indexHunks(index json.RawMessage) (map[string]bool, error) {
	var value struct {
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	}
	if err := json.Unmarshal(index, &value); err != nil {
		return nil, err
	}
	hunks := make(map[string]bool, len(value.Hunks))
	for _, hunk := range value.Hunks {
		hunks[hunk.ID] = true
	}
	return hunks, nil
}

// derivedView is the part of a change's derived fields the tiles package
// reads.
type derivedView struct {
	State       string `json:"state"`
	ReviewScore *int   `json:"review_score"`
}

const stateStale = "stale"

func changeDerived(change Change) derivedView {
	var derived derivedView
	_ = json.Unmarshal(change.Derived, &derived)
	return derived
}

func currentChange(change Change) bool {
	return changeDerived(change).State != stateStale
}

// CheckTiles applies the tile-level checks and marks every tile that fails
// one unavailable, with its problems. It sets each custom tile's covers. It
// fails only when the schemas cannot be compiled.
func (v *Validator) CheckTiles(doc *Document) error {
	data, err := v.data()
	if err != nil {
		return err
	}
	refs := newTileRefs(doc)
	for i := range doc.Tiles {
		tile := &doc.Tiles[i]
		problems, covers := v.tileProblems(data, refs, tile)
		if tile.Type == TypeCustom {
			tile.Covers = covers
		}
		if len(problems) > 0 {
			tile.State = StateUnavailable
			tile.Problems = problems
		}
	}
	return nil
}

// TileProblems returns the tile-level problems of one tile of a document
// without marking it.
func (v *Validator) TileProblems(doc *Document, tile *Tile) ([]string, error) {
	data, err := v.data()
	if err != nil {
		return nil, err
	}
	problems, _ := v.tileProblems(data, newTileRefs(doc), tile)
	return problems, nil
}

// tileRefs is what a tile's data may refer to.
type tileRefs struct {
	changes map[string]Change
	current map[string]bool
	risks   map[string]Risk
	// everythingElse is the change the everything_else tile shows.
	everythingElse string
	doc            *Document
	// custom is what custom tiles' citations resolve to, built when the
	// first custom tile needs it.
	custom *customRefs
	// refused holds why each custom tile past the document's limits is,
	// built when the first custom tile is checked; spent is the validation
	// steps its custom tiles took so far.
	refused map[*Tile]string
	spent   int
}

func (refs *tileRefs) customRefs() *customRefs {
	if refs.custom == nil {
		refs.custom = newCustomRefs(refs.doc)
	}
	return refs.custom
}

func newTileRefs(doc *Document) *tileRefs {
	refs := &tileRefs{changes: map[string]Change{}, current: map[string]bool{}, risks: map[string]Risk{}, doc: doc}
	for _, tile := range doc.Tiles {
		if tile.Type == TypeEverythingElse {
			var value EverythingElseData
			if json.Unmarshal(tile.Data, &value) == nil && value.Change != nil {
				refs.everythingElse = *value.Change
			}
		}
	}
	for _, change := range doc.Changes {
		refs.changes[change.ID] = change
		refs.current[change.ID] = currentChange(change)
	}
	for _, risk := range doc.Risks {
		refs.risks[risk.ID] = risk
	}
	return refs
}

// tileProblems returns a tile's problems and, for a custom tile without
// any, the hunks it covers.
func (v *Validator) tileProblems(data map[string]*jsonschema.Resolved, refs *tileRefs, tile *Tile) ([]string, []string) {
	if tile.Type == TypeCustom {
		return v.customProblems(refs, tile)
	}
	return builtinProblems(data, refs, tile), nil
}

func builtinProblems(data map[string]*jsonschema.Resolved, refs *tileRefs, tile *Tile) []string {
	builtin, ok := Builtin(tile.Type)
	if !ok {
		return []string{fmt.Sprintf("tile type %q is unknown", tile.Type)}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if builtin.Single && tile.ID != tile.Type {
		add("a %s tile's ID must be %q", tile.Type, tile.Type)
	}
	// A draft's context tile is pending before the context is written, and
	// has no data yet; every other tile has its data.
	if tile.Type == TypeContext && tile.State == StatePending && isEmptyObject(tile.Data) {
		return problems
	}
	var instance any
	if err := json.Unmarshal(tile.Data, &instance); err != nil {
		return append(problems, "data is not valid JSON")
	}
	if err := data[tile.Type].Validate(instance); err != nil {
		return append(problems, fmt.Sprintf("data does not have the %s tile's shape: %s", tile.Type, conciseError(err)))
	}
	change := func(id, what string) {
		if _, ok := refs.changes[id]; !ok {
			add("%s names change %s, which the document does not have", what, id)
		}
	}
	origin := func(item itemPlace, what string) {
		if item.Change != "" {
			change(item.Change, what)
		}
		if item.Origin != nil {
			change(item.Origin.Change, what+" origin")
		}
	}
	switch tile.Type {
	case TypeChange:
		var value ChangeData
		_ = json.Unmarshal(tile.Data, &value)
		if tile.ID != value.Change {
			add("a change tile's ID must be its change's")
		}
		if c, ok := refs.changes[value.Change]; !ok {
			change(value.Change, "the tile")
		} else if c.ID == refs.everythingElse {
			add("a change tile shows change %s, which everything_else shows", c.ID)
		}
	case TypeEverythingElse:
		var value EverythingElseData
		_ = json.Unmarshal(tile.Data, &value)
		if value.Change != nil {
			if c, ok := refs.changes[*value.Change]; !ok {
				change(*value.Change, "the tile")
			} else if c.Kind != KindMechanical || !refs.current[c.ID] {
				add("everything_else shows change %s, which is not the current mechanical change", c.ID)
			}
		}
	case TypeRisks:
		var value RisksData
		_ = json.Unmarshal(tile.Data, &value)
		for _, id := range value.Ranked {
			risk, ok := refs.risks[id]
			switch {
			case !ok:
				add("ranked risk %s is not in the document", id)
			case risk.State != "active":
				add("ranked risk %s is not active", id)
			case !refs.current[risk.Change]:
				add("ranked risk %s belongs to an obsolete change", id)
			}
		}
	case TypeIntent:
		var value struct {
			Lines []itemPlace `json:"lines"`
		}
		_ = json.Unmarshal(tile.Data, &value)
		for _, line := range value.Lines {
			origin(line, "an intent line")
		}
	case TypeDecisions:
		var value struct {
			Items []itemPlace `json:"items"`
		}
		_ = json.Unmarshal(tile.Data, &value)
		ids := map[string]bool{}
		for _, item := range value.Items {
			origin(item, "a decision")
			if ids[item.ID] {
				add("decision %s is not unique", item.ID)
			}
			ids[item.ID] = true
		}
	case TypeMedia:
		var value struct {
			Items []itemPlace `json:"items"`
		}
		_ = json.Unmarshal(tile.Data, &value)
		for _, item := range value.Items {
			origin(item, "a media item")
		}
	}
	return problems
}

// itemPlace is where an item of an aggregate tile belongs.
type itemPlace struct {
	ID     string       `json:"id"`
	Change string       `json:"change"`
	Origin *BlockOrigin `json:"origin"`
}

func isEmptyObject(data json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("{}"))
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// conciseError returns the first line of a schema error, bounded.
func conciseError(err error) string {
	var text string
	if err != nil {
		text = err.Error()
	}
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = line
	}
	return truncate(text, 300)
}

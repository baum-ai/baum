package tiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// A review's extra tiles are the tiles an older tile review's analysis
// added to what the review says: library tiles (a library type with its data) and authored
// tiles (exhibits: a table, code excerpt or prose the analysis wrote). The
// review view composition (tiles/view) stores them; this file checks them
// against the tile document converted from their review, which is the
// substrate their citations resolve in.

// FixKind names a repair NormalizeExtras made.
type FixKind string

// The fixes.
const (
	FixLibraryInvalid   FixKind = "library_invalid"
	FixLibraryDuplicate FixKind = "library_duplicate"
	FixLibraryLimit     FixKind = "library_limit"
	FixAuthoredInvalid  FixKind = "authored_invalid"
	FixAuthoredLimit    FixKind = "authored_limit"
	FixRenamed          FixKind = "renamed"
	FixTitle            FixKind = "title"
	// FixInputLimit counts what was dropped unread past an input bound.
	FixInputLimit FixKind = "input_limit"
	// FixControl is text with control characters, which are removed.
	FixControl FixKind = "control"
	// FixUnusable is reported when the document cannot take extra tiles.
	FixUnusable FixKind = "unusable"
)

// Fix is one repair. Tile is the tile's ID when it is a plain identifier;
// Detail is the host's own text, which never quotes what the model wrote.
type Fix struct {
	Kind   FixKind `json:"kind"`
	Tile   string  `json:"tile,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

// Report lists the fixes of a normalisation, in the order they were made:
// the first MaxReportedFixes of them, and how many there were in all.
type Report struct {
	Fixes []Fix `json:"fixes,omitempty"`
	Total int   `json:"total,omitempty"`
	// counts counts every fix by kind, those past MaxReportedFixes too.
	counts map[FixKind]int
}

// MaxReportedFixes bounds the fixes a Report lists; Total and Counts count
// them all.
const MaxReportedFixes = 200

// Counts returns how many fixes of each kind the normalisation made, or nil.
func (r Report) Counts() map[FixKind]int {
	if r.counts != nil {
		return maps.Clone(r.counts)
	}
	if len(r.Fixes) == 0 {
		return nil
	}
	counts := map[FixKind]int{}
	for _, fix := range r.Fixes {
		counts[fix.Kind]++
	}
	return counts
}

// Messages returns one plain sentence per fix, for people and agents.
func (r Report) Messages() []string {
	messages := make([]string, len(r.Fixes))
	for i, fix := range r.Fixes {
		messages[i] = fix.Message()
	}
	return messages
}

// Message says what a fix repaired.
func (f Fix) Message() string {
	name := func(noun, article string) string {
		if f.Tile != "" {
			return fmt.Sprintf("%s %q", noun, f.Tile)
		}
		return article + " " + noun
	}
	tile := name("tile", "a")
	var text string
	switch f.Kind {
	case FixLibraryInvalid:
		text = name("library tile", "a") + " fails its checks and is left out"
	case FixLibraryDuplicate:
		text = name("library tile", "a") + " has the ID of another tile and is left out"
	case FixLibraryLimit:
		text = fmt.Sprintf("there are more than %d library tiles; the rest are left out", MaxLibraryTiles)
	case FixAuthoredInvalid:
		text = name("authored tile", "an") + " fails its checks and is left out"
	case FixAuthoredLimit:
		text = fmt.Sprintf("there are more than %d authored tiles; the rest are left out", MaxAuthored)
	case FixRenamed:
		text = fmt.Sprintf("%s has the ID of another tile; it is renamed", tile)
	case FixTitle:
		text = fmt.Sprintf("the title of %s is blank, too long or on several lines; it is tidied", tile)
	case FixInputLimit:
		text = "there are far more tiles than may be held; the rest is not read"
	case FixControl:
		text = fmt.Sprintf("the text of %s holds control characters; they are removed", tile)
	case FixUnusable:
		text = "the tiles cannot be checked against this document"
	default:
		text = string(f.Kind)
	}
	if f.Detail != "" {
		text += ": " + f.Detail
	}
	return text
}

// plainID returns an ID to name in a fix, or "".
func plainID(id string) string {
	if isPlainName(id) {
		return id
	}
	return ""
}

func unusable(err error) Report {
	return Report{Fixes: []Fix{{Kind: FixUnusable, Detail: err.Error()}}, Total: 1, counts: map[FixKind]int{FixUnusable: 1}}
}

// NormalizeExtras repairs a review's extra tiles, as the analysis wrote
// them, against the tile document converted from the review, and reports
// every fix. The result is a fixed point: normalising it again changes
// nothing. The rules, in order:
//
//  0. Bounds: only the first few dozen of each part are read.
//  1. Library tiles: a tile whose definition does not decode to a name, or
//     whose ID (LibraryTileID) another tile of the document or an earlier
//     library tile has, is dropped; so is one whose definition, data or
//     citations fail the custom tile checks. The first MaxLibraryTiles that
//     pass are kept. A pending tile's data is empty.
//  2. Authored tiles: the ID is table_, code_ or prose_ and a slug, the
//     first MaxAuthored that pass their checks, the data a JSON object of at
//     most MaxDataBytes without an origin, with the type's shape, citing at
//     least one hunk or file of the review, every citation resolving.
//  3. IDs: an authored tile whose ID another tile has gets the first free
//     numeric suffix (_2, _3, …).
//  4. Text: control characters are removed from titles and prose (a prose
//     tile keeps tabs and newlines); titles are on one line, at most
//     MaxComposedTitle characters, the type's catalogue title when blank.
//
// Fixes name a tile only by an ID that is a plain identifier and never quote
// what the tiles hold.
func NormalizeExtras(doc *Document, library []LibraryTile, authored []AuthoredTile) ([]LibraryTile, []AuthoredTile, Report) {
	cm := newComposer(doc, modeWrite)
	if err := cm.prepare(library, authored); err != nil {
		return nil, nil, unusable(err)
	}
	var lib []LibraryTile
	for _, cand := range cm.library {
		lib = append(lib, cand.library)
	}
	var auth []AuthoredTile
	for _, cand := range cm.authored {
		auth = append(auth, cand.authored)
	}
	return lib, auth, cm.report
}

// ExtraTiles returns a review's extra tiles as tiles of the document
// converted from it: each checked, with the hunks it covers; a tile that
// fails its checks is unavailable with its problems, as a stored custom
// tile is, rather than dropped, and a pending library tile is pending. Only
// tiles NormalizeExtras returned, or the host's own (a library tile the
// person added), should be passed: a model's are normalised first.
func ExtraTiles(doc *Document, library []LibraryTile, authored []AuthoredTile) ([]Tile, error) {
	if doc == nil {
		return nil, errors.New("tiles: no document")
	}
	cm := newComposer(doc, modeRead)
	if err := cm.prepare(library, authored); err != nil {
		return nil, err
	}
	var out []Tile
	for _, cands := range [][]candidate{cm.library, cm.authored} {
		for _, cand := range cands {
			out = append(out, cand.tile)
		}
	}
	return out, nil
}

type composeMode int

const (
	// modeWrite drops a tile that fails its checks.
	modeWrite composeMode = iota
	// modeRead keeps a library or authored tile that fails its checks,
	// unavailable with its problems, as a stored custom tile is.
	modeRead
)

var (
	authoredID = regexp.MustCompile(`^(table|code|prose)_[a-z0-9_]{1,64}$`)
	tileID     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
)

type composer struct {
	doc    *Document
	mode   composeMode
	report Report
	byID   map[string]*Tile
	// tiles are the extra tiles once normalised, by kind.
	library  []candidate
	authored []candidate
}

// candidate is an extra tile being normalised.
type candidate struct {
	tile     Tile
	library  LibraryTile
	authored AuthoredTile
}

func newComposer(doc *Document, mode composeMode) *composer {
	cm := &composer{doc: doc, mode: mode, byID: map[string]*Tile{}}
	for i := range doc.Tiles {
		cm.byID[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	return cm
}

func (cm *composer) fix(kind FixKind, id, detail string) {
	cm.fixes(kind, 1)
	cm.list(Fix{Kind: kind, Tile: plainID(id), Detail: detail})
}

// list lists a fix while the report has room.
func (cm *composer) list(fix Fix) {
	if len(cm.report.Fixes) < MaxReportedFixes {
		cm.report.Fixes = append(cm.report.Fixes, fix)
	}
}

// fixes counts n fixes of a kind without listing them.
func (cm *composer) fixes(kind FixKind, n int) {
	if n <= 0 {
		return
	}
	if cm.report.counts == nil {
		cm.report.counts = map[FixKind]int{}
	}
	cm.report.counts[kind] += n
	cm.report.Total += n
}

// The most of each part a normalisation looks at, generous multiples of
// what a review may hold; the rest is dropped unread and counted as
// FixInputLimit.
const (
	maxInputLibrary  = 64
	maxInputAuthored = 64
)

// head returns the first limit items, counting the rest as dropped.
func head[T any](cm *composer, items []T, limit int) []T {
	if len(items) > limit {
		cm.fixes(FixInputLimit, len(items)-limit)
		cm.list(Fix{Kind: FixInputLimit, Detail: fmt.Sprintf("%d items", len(items)-limit)})
		return items[:limit]
	}
	return items
}

// prepare normalises the extra tiles.
func (cm *composer) prepare(library []LibraryTile, authored []AuthoredTile) error {
	if cm.doc.Origin.Kind != OriginConverted {
		return errors.New("tiles: extra tiles apply only to a converted document")
	}
	cm.libraryTiles(head(cm, library, maxInputLibrary))
	cm.authoredTiles(head(cm, authored, maxInputAuthored))
	cm.uniqueIDs()
	return cm.check()
}

// taken reports whether a converted tile has the ID.
func (cm *composer) taken(id string) bool { return cm.byID[id] != nil }

func (cm *composer) libraryTiles(library []LibraryTile) {
	names := map[string]bool{}
	for _, lib := range library {
		def, problems := defaultValidator.CheckDefinition(lib.Definition)
		name := def.Name
		if !tileID.MatchString(name) || len(name) > 64 {
			cm.fix(FixLibraryInvalid, "", "its definition has no valid name")
			continue
		}
		if len(problems) > 0 && cm.mode == modeWrite {
			cm.fix(FixLibraryInvalid, name, problems[0])
			continue
		}
		id := LibraryTileID(name)
		if cm.taken(id) || names[id] {
			cm.fix(FixLibraryDuplicate, name, "")
			continue
		}
		names[id] = true
		lib.Definition = bytes.Clone(lib.Definition)
		if lib.Pending || len(bytes.TrimSpace(lib.Data)) == 0 {
			lib.Data = json.RawMessage(`{}`)
		}
		title := strings.TrimSpace(strings.Join(strings.Fields(stripControls(def.Title, false)), " "))
		if title == "" {
			title = name
		}
		state := StateReady
		if lib.Pending {
			state = StatePending
		}
		cm.library = append(cm.library, candidate{library: lib, tile: Tile{
			ID: id, Type: TypeCustom, Title: truncate(title, maxTitle), Data: lib.Data, State: state, Definition: lib.Definition,
		}})
	}
}

// reservedPrefixes start the IDs the host gives change tiles and authored
// tiles. A library type whose name starts with one gets its tile the ID
// "library_" and its name, so it never takes such an ID from another
// subject, in this version or the next.
var reservedPrefixes = []string{"sec_", "table_", "code_", "prose_", "diagram_"}

// LibraryTileID returns the ID of a library type's tile: its name, or
// "library_" and its name for a name that starts as a change tile's or an
// authored tile's ID does.
func LibraryTileID(name string) string {
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(name, p) {
			return "library_" + name
		}
	}
	return name
}

// composedHeadline is an authored tile's headline: a table's row count, a
// code excerpt's caption or a prose tile's first sentence.
func composedHeadline(kind string, fields map[string]json.RawMessage) string {
	return stripControls(rawHeadline(kind, fields), false)
}

func rawHeadline(kind string, fields map[string]json.RawMessage) string {
	switch kind {
	case TypeTable:
		var rows []json.RawMessage
		_ = json.Unmarshal(fields["rows"], &rows)
		return countHeadline(len(rows), "row", "rows")
	case TypeCode:
		var caption string
		_ = json.Unmarshal(fields["caption"], &caption)
		return headline(caption)
	case TypeProse:
		var text string
		_ = json.Unmarshal(fields["text"], &text)
		return headline(text)
	}
	return ""
}

func (cm *composer) authoredTiles(authored []AuthoredTile) {
	var kept []candidate
	for _, a := range authored {
		if !authoredID.MatchString(a.ID) {
			cm.fix(FixAuthoredInvalid, "", "its ID is not table_, code_ or prose_ followed by a slug")
			continue
		}
		var fields map[string]json.RawMessage
		switch {
		case encodedSize(a.Data, MaxDataBytes) > MaxDataBytes:
			cm.fix(FixAuthoredInvalid, a.ID, fmt.Sprintf("its data is larger than %d KiB", MaxDataBytes>>10))
			continue
		case json.Unmarshal(a.Data, &fields) != nil || fields == nil:
			cm.fix(FixAuthoredInvalid, a.ID, "its data is not a JSON object")
			continue
		case fields["origin"] != nil:
			cm.fix(FixAuthoredInvalid, a.ID, "an authored tile has no origin")
			continue
		}
		kind, _, _ := strings.Cut(a.ID, "_")
		a.Title = cm.title(a.ID, a.Title, kind)
		a.Data = bytes.Clone(a.Data)
		if kind == TypeProse {
			var text string
			if json.Unmarshal(fields["text"], &text) == nil {
				if clean := stripControls(text, true); clean != text {
					cm.fix(FixControl, a.ID, "")
					fields["text"], _ = json.Marshal(clean)
					a.Data, _ = json.Marshal(fields)
				}
			}
		}
		kept = append(kept, candidate{authored: a, tile: Tile{
			ID: a.ID, Type: kind, Title: a.Title, Headline: composedHeadline(kind, fields), Data: a.Data, State: StateReady,
		}})
	}
	cm.authored = kept
}

// title tidies an authored tile's title.
func (cm *composer) title(id, title, kind string) string {
	tidy := strings.TrimSpace(strings.Join(strings.Fields(stripControls(title, false)), " "))
	if tidy == "" {
		builtin, _ := Builtin(kind)
		tidy = builtin.Title
	}
	tidy = truncate(tidy, MaxComposedTitle)
	if tidy != title {
		cm.fix(FixTitle, id, "")
	}
	return tidy
}

// stripControls removes C0 control characters and DEL from text; with
// lines, tabs and newlines stay. Text on one line gets a space for each.
func stripControls(text string, lines bool) string {
	if !strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return text
	}
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '\t' || r == '\n':
			if lines {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// uniqueIDs renames an authored tile whose ID a converted tile, a library
// tile or an earlier authored tile has.
func (cm *composer) uniqueIDs() {
	used := map[string]bool{}
	for _, cand := range cm.library {
		used[cand.tile.ID] = true
	}
	for i := range cm.authored {
		cand := &cm.authored[i]
		id := cand.tile.ID
		if cm.taken(id) || used[id] {
			next := id
			for n := 2; cm.taken(next) || used[next]; n++ {
				next = suffixed(id, n)
			}
			cm.fix(FixRenamed, id, "")
			cand.tile.ID = next
			cand.authored.ID = next
		}
		used[cand.tile.ID] = true
	}
}

// maxSlug bounds the slug of an authored tile's ID.
const maxSlug = 64

// suffixed returns an authored tile's ID with a numeric suffix, its slug
// shortened so the ID keeps its pattern.
func suffixed(id string, n int) string {
	suffix := "_" + strconv.Itoa(n)
	kind, _, _ := strings.Cut(id, "_")
	if limit := len(kind) + 1 + maxSlug - len(suffix); len(id) > limit {
		id = id[:limit]
	}
	return id + suffix
}

// check runs the tile checks on the extra tiles against the document they
// join. In write mode a tile that fails is dropped, and the checks run
// again, since a citation may name a dropped tile.
func (cm *composer) check() error {
	data, err := defaultValidator.data()
	if err != nil {
		return err
	}
	for {
		doc := cm.candidateDocument()
		refs := newTileRefs(doc)
		next := len(cm.doc.Tiles)
		dropped := false
		keep := func(cands []candidate, kind FixKind) []candidate {
			var kept []candidate
			for _, cand := range cands {
				tile := &doc.Tiles[next]
				next++
				var problems, covers []string
				if tile.Type == TypeCustom {
					problems, covers = defaultValidator.customProblems(refs, tile)
				} else if problems = shapeProblems(data, refs, tile); len(problems) == 0 {
					problems, covers = authoredProblems(refs, tile)
				}
				if len(problems) > 0 && cm.mode == modeWrite {
					cm.fix(kind, tile.ID, problems[0])
					dropped = true
					continue
				}
				cand.tile.Covers, cand.tile.Problems, cand.tile.State = covers, nil, StateReady
				switch {
				case len(problems) > 0:
					cand.tile.State, cand.tile.Problems = StateUnavailable, problems
				case cand.library.Pending:
					cand.tile.State = StatePending
				}
				kept = append(kept, cand)
			}
			return kept
		}
		cm.library = keep(cm.library, FixLibraryInvalid)
		cm.authored = keep(cm.authored, FixAuthoredInvalid)
		// The limits keep the first tiles that pass their checks; dropping
		// the rest may leave a citation of one unresolved, so check again.
		for _, limit := range []struct {
			cands *[]candidate
			max   int
			kind  FixKind
		}{{&cm.library, MaxLibraryTiles, FixLibraryLimit}, {&cm.authored, MaxAuthored, FixAuthoredLimit}} {
			if len(*limit.cands) > limit.max {
				for _, cand := range (*limit.cands)[limit.max:] {
					cm.fix(limit.kind, cand.tile.ID, "")
				}
				*limit.cands = (*limit.cands)[:limit.max]
				dropped = true
			}
		}
		if !dropped {
			return nil
		}
	}
}

// shapeProblems returns an authored tile's problems as a built-in tile's,
// except that data without its type's shape is reported without the schema
// library's message, which may quote the data.
func shapeProblems(data map[string]*jsonschema.Resolved, refs *tileRefs, tile *Tile) []string {
	problems := builtinProblems(data, refs, tile)
	for i, problem := range problems {
		if strings.HasPrefix(problem, "data does not have the ") {
			problems[i] = fmt.Sprintf("its data does not have the %s tile's shape", tile.Type)
		}
	}
	return problems
}

// authoredProblems checks an authored tile's citations: every one resolves,
// and at least one names a hunk or a file. It returns the problems, or the
// hunks the tile covers.
func authoredProblems(refs *tileRefs, tile *Tile) ([]string, []string) {
	value, err := decodeData(tile.Data)
	if err != nil {
		return []string{"the data is not valid JSON"}, nil
	}
	citations, err := findCitations(value, &validation{})
	switch {
	case err != nil:
		return []string{tooCostly}, nil
	case len(citations) > MaxCitations:
		return []string{fmt.Sprintf("the data has more than %d citations", MaxCitations)}, nil
	}
	problems, hunks := citationProblems(citations, refs.customRefs())
	if len(problems) > 0 {
		return problems, nil
	}
	if !slices.ContainsFunc(citations, func(c citation) bool { return c.kind == "hunk" || c.kind == "file" }) {
		return []string{"an authored tile cites no hunk or file of the review; cite the code it shows"}, nil
	}
	return nil, hunks
}

// candidateDocument is the converted document with the extra tiles added,
// for checking them.
func (cm *composer) candidateDocument() *Document {
	doc := *cm.doc
	doc.Tiles = slices.Clone(cm.doc.Tiles)
	for _, cands := range [][]candidate{cm.library, cm.authored} {
		for _, cand := range cands {
			doc.Tiles = append(doc.Tiles, cand.tile)
		}
	}
	return &doc
}

// Block is a table or code block a current change's section holds, which
// an authored tile must not repeat.
type Block struct {
	Change string
	Kind   string
	Raw    json.RawMessage
}

// Blocks returns the table and code blocks of a converted document's
// current change tiles, by change in the document's tile order, then by
// position.
func Blocks(doc *Document) []Block {
	var blocks []Block
	for i := range doc.Tiles {
		tile := &doc.Tiles[i]
		if !currentChangeTile(doc, tile) {
			continue
		}
		var data ChangeData
		if json.Unmarshal(tile.Data, &data) != nil {
			continue
		}
		for _, raw := range data.Blocks {
			kind, err := blockKind(raw)
			if err != nil || kind != TypeTable && kind != TypeCode {
				continue
			}
			blocks = append(blocks, Block{Change: tile.ID, Kind: kind, Raw: raw})
		}
	}
	return blocks
}

// currentChangeTile reports whether a tile is a current change's ready
// tile.
func currentChangeTile(doc *Document, tile *Tile) bool {
	if tile.Type != TypeChange || tile.State != StateReady {
		return false
	}
	for _, change := range doc.Changes {
		if change.ID == tile.ID {
			return currentChange(change)
		}
	}
	return false
}

package tiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// NormalizeComposition repairs a composition, as a model wrote it, against
// the tile document converted from its review, and reports every fix. The
// result is a fixed point: normalising it again changes nothing. An empty
// composition stays empty. The rules, in order:
//
//  1. Library tiles: at most MaxLibraryTiles; a tile whose definition does
//     not decode to a name, or whose name another tile of the document or an
//     earlier library tile has, is dropped; so is one whose definition, data
//     or citations fail the custom tile checks. A pending tile's data is
//     empty.
//  2. Promotions: the ID is table_ or code_ and a slug, naming a table or
//     code block of a current change tile of that kind, at its position
//     among the change's blocks in the review; at most one per change and
//     per block, and at most MaxPromotions. The host writes the block's
//     digest; a promotion whose digest no longer matches is dropped, and so
//     is one whose block does not have its tile's shape.
//  3. Authored tiles: the ID is table_, code_ or prose_ and a slug, at most
//     MaxAuthored, the data a JSON object of at most MaxDataBytes without an
//     origin, with the type's shape, citing at least one hunk or file of the
//     review, every citation resolving.
//  4. IDs: a promoted or authored tile whose ID another tile has (a library
//     tile first, then promotions, then authored tiles, in order) gets the
//     first free numeric suffix (_2, _3, …).
//  5. Titles: on one line, at most MaxComposedTitle characters, the type's
//     catalogue title when blank.
//  6. Layout: an entry naming no tile, a second entry for a tile and an entry
//     for an obsolete change (the host places those last) are dropped; a
//     size the tile's type does not allow (AllowedSizes) becomes its default
//     size; only a current change tile that is not pinned may be in the
//     changes group, the only group a composition sets; tiles the layout
//     omits are appended in the converter's order with its sizes (a change
//     tile at the end of the changes group, composition tiles last); the
//     changes group is made contiguous at its first member; changes_shown is
//     at least 1.
//  7. Notes: a reason naming no tile is dropped, reasons are cut to MaxWhy
//     characters, moves to MaxMoves.
//
// Fixes name a tile only by an ID that is a plain identifier and never quote
// what the composition holds.
func NormalizeComposition(doc *Document, c *Composition) (Composition, Report) {
	if c.Empty() {
		if c == nil {
			return Composition{Schema: CompositionName}, Report{}
		}
		out := *c
		out.Schema = CompositionName
		return out, Report{}
	}
	cm := newComposer(doc, modeWrite)
	n, err := cm.normalize(c)
	if err != nil {
		// The document cannot take a composition (it is not a converted
		// document); there is nothing to normalise against.
		return Composition{Schema: CompositionName}, Report{Fixes: []Fix{{Kind: FixUnusable, Detail: err.Error()}}}
	}
	return n, cm.report
}

// Compose applies a composition to the tile document FromReview converted
// from a review, and returns the generated tile document: origin
// "generated", the composition's tiles added, promoted blocks moved out of
// their changes, the layout applied and arranged (Arrange: pinned tiles
// first, and the risks tile onto the landing page when it holds an active
// critical or high risk). The composition is normalised first, so any
// composition gives a valid document; a library or authored tile that fails
// its checks is shown unavailable with its problems, as a stored custom tile
// is, rather than dropped. A nil or empty composition gives the converted
// document unchanged, encoding to the same bytes. doc is not modified.
//
// Whatever the composition, every tile of the converted document is placed,
// with its content (less its promoted blocks), so the tiles cover exactly
// the current hunks; the substrate, recommendation and risk score are the
// converted document's; every risk, evidence item, decision and block is
// presented once; and ToReview of the result is the review.
func Compose(doc *Document, c *Composition) (*Document, error) {
	if doc == nil {
		return nil, errors.New("tiles: no document to compose")
	}
	if c.Empty() {
		return cloneDocument(doc), nil
	}
	cm := newComposer(doc, modeRead)
	n, err := cm.normalize(c)
	if err != nil {
		return nil, err
	}
	return cm.build(&n)
}

// cloneDocument copies a document deeply enough that changing its tiles or
// layout does not change the original.
func cloneDocument(doc *Document) *Document {
	out := *doc
	out.Tiles = slices.Clone(doc.Tiles)
	out.Layout.Entries = slices.Clone(doc.Layout.Entries)
	out.Layout.Groups = slices.Clone(doc.Layout.Groups)
	return &out
}

// FixKind names a repair NormalizeComposition made.
type FixKind string

// The fixes.
const (
	FixUnknownTile       FixKind = "unknown_tile"
	FixDuplicateEntry    FixKind = "duplicate_entry"
	FixHostPlaced        FixKind = "host_placed"
	FixAppended          FixKind = "appended"
	FixSize              FixKind = "size"
	FixGroup             FixKind = "group"
	FixGroupContiguous   FixKind = "group_contiguous"
	FixChangesShown      FixKind = "changes_shown"
	FixLibraryInvalid    FixKind = "library_invalid"
	FixLibraryDuplicate  FixKind = "library_duplicate"
	FixLibraryLimit      FixKind = "library_limit"
	FixPromotionInvalid  FixKind = "promotion_invalid"
	FixPromotionMissing  FixKind = "promotion_missing"
	FixPromotionChanged  FixKind = "promotion_changed"
	FixPromotionRepeated FixKind = "promotion_repeated"
	FixPromotionLimit    FixKind = "promotion_limit"
	FixAuthoredInvalid   FixKind = "authored_invalid"
	FixAuthoredLimit     FixKind = "authored_limit"
	FixRenamed           FixKind = "renamed"
	FixTitle             FixKind = "title"
	FixNote              FixKind = "note"
	// FixUnusable is reported when the document cannot take a composition.
	FixUnusable FixKind = "unusable"
)

// Fix is one repair. Tile is the tile's ID when it is a plain identifier;
// Detail is the host's own text, which never quotes the composition.
type Fix struct {
	Kind   FixKind `json:"kind"`
	Tile   string  `json:"tile,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

// Report lists the fixes of a normalisation, in the order they were made.
type Report struct {
	Fixes []Fix `json:"fixes,omitempty"`
}

// Counts returns how many fixes of each kind the report holds, or nil.
func (r Report) Counts() map[FixKind]int {
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
	case FixUnknownTile:
		text = "the layout names a tile the review does not have; the entry is dropped"
	case FixDuplicateEntry:
		text = fmt.Sprintf("the layout places %s more than once; its first place is kept", tile)
	case FixHostPlaced:
		text = fmt.Sprintf("the layout places %s, an obsolete change, which always goes last", tile)
	case FixAppended:
		text = fmt.Sprintf("the layout does not place %s; it is added in the default order", tile)
	case FixSize:
		text = fmt.Sprintf("%s cannot have that size; it gets the default size of its type", tile)
	case FixGroup:
		text = fmt.Sprintf("%s cannot be in that group (only a current change that is not pinned can be in the changes group); it is placed outside it", tile)
	case FixGroupContiguous:
		text = fmt.Sprintf("%s is moved next to the other members of the changes group", tile)
	case FixChangesShown:
		text = "the changes group must show at least one change"
	case FixLibraryInvalid:
		text = name("library tile", "a") + " fails its checks and is left out"
	case FixLibraryDuplicate:
		text = name("library tile", "a") + " has the ID of another tile and is left out"
	case FixLibraryLimit:
		text = fmt.Sprintf("the composition has more than %d library tiles; the rest are left out", MaxLibraryTiles)
	case FixPromotionInvalid:
		text = "a promotion's ID is not table_ or code_ followed by a slug; it is left out"
	case FixPromotionMissing:
		text = name("promotion", "a") + " names no table or code block of its kind in a current change; it is left out"
	case FixPromotionChanged:
		text = "the block " + name("promotion", "a") + " names has changed since it was promoted; it is left out"
	case FixPromotionRepeated:
		text = name("promotion", "a") + " names a block already promoted or a change that already has a promotion; it is left out"
	case FixPromotionLimit:
		text = fmt.Sprintf("the composition has more than %d promotions; the rest are left out", MaxPromotions)
	case FixAuthoredInvalid:
		text = name("authored tile", "an") + " fails its checks and is left out"
	case FixAuthoredLimit:
		text = fmt.Sprintf("the composition has more than %d authored tiles; the rest are left out", MaxAuthored)
	case FixRenamed:
		text = fmt.Sprintf("%s has the ID of another tile; it is renamed", tile)
	case FixTitle:
		text = fmt.Sprintf("the title of %s is blank, too long or on several lines; it is tidied", tile)
	case FixNote:
		text = "a note names a tile the review does not have or is too long; it is dropped or shortened"
	case FixUnusable:
		text = "the composition cannot be applied to this document"
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

// AllowedSizes returns the sizes a tile may have in a layout: half or full
// for the overview and risks tiles and for table, code, prose and diagram
// tiles; the sizes its definition declares for a custom tile (every size
// when the definition is invalid); every size for the others.
func AllowedSizes(tile Tile) []Size {
	all := []Size{SizeFull, SizeHalf, SizeThird}
	switch tile.Type {
	case TypeOverview, TypeRisks, TypeTable, TypeCode, TypeProse, TypeDiagram:
		return []Size{SizeFull, SizeHalf}
	case TypeCustom:
		if sizes := definitionSizes(tile.Definition); len(sizes) > 0 {
			return sizes
		}
	}
	return all
}

// BlockDigest returns a block's digest: "sha256:" and the lowercase hex
// SHA-256 of its RFC 8785 canonical JSON, as DefinitionDigest computes it
// for a definition (without removing any member).
func BlockDigest(block json.RawMessage) (string, error) {
	value, err := decodeCanonical(block)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := canonical(&b, value); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// PromotableBlock is a table or code block of a current change tile that a
// composition may promote. Block is its position among the change's blocks
// in the review; Index is its position among the blocks its tile holds.
type PromotableBlock struct {
	Change string
	Block  int
	Index  int
	Kind   string
	Digest string
	Raw    json.RawMessage
}

// Promotable returns the blocks of a converted document that a composition
// may promote, by change in the document's tile order, then by position.
func Promotable(doc *Document) []PromotableBlock {
	moved := movedPositions(doc)
	var blocks []PromotableBlock
	for i := range doc.Tiles {
		tile := &doc.Tiles[i]
		if !promotableChange(doc, tile) {
			continue
		}
		var data ChangeData
		if json.Unmarshal(tile.Data, &data) != nil {
			continue
		}
		positions := keptPositions(len(data.Blocks), moved[tile.ID])
		for k, raw := range data.Blocks {
			kind, err := blockKind(raw)
			if err != nil || !slices.Contains(promotableTypes, kind) {
				continue
			}
			digest, err := BlockDigest(raw)
			if err != nil {
				continue
			}
			blocks = append(blocks, PromotableBlock{Change: tile.ID, Block: positions[k], Index: k, Kind: kind, Digest: digest, Raw: raw})
		}
	}
	return blocks
}

// promotableChange reports whether a tile is a current change's ready tile.
func promotableChange(doc *Document, tile *Tile) bool {
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

// movedPositions returns, by change, the positions of the change's blocks
// the intent, media and decisions tiles hold.
func movedPositions(doc *Document) map[string]map[int]bool {
	moved := map[string]map[int]bool{}
	add := func(origin *BlockOrigin) {
		if origin == nil {
			return
		}
		if moved[origin.Change] == nil {
			moved[origin.Change] = map[int]bool{}
		}
		moved[origin.Change][origin.Block] = true
	}
	for _, tile := range doc.Tiles {
		var items struct {
			Lines []itemPlace `json:"lines"`
			Items []itemPlace `json:"items"`
		}
		switch tile.Type {
		case TypeIntent, TypeMedia, TypeDecisions:
			if json.Unmarshal(tile.Data, &items) != nil {
				continue
			}
		default:
			continue
		}
		for _, item := range append(items.Lines, items.Items...) {
			add(item.Origin)
		}
	}
	return moved
}

// keptPositions returns the review positions of a change tile's n kept
// blocks: the positions the aggregate tiles do not hold, in order.
func keptPositions(n int, moved map[int]bool) []int {
	positions := make([]int, 0, n)
	for p := 0; len(positions) < n; p++ {
		if !moved[p] {
			positions = append(positions, p)
		}
	}
	return positions
}

type composeMode int

const (
	// modeWrite drops a composition tile that fails its checks.
	modeWrite composeMode = iota
	// modeRead keeps a library or authored tile that fails its checks,
	// unavailable with its problems, as a stored custom tile is.
	modeRead
)

var (
	promotionID = regexp.MustCompile(`^(table|code)_[a-z0-9_]{1,64}$`)
	authoredID  = regexp.MustCompile(`^(table|code|prose)_[a-z0-9_]{1,64}$`)
	tileID      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
)

type composer struct {
	doc    *Document
	mode   composeMode
	report Report
	byID   map[string]*Tile
	// obsolete holds the obsolete changes' tiles, which the host places.
	obsolete map[string]bool
	// tiles are the composition's tiles once normalised, by kind.
	library  []candidate
	promoted []candidate
	authored []candidate
}

// candidate is a composition tile being normalised.
type candidate struct {
	tile      Tile
	library   LibraryTile
	promotion Promotion
	authored  AuthoredTile
	// index is a promoted block's position among its tile's blocks.
	index int
}

func (c *candidate) isPromotion() bool { return c.promotion.Change != "" }

func newComposer(doc *Document, mode composeMode) *composer {
	cm := &composer{doc: doc, mode: mode, byID: map[string]*Tile{}, obsolete: map[string]bool{}}
	for i := range doc.Tiles {
		cm.byID[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	for _, entry := range doc.Layout.Entries {
		if entry.Group == GroupObsolete {
			cm.obsolete[entry.Tile] = true
		}
	}
	return cm
}

func (cm *composer) fix(kind FixKind, id, detail string) {
	cm.report.Fixes = append(cm.report.Fixes, Fix{Kind: kind, Tile: plainID(id), Detail: detail})
}

func (cm *composer) normalize(c *Composition) (Composition, error) {
	if err := cm.prepare(c); err != nil {
		return Composition{}, err
	}
	return cm.finish(c, c.Layout), nil
}

// prepare normalises the composition's tiles.
func (cm *composer) prepare(c *Composition) error {
	if cm.doc.Origin.Kind != OriginConverted {
		return errors.New("tiles: a composition applies only to a converted document")
	}
	cm.libraryTiles(c.Library)
	cm.promotions(c.Promotions)
	cm.authoredTiles(c.Authored)
	cm.uniqueIDs()
	return cm.check()
}

// finish returns the normalised composition with the prepared tiles and the
// layout normalised.
func (cm *composer) finish(c *Composition, layout CompositionLayout) Composition {
	out := Composition{Schema: CompositionName}
	if c.Generator != nil {
		generator := *c.Generator
		out.Generator = &generator
	}
	for _, cand := range cm.library {
		out.Library = append(out.Library, cand.library)
	}
	for _, cand := range cm.promoted {
		out.Promotions = append(out.Promotions, cand.promotion)
	}
	for _, cand := range cm.authored {
		out.Authored = append(out.Authored, cand.authored)
	}
	out.Layout = cm.layout(layout)
	out.Notes = cm.notes(c.Notes)
	return out
}

// taken reports whether a converted tile or an obsolete change has the ID.
func (cm *composer) taken(id string) bool { return cm.byID[id] != nil }

func (cm *composer) libraryTiles(library []LibraryTile) {
	names := map[string]bool{}
	for i, lib := range library {
		if i == MaxLibraryTiles {
			for range library[i:] {
				cm.fix(FixLibraryLimit, "", "")
			}
			return
		}
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
		if cm.taken(name) || names[name] {
			cm.fix(FixLibraryDuplicate, name, "")
			continue
		}
		names[name] = true
		lib.Definition = bytes.Clone(lib.Definition)
		if lib.Pending || len(bytes.TrimSpace(lib.Data)) == 0 {
			lib.Data = json.RawMessage(`{}`)
		}
		title := strings.TrimSpace(strings.Join(strings.Fields(def.Title), " "))
		if title == "" {
			title = name
		}
		state := StateReady
		if lib.Pending {
			state = StatePending
		}
		cm.library = append(cm.library, candidate{library: lib, tile: Tile{
			ID: name, Type: TypeCustom, Title: truncate(title, maxTitle), Data: lib.Data, State: state, Definition: lib.Definition,
		}})
	}
}

func (cm *composer) promotions(promotions []Promotion) {
	moved := movedPositions(cm.doc)
	blocks := map[[2]string]bool{}
	changes := map[string]bool{}
	var kept []candidate
	for _, p := range promotions {
		if !promotionID.MatchString(p.ID) {
			cm.fix(FixPromotionInvalid, "", "")
			continue
		}
		kind, _, _ := strings.Cut(p.ID, "_")
		tile := cm.byID[p.Change]
		if tile == nil || !promotableChange(cm.doc, tile) {
			cm.fix(FixPromotionMissing, p.ID, "")
			continue
		}
		var data ChangeData
		if json.Unmarshal(tile.Data, &data) != nil {
			cm.fix(FixPromotionMissing, p.ID, "")
			continue
		}
		index := slices.Index(keptPositions(len(data.Blocks), moved[tile.ID]), p.Block)
		if index < 0 {
			cm.fix(FixPromotionMissing, p.ID, "")
			continue
		}
		raw := data.Blocks[index]
		if blockKindOf(raw) != kind {
			cm.fix(FixPromotionMissing, p.ID, "")
			continue
		}
		digest, err := BlockDigest(raw)
		if err != nil {
			cm.fix(FixPromotionMissing, p.ID, "")
			continue
		}
		if p.Digest != "" && p.Digest != digest {
			cm.fix(FixPromotionChanged, p.ID, "")
			continue
		}
		key := [2]string{p.Change, strconv.Itoa(p.Block)}
		if blocks[key] || changes[p.Change] {
			cm.fix(FixPromotionRepeated, p.ID, "")
			continue
		}
		blocks[key], changes[p.Change] = true, true
		p.Digest = digest
		p.Title = cm.title(p.ID, p.Title, kind)
		kept = append(kept, candidate{promotion: p, index: index, tile: Tile{ID: p.ID, Type: kind, Title: p.Title, State: StateReady}})
	}
	if len(kept) > MaxPromotions {
		for range kept[MaxPromotions:] {
			cm.fix(FixPromotionLimit, "", "")
		}
		kept = kept[:MaxPromotions]
	}
	for i := range kept {
		cm.promotedTile(&kept[i])
	}
	cm.promoted = kept
}

func blockKindOf(raw json.RawMessage) string {
	kind, _ := blockKind(raw)
	return kind
}

// promotedTile writes a promoted tile's data: its block without the kind,
// with its origin.
func (cm *composer) promotedTile(cand *candidate) {
	p := cand.promotion
	var data ChangeData
	_ = json.Unmarshal(cm.byID[p.Change].Data, &data)
	var fields map[string]json.RawMessage
	if json.Unmarshal(data.Blocks[cand.index], &fields) != nil {
		fields = map[string]json.RawMessage{}
	}
	delete(fields, "kind")
	fields["origin"], _ = json.Marshal(BlockOrigin{Change: p.Change, Block: p.Block})
	cand.tile.Data, _ = json.Marshal(fields)
	cand.tile.Headline = composedHeadline(cand.tile.Type, fields)
	cand.tile.Covers = sourceHunks(cm.doc, fields)
}

// composedHeadline is a promoted or authored tile's headline: a table's row
// count, a code excerpt's caption or a prose tile's first sentence.
func composedHeadline(kind string, fields map[string]json.RawMessage) string {
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

// sourceHunks returns the index's hunks a block's source cites, sorted and
// without repeats.
func sourceHunks(doc *Document, fields map[string]json.RawMessage) []string {
	var source []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	_ = json.Unmarshal(fields["source"], &source)
	indexed, _ := indexHunks(doc.Index)
	var hunks []string
	for _, anchor := range source {
		if anchor.Type == "hunk" && indexed[anchor.ID] {
			hunks = append(hunks, anchor.ID)
		}
	}
	slices.Sort(hunks)
	return slices.Compact(hunks)
}

func (cm *composer) authoredTiles(authored []AuthoredTile) {
	var kept []candidate
	for _, a := range authored {
		if !authoredID.MatchString(a.ID) {
			cm.fix(FixAuthoredInvalid, "", "its ID is not table_, code_ or prose_ followed by a slug")
			continue
		}
		if len(kept) == MaxAuthored {
			cm.fix(FixAuthoredLimit, a.ID, "")
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
			cm.fix(FixAuthoredInvalid, a.ID, "an authored tile has no origin; only a promoted block has one")
			continue
		}
		kind, _, _ := strings.Cut(a.ID, "_")
		a.Title = cm.title(a.ID, a.Title, kind)
		a.Data = bytes.Clone(a.Data)
		kept = append(kept, candidate{authored: a, tile: Tile{
			ID: a.ID, Type: kind, Title: a.Title, Headline: composedHeadline(kind, fields), Data: a.Data, State: StateReady,
		}})
	}
	cm.authored = kept
}

// title tidies a promoted or authored tile's title.
func (cm *composer) title(id, title, kind string) string {
	tidy := strings.TrimSpace(strings.Join(strings.Fields(title), " "))
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

// uniqueIDs renames a promoted or authored tile whose ID a converted tile, a
// library tile or an earlier composition tile has.
func (cm *composer) uniqueIDs() {
	used := map[string]bool{}
	for _, cand := range cm.library {
		used[cand.tile.ID] = true
	}
	rename := func(cand *candidate) {
		id := cand.tile.ID
		if cm.taken(id) || used[id] {
			next := id
			for n := 2; cm.taken(next) || used[next]; n++ {
				next = suffixed(id, n)
			}
			cm.fix(FixRenamed, id, "")
			cand.tile.ID = next
			if cand.isPromotion() {
				cand.promotion.ID = next
			} else {
				cand.authored.ID = next
			}
		}
		used[cand.tile.ID] = true
	}
	for i := range cm.promoted {
		rename(&cm.promoted[i])
	}
	for i := range cm.authored {
		rename(&cm.authored[i])
	}
}

// maxSlug bounds the slug of a promoted or authored tile's ID.
const maxSlug = 64

// suffixed returns a promoted or authored tile's ID with a numeric suffix,
// its slug shortened so the ID keeps its pattern.
func suffixed(id string, n int) string {
	suffix := "_" + strconv.Itoa(n)
	if limit := len(prefix(id)) + 1 + maxSlug - len(suffix); len(id) > limit {
		id = id[:limit]
	}
	return id + suffix
}

// check runs the tile checks on the composition's tiles against the
// document they join. In write mode a tile that fails is dropped, and the
// checks run again, since a citation may name a dropped tile; a promoted
// tile that fails is dropped in either mode, which leaves its block in its
// change.
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
				switch {
				case tile.Type == TypeCustom:
					problems, covers = defaultValidator.customProblems(refs, tile)
				case cand.isPromotion():
					problems, covers = shapeProblems(data, refs, tile), tile.Covers
				default:
					if problems = shapeProblems(data, refs, tile); len(problems) == 0 {
						problems, covers = authoredProblems(refs, tile)
					}
				}
				if len(problems) > 0 && (cm.mode == modeWrite || cand.isPromotion()) {
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
		cm.promoted = keep(cm.promoted, FixPromotionMissing)
		cm.authored = keep(cm.authored, FixAuthoredInvalid)
		if !dropped {
			return nil
		}
	}
}

// shapeProblems returns a promoted or authored tile's problems as a
// built-in tile's, except that data without its type's shape is reported
// without the schema library's message, which may quote the data.
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

// candidateDocument is the converted document with the composition's tiles
// added, for checking them.
func (cm *composer) candidateDocument() *Document {
	doc := cloneDocument(cm.doc)
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for _, cand := range cands {
			doc.Tiles = append(doc.Tiles, cand.tile)
		}
	}
	return doc
}

// layout normalises the layout's entries against the document's tiles and
// the composition's.
func (cm *composer) layout(in CompositionLayout) CompositionLayout {
	tiles := map[string]*Tile{}
	for id, tile := range cm.byID {
		if !cm.obsolete[id] {
			tiles[id] = tile
		}
	}
	var added []*Tile
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for i := range cands {
			tiles[cands[i].tile.ID] = &cands[i].tile
			added = append(added, &cands[i].tile)
		}
	}
	defaults := map[string]Entry{}
	for _, entry := range cm.doc.Layout.Entries {
		defaults[entry.Tile] = entry
	}
	defaultSize := func(tile *Tile) Size {
		if entry, ok := defaults[tile.ID]; ok {
			return entry.Size
		}
		if builtin, ok := Builtin(tile.Type); ok {
			return builtin.Size
		}
		return AllowedSizes(*tile)[0]
	}
	groupable := func(tile *Tile) bool {
		return tile.Type == TypeChange && defaults[tile.ID].Group == GroupChanges
	}

	var entries []Entry
	placed := map[string]bool{}
	for _, entry := range in.Entries {
		tile := tiles[entry.Tile]
		switch {
		case cm.obsolete[entry.Tile]:
			cm.fix(FixHostPlaced, entry.Tile, "")
			continue
		case tile == nil:
			cm.fix(FixUnknownTile, "", "")
			continue
		case placed[entry.Tile]:
			cm.fix(FixDuplicateEntry, entry.Tile, "")
			continue
		}
		placed[entry.Tile] = true
		if !slices.Contains(AllowedSizes(*tile), entry.Size) {
			cm.fix(FixSize, entry.Tile, "")
			entry.Size = defaultSize(tile)
		}
		if entry.Group != "" && (entry.Group != GroupChanges || entry.Pin || !groupable(tile)) {
			cm.fix(FixGroup, entry.Tile, "")
			entry.Group = ""
		}
		entries = append(entries, Entry{Tile: entry.Tile, Size: entry.Size, Group: entry.Group, Pin: entry.Pin})
	}

	entries = contiguous(entries, func(id string) { cm.fix(FixGroupContiguous, id, "") })

	// Append what the layout omits: a change at the end of the changes
	// group, the others last.
	for _, entry := range cm.doc.Layout.Entries {
		if placed[entry.Tile] || cm.obsolete[entry.Tile] {
			continue
		}
		placed[entry.Tile] = true
		cm.fix(FixAppended, entry.Tile, "")
		entry.Pin = false
		at := len(entries)
		if entry.Group == GroupChanges {
			if last := lastIndex(entries, GroupChanges); last >= 0 {
				at = last + 1
			}
		}
		entries = slices.Insert(entries, at, entry)
	}
	for _, tile := range added {
		if !placed[tile.ID] {
			placed[tile.ID] = true
			cm.fix(FixAppended, tile.ID, "")
			entries = append(entries, Entry{Tile: tile.ID, Size: defaultSize(tile)})
		}
	}

	out := CompositionLayout{Entries: nonNil(entries)}
	if in.ChangesShown != nil {
		shown := *in.ChangesShown
		if shown < 1 {
			cm.fix(FixChangesShown, "", "")
			shown = 1
		}
		out.ChangesShown = &shown
	}
	return out
}

func lastIndex(entries []Entry, group string) int {
	last := -1
	for i, entry := range entries {
		if entry.Group == group {
			last = i
		}
	}
	return last
}

// contiguous gathers the members of each group at its first member's place,
// in their order, calling moved for each member it moves.
func contiguous(entries []Entry, moved func(id string)) []Entry {
	out := make([]Entry, 0, len(entries))
	gathered := map[string]bool{}
	for i, entry := range entries {
		if entry.Group == "" {
			out = append(out, entry)
			continue
		}
		if gathered[entry.Group] {
			continue
		}
		gathered[entry.Group] = true
		run := true
		for _, member := range entries[i:] {
			if member.Group != entry.Group {
				run = false
				continue
			}
			if !run {
				moved(member.Tile)
			}
			out = append(out, member)
		}
	}
	return out
}

func (cm *composer) notes(in *Notes) *Notes {
	if in == nil {
		return nil
	}
	known := map[string]bool{}
	for id := range cm.byID {
		known[id] = true
	}
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for _, cand := range cands {
			known[cand.tile.ID] = true
		}
	}
	bound := func(text string, limit int) string {
		cut := truncate(text, limit)
		if cut != text {
			cm.fix(FixNote, "", "")
		}
		return cut
	}
	out := &Notes{}
	ids := slices.Sorted(maps.Keys(in.Why))
	for _, id := range ids {
		why := in.Why[id]
		if !known[id] {
			cm.fix(FixNote, "", "")
			continue
		}
		if out.Why == nil {
			out.Why = map[string]string{}
		}
		out.Why[id] = bound(why, MaxWhy)
	}
	for _, move := range in.Moves {
		if !known[move.Tile] || len(out.Moves) == MaxMoves {
			cm.fix(FixNote, "", "")
			continue
		}
		out.Moves = append(out.Moves, Move{Tile: move.Tile, Why: bound(move.Why, MaxWhy)})
	}
	if s := in.CustomSuggestion; s != nil {
		out.CustomSuggestion = &CustomSuggestion{Title: bound(s.Title, MaxComposedTitle), Shows: bound(s.Shows, 300), Why: bound(s.Why, 300)}
	}
	if out.Why == nil && out.Moves == nil && out.CustomSuggestion == nil {
		return nil
	}
	return out
}

// build applies a normalised composition to the converted document.
func (cm *composer) build(n *Composition) (*Document, error) {
	doc := cloneDocument(cm.doc)
	doc.Origin = Origin{Kind: OriginGenerated}
	for _, cand := range cm.promoted {
		at := slices.IndexFunc(doc.Tiles, func(t Tile) bool { return t.ID == cand.promotion.Change })
		var data ChangeData
		if err := json.Unmarshal(doc.Tiles[at].Data, &data); err != nil {
			return nil, fmt.Errorf("tiles: change tile data: %w", err)
		}
		data.Blocks = slices.Delete(slices.Clone(data.Blocks), cand.index, cand.index+1)
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		doc.Tiles[at].Data = encoded
	}
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for _, cand := range cands {
			doc.Tiles = append(doc.Tiles, cand.tile)
		}
	}
	entries := slices.Clone(n.Layout.Entries)
	for _, entry := range cm.doc.Layout.Entries {
		if cm.obsolete[entry.Tile] {
			entries = append(entries, entry)
		}
	}
	groups := groupsOf(entries)
	if n.Layout.ChangesShown != nil {
		for i := range groups {
			if groups[i].ID == GroupChanges {
				groups[i].CollapseAfter = new(*n.Layout.ChangesShown)
			}
		}
	}
	doc.Layout = Layout{Entries: entries, Groups: groups}
	Arrange(doc)
	return doc, nil
}

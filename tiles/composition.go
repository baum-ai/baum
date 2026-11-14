package tiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/baum-ai/baum/schema"
)

// A composition (baum.composition/1, schema/composition-1.json) says how a
// generated review is presented as tiles. It is stored beside the review's
// baum.review/1 document, and the served tile document is
// Compose(FromReview(document), composition). It holds only choices and the
// content a review cannot hold, never a copy of the review's:
//
//   - the layout: the tiles in reading order with their sizes, which change
//     tiles are in the changes group, pins, and how many members the group
//     shows;
//   - promotions: table and code blocks of a change moved to tiles of their
//     own, which go back to their change in the review view;
//   - library tiles: a custom tile type's definition with the data filled
//     for this review;
//   - authored tiles: table, code and prose tiles the composer wrote, each
//     citing the code it shows;
//   - notes (a reason per tile, moves against the previous version, a
//     suggested custom tile), kept for evaluation and not shown, and the
//     generator's provenance.
//
// Tile IDs follow one rule: a single tile's ID is its type, a change tile's
// is its section's, a library tile's is its type's name, and a promoted or
// authored tile's is its type, an underscore and a slug ("table_routes").
type Composition struct {
	Schema    string            `json:"schema"`
	Generator *Generator        `json:"generator,omitempty"`
	Layout    CompositionLayout `json:"layout"`
	Library   []LibraryTile     `json:"library,omitempty"`
	// Promotions and Authored come after Library: an ID two tiles claim is
	// kept by the library tile, then the promotion, then the authored tile.
	Promotions []Promotion    `json:"promotions,omitempty"`
	Authored   []AuthoredTile `json:"authored,omitempty"`
	Notes      *Notes         `json:"notes,omitempty"`
}

// CompositionName is the value of a composition's "schema" field.
const CompositionName = schema.CompositionName

// Generator records where a composition came from.
type Generator struct {
	PromptVersion string `json:"prompt_version,omitempty"`
	PromptDigest  string `json:"prompt_digest,omitempty"`
	Model         string `json:"model,omitempty"`
	// ComposedAt is when the composition was made, in RFC 3339.
	ComposedAt string `json:"composed_at,omitempty"`
	// Template is the change-kind template the composition started from or,
	// with Fallback, is. Only the host writes a generator (SetGenerator).
	Template string `json:"template,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	// Fixes counts what normalisation repaired, by kind (Report.Counts).
	Fixes    map[FixKind]int `json:"fixes,omitempty"`
	Problems []string        `json:"problems,omitempty"`
}

// CompositionLayout is the composer's layout: every tile but the obsolete
// changes, which the host places last, in reading order. Only current change
// tiles may have the changes group. ChangesShown, when set, is how many
// members the changes group shows before folding; without it the converter's
// default applies.
//
// Hidden lists the tiles a reader removed. Only the host writes it, after
// normalising: NormalizeComposition drops a model's, and Compose honours a
// stored one by moving those tiles, out of their groups, to a last collapsed
// "Hidden" group, so they stay reachable and the document still presents
// everything. The overview, and the risks tile while it ranks an active
// critical or high risk, are never hidden.
type CompositionLayout struct {
	Entries      []Entry  `json:"entries"`
	ChangesShown *int     `json:"changes_shown,omitempty"`
	Hidden       []string `json:"hidden,omitempty"`
}

// LibraryTile is a custom tile of a library type: its definition, with its
// digest, and its data. Its ID and title are its definition's name and
// title. A pending tile is still being filled and has empty data.
type LibraryTile struct {
	Definition json.RawMessage `json:"definition"`
	Data       json.RawMessage `json:"data"`
	Pending    bool            `json:"pending,omitempty"`
}

// Promotion moves a table or code block of a current change to a tile of
// its own. The ID's prefix is the block's kind. Block is the block's
// position among the change's blocks in baum.review/1, as a BlockOrigin
// gives it. Digest is host-written (BlockDigest): a block that no longer
// matches it is not promoted.
type Promotion struct {
	ID     string `json:"id"`
	Change string `json:"change"`
	Block  int    `json:"block"`
	Digest string `json:"digest,omitempty"`
	Title  string `json:"title"`
}

// AuthoredTile is a table, code or prose tile the composer wrote. The ID's
// prefix is its type, and Data has that type's shape without an origin.
type AuthoredTile struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

// Notes are what the composer said about its choices.
type Notes struct {
	// Why holds a reason per tile, by tile ID.
	Why              map[string]string `json:"why,omitempty"`
	Moves            []Move            `json:"moves,omitempty"`
	CustomSuggestion *CustomSuggestion `json:"custom_suggestion,omitempty"`
}

// Move explains a tile's move against the previous version.
type Move struct {
	Tile string `json:"tile"`
	Why  string `json:"why"`
}

// CustomSuggestion is a custom tile the composer would have made. It is
// logged, never shown or built.
type CustomSuggestion struct {
	Title string `json:"title"`
	Shows string `json:"shows"`
	Why   string `json:"why"`
}

// The limits of a composition.
const (
	MaxPromotions   = 3
	MaxAuthored     = 3
	MaxLibraryTiles = 8
	// MaxComposedTitle bounds a promoted or authored tile's title.
	MaxComposedTitle = 80
	// MaxWhy bounds a reason in the notes.
	MaxWhy = 160
	// MaxMoves bounds the moves in the notes.
	MaxMoves = 20
)

// The types a promotion or an authored tile may have.
var (
	promotableTypes = []string{TypeTable, TypeCode}
	authoredTypes   = []string{TypeTable, TypeCode, TypeProse}
)

// Empty reports whether a composition chooses nothing: no layout entries,
// no changes_shown or hidden tiles, no promotions, library or authored tiles. Applying an
// empty composition, like none, gives the converted document unchanged.
func (c *Composition) Empty() bool {
	return c == nil || len(c.Layout.Entries) == 0 && c.Layout.ChangesShown == nil && len(c.Layout.Hidden) == 0 &&
		len(c.Promotions) == 0 && len(c.Library) == 0 && len(c.Authored) == 0
}

// SetGenerator records where a normalised composition came from. The
// generator is the host's: NormalizeComposition drops one a model wrote, so
// the host sets it afterwards, and SetGenerator refuses one the schema would
// not accept.
func (c *Composition) SetGenerator(g Generator) error {
	for _, field := range []struct {
		value string
		limit int
	}{{g.PromptVersion, 200}, {g.PromptDigest, 200}, {g.Model, 200}, {g.ComposedAt, 64}} {
		if utf8.RuneCountInString(field.value) > field.limit {
			return fmt.Errorf("tiles: a generator field is longer than %d characters", field.limit)
		}
	}
	if g.Template != "" && !templateName.MatchString(g.Template) {
		return errors.New("tiles: the generator's template is not a template name")
	}
	for _, n := range g.Fixes {
		if n < 1 {
			return errors.New("tiles: the generator counts a fix kind less than once")
		}
	}
	if len(g.Problems) > 20 {
		return errors.New("tiles: the generator has more than 20 problems")
	}
	for _, problem := range g.Problems {
		if utf8.RuneCountInString(problem) > 400 {
			return errors.New("tiles: a generator problem is longer than 400 characters")
		}
	}
	g.Fixes = maps.Clone(g.Fixes)
	g.Problems = slices.Clone(g.Problems)
	c.Generator = &g
	return nil
}

var templateName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ParseComposition decodes a stored composition. It fails on invalid JSON
// or another schema; members it does not know are ignored, so a composition
// written by a newer version still applies. It does not normalise: Compose
// does.
func ParseComposition(raw []byte) (*Composition, error) {
	var c Composition
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("tiles: decoding the composition: %w", err)
	}
	if c.Schema != CompositionName {
		return nil, fmt.Errorf("tiles: the composition's schema is not %q", CompositionName)
	}
	return &c, nil
}

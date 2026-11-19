package tiles

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

// The parts of a review view composition (baum.composition/2, package
// tiles/view) that are tiles of this package: the library and authored
// tiles the tile analysis wrote, and the generator's provenance.

// Generator records where a composition came from. Only the host writes it
// (SetGenerator).
type Generator struct {
	PromptVersion string `json:"prompt_version,omitempty"`
	PromptDigest  string `json:"prompt_digest,omitempty"`
	Model         string `json:"model,omitempty"`
	// ComposedAt is when the composition was made, in RFC 3339.
	ComposedAt string `json:"composed_at,omitempty"`
	Fallback   bool   `json:"fallback,omitempty"`
	// Fixes counts what normalisation repaired, by kind (Report.Counts).
	Fixes    map[FixKind]int `json:"fixes,omitempty"`
	Problems []string        `json:"problems,omitempty"`
}

// Checked returns the generator with its fields checked and its maps and
// lists copied, or an error naming the field that is too long.
func (g Generator) Checked() (Generator, error) {
	for _, field := range []struct {
		value string
		limit int
	}{{g.PromptVersion, 200}, {g.PromptDigest, 200}, {g.Model, 200}, {g.ComposedAt, 64}} {
		if utf8.RuneCountInString(field.value) > field.limit {
			return Generator{}, fmt.Errorf("tiles: a generator field is longer than %d characters", field.limit)
		}
	}
	for _, n := range g.Fixes {
		if n < 1 {
			return Generator{}, fmt.Errorf("tiles: the generator counts a fix kind less than once")
		}
	}
	if len(g.Problems) > 20 {
		return Generator{}, fmt.Errorf("tiles: the generator has more than 20 problems")
	}
	for _, problem := range g.Problems {
		if utf8.RuneCountInString(problem) > 400 {
			return Generator{}, fmt.Errorf("tiles: a generator problem is longer than 400 characters")
		}
	}
	g.Fixes = maps.Clone(g.Fixes)
	g.Problems = slices.Clone(g.Problems)
	return g, nil
}

// LibraryTile is a custom tile of a library type: its definition, with its
// digest, and its data. Its ID and title are its definition's name and
// title. A pending tile is still being filled and has empty data.
type LibraryTile struct {
	Definition json.RawMessage `json:"definition"`
	Data       json.RawMessage `json:"data"`
	Pending    bool            `json:"pending,omitempty"`
}

// AuthoredTile is a table, code or prose tile (an exhibit) the tile
// analysis wrote. The ID's prefix is its type, and Data has that type's
// shape.
type AuthoredTile struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

// The limits of a review's extra tiles.
const (
	MaxAuthored     = 3
	MaxLibraryTiles = 8
	// MaxComposedTitle bounds an authored tile's title.
	MaxComposedTitle = 80
)

// MaxStoredComposition is the most bytes of a composition a host stores or
// reads: a normalised one is far smaller, so a larger one is not used.
const MaxStoredComposition = 256 << 10

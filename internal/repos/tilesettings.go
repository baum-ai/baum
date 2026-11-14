package repos

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
	"github.com/baum-ai/baum/tiles/library"
)

// Limits of a repository's tile preferences.
const (
	// MaxTileText is the longest instruction, in characters.
	MaxTileText = 1000
	// MaxTilePins is the most tiles a repository pins.
	MaxTilePins = 8
	// MaxTileTypes is the most tile types a repository saves.
	MaxTileTypes = 8
)

// ErrInvalidSettings reports settings the registry refuses; its message is
// the reason, in words a person can act on.
var ErrInvalidSettings = errors.New("invalid repository settings")

// TileSettings are a repository's standing preferences for the tile pages of
// its reviews. They outlive any pull request: Text is an instruction the
// composer of every later review in the repository follows, and Pins are
// tiles that lead its pages whenever they apply. They take effect in the
// next review generated with tile reviews on.
type TileSettings struct {
	// Types are at most MaxTileTypes saved tile type names.
	Types []string `json:"types,omitempty"`
	// Text is the instruction, at most MaxTileText characters.
	Text string `json:"text,omitempty"`
	// Pins are at most MaxTilePins tile IDs: a single built-in tile type
	// (overview, risks, evidence and so on) or a library tile type's name.
	Pins []string `json:"pins,omitempty"`
}

// checkedTiles validates and tidies tile settings: the text is trimmed, pins
// are stored as the tile IDs the composer matches (a library name becomes its
// tile's ID) with repeats dropped, and empty settings are nil. The error is
// the reason, wrapping ErrInvalidSettings.
func checkedTiles(t *TileSettings) (*TileSettings, error) {
	if t == nil {
		return nil, nil
	}
	out := TileSettings{Text: strings.TrimSpace(t.Text)}
	for _, name := range t.Types {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(out.Types, name) {
			out.Types = append(out.Types, name)
		}
	}
	if len(out.Types) > MaxTileTypes {
		return nil, fmt.Errorf("%w: %d tile types are saved; the most is %d", ErrInvalidSettings, len(out.Types), MaxTileTypes)
	}
	if n := utf8.RuneCountInString(out.Text); n > MaxTileText {
		return nil, fmt.Errorf("%w: the tile layout instruction is %d characters; the most is %d", ErrInvalidSettings, n, MaxTileText)
	}
	for _, pin := range t.Pins {
		pin = strings.TrimSpace(pin)
		if err := compose.CheckPin(pin); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
		}
		if e, ok := library.Find(pin); ok {
			pin = tiles.LibraryTileID(e.Definition.Name)
		}
		if !slices.Contains(out.Pins, pin) {
			out.Pins = append(out.Pins, pin)
		}
	}
	if len(out.Pins) > MaxTilePins {
		return nil, fmt.Errorf("%w: %d tiles are pinned; the most is %d", ErrInvalidSettings, len(out.Pins), MaxTilePins)
	}
	if out.Text == "" && len(out.Pins) == 0 && len(out.Types) == 0 {
		return nil, nil
	}
	return &out, nil
}

// Checked validates the settings a person or a proposal saves and returns
// them tidied, or an error wrapping ErrInvalidSettings whose message is the
// reason.
func (s Settings) Checked() (Settings, error) {
	var err error
	s.Tiles, err = checkedTiles(s.Tiles)
	return s, err
}

// mergedTiles applies a settings PUT's tiles to the stored ones, member by
// member: a member the request leaves out keeps its stored value, one it
// sends, even empty, replaces it, and tiles: null clears them all. sent is the
// request's raw tiles field (nil when absent) and got what it decoded to.
func mergedTiles(stored, got *TileSettings, sent json.RawMessage) *TileSettings {
	if sent == nil {
		return stored
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(sent, &members) != nil || members == nil {
		return nil
	}
	var out TileSettings
	if stored != nil {
		out = *stored
	}
	if got == nil {
		got = &TileSettings{}
	}
	if _, ok := members["types"]; ok {
		out.Types = got.Types
	}
	if _, ok := members["text"]; ok {
		out.Text = got.Text
	}
	if _, ok := members["pins"]; ok {
		out.Pins = got.Pins
	}
	if out.Text == "" && len(out.Pins) == 0 && len(out.Types) == 0 {
		return nil
	}
	return &out
}

package repos

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxTileTypes is the most tile types a repository pins.
const MaxTileTypes = 8

// ErrInvalidSettings reports settings the registry refuses; its message is
// the reason, in words a person can act on.
var ErrInvalidSettings = errors.New("invalid repository settings")

// TileSettings are a repository's standing tile settings: the saved tile
// types pinned to it, which every later review of the repository fills
// whenever tile reviews are on. Settings stored with the tile grid's
// layout instruction or pins keep only their types.
type TileSettings struct {
	// Types are at most MaxTileTypes saved tile type names.
	Types []string `json:"types,omitempty"`
}

// checkedTiles validates and tidies tile settings: repeats are dropped, and
// empty settings are nil. The error is the reason, wrapping
// ErrInvalidSettings.
func checkedTiles(t *TileSettings) (*TileSettings, error) {
	if t == nil {
		return nil, nil
	}
	var out TileSettings
	for _, name := range t.Types {
		name = strings.TrimSpace(name)
		if name != "" && !tileTypeName.MatchString(name) {
			return nil, fmt.Errorf("%w: %q is not a saved tile type's name: a lowercase letter, then lowercase letters, digits and underscores", ErrInvalidSettings, name)
		}
		if name != "" && !slices.Contains(out.Types, name) {
			out.Types = append(out.Types, name)
		}
	}
	if len(out.Types) > MaxTileTypes {
		return nil, fmt.Errorf("%w: %d tile types are saved; the most is %d", ErrInvalidSettings, len(out.Types), MaxTileTypes)
	}
	if len(out.Types) == 0 {
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
	if len(out.Types) == 0 {
		return nil
	}
	return &out
}

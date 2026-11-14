package compose

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/tiles"
)

// Reply is what a composer writes (its model view). Every property is
// required and absent values are explicit nulls, so the same schema serves
// providers' strict structured outputs.
type Reply struct {
	Entries          []ReplyEntry            `json:"entries"`
	ChangesShown     *int                    `json:"changes_shown"`
	Promote          []ReplyPromotion        `json:"promote"`
	Moves            []tiles.Move            `json:"moves"`
	CustomSuggestion *tiles.CustomSuggestion `json:"custom_suggestion"`
}

// ReplyEntry places one tile.
type ReplyEntry struct {
	Tile  string     `json:"tile"`
	Size  tiles.Size `json:"size"`
	Group *string    `json:"group"`
	Why   *string    `json:"why"`
}

// ReplyPromotion promotes the block an inventory handle names to a tile of
// its own.
type ReplyPromotion struct {
	Block string `json:"block"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// The reply's bounds.
const (
	MaxReplyEntries = 200
	MinChangesShown = 1
	MaxChangesShown = tiles.ChangesShownAll
)

// replySchema is the reply's JSON Schema.
var replySchema = fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["entries", "changes_shown", "promote", "moves", "custom_suggestion"],
  "properties": {
    "entries": {
      "type": "array",
      "maxItems": %[1]d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["tile", "size", "group", "why"],
        "properties": {
          "tile": {"type": "string", "pattern": "^[a-z][a-z0-9_]{0,127}$"},
          "size": {"type": "string", "enum": ["full", "half", "third"]},
          "group": {"type": ["string", "null"], "enum": ["changes", null]},
          "why": {"type": ["string", "null"], "maxLength": %[2]d}
        }
      }
    },
    "changes_shown": {"type": ["integer", "null"], "minimum": %[3]d, "maximum": %[4]d},
    "promote": {
      "type": "array",
      "maxItems": %[5]d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["block", "id", "title"],
        "properties": {
          "block": {"type": "string", "pattern": "^blk_[0-9]{1,4}$"},
          "id": {"type": "string", "pattern": "^(table|code)_[a-z0-9_]{1,60}$"},
          "title": {"type": "string", "minLength": 1, "maxLength": %[6]d}
        }
      }
    },
    "moves": {
      "type": "array",
      "maxItems": %[7]d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["tile", "why"],
        "properties": {
          "tile": {"type": "string", "maxLength": 128},
          "why": {"type": "string", "maxLength": %[2]d}
        }
      }
    },
    "custom_suggestion": {
      "anyOf": [
        {"type": "null"},
        {
          "type": "object",
          "additionalProperties": false,
          "required": ["title", "shows", "why"],
          "properties": {
            "title": {"type": "string", "maxLength": %[6]d},
            "shows": {"type": "string", "maxLength": 300},
            "why": {"type": "string", "maxLength": 300}
          }
        }
      ]
    }
  }
}`, MaxReplyEntries, tiles.MaxWhy, MinChangesShown, MaxChangesShown, tiles.MaxPromotions, tiles.MaxComposedTitle, tiles.MaxMoves)

// ReplySchema returns the reply's JSON Schema.
func ReplySchema() []byte { return []byte(replySchema) }

// hostSchema is the schema a reply is checked against: ReplySchema without
// its string length bounds, which normalisation enforces by cutting the text
// (a reason, a title), so an over-long reason costs no repair call.
var hostSchema = regexp.MustCompile(`,?\s*"maxLength": \d+`).ReplaceAllString(replySchema, "")

var resolvedReply = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(hostSchema), &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
})

// maxProblems bounds the problems ParseReply reports.
const maxProblems = 5

// ParseReply decodes a reply and checks it against ReplySchema, but for
// the strings' lengths, which normalisation bounds. It returns
// the problems, plain sentences fit for a repair prompt, when the reply is
// not a JSON object of the schema.
func ParseReply(raw []byte) (Reply, []string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Reply{}, []string{"the reply is not valid JSON"}
	}
	if _, ok := value.(map[string]any); !ok {
		return Reply{}, []string{"the reply is not a JSON object"}
	}
	resolved, err := resolvedReply()
	if err != nil {
		return Reply{}, []string{"the reply schema does not compile: " + err.Error()}
	}
	if err := resolved.Validate(value); err != nil {
		var problems []string
		for _, line := range strings.Split(err.Error(), "\n") {
			if line = strings.TrimSpace(line); line != "" && len(problems) < maxProblems {
				problems = append(problems, clip(line, 300))
			}
		}
		return Reply{}, problems
	}
	var reply Reply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Reply{}, []string{"the reply does not decode: " + clip(err.Error(), 300)}
	}
	return reply, nil
}

// Composition maps a reply onto a composition, before the host stabilises its
// IDs and normalises it: the entries become the layout, their reasons the
// notes, and each promotion names the change and block position its handle
// stands for. A handle the inventory does not have gives a promotion of no
// change, which normalisation drops and counts.
func (inv *Inventory) Composition(r Reply) tiles.Composition {
	c := tiles.Composition{Schema: tiles.CompositionName, Layout: tiles.CompositionLayout{Entries: []tiles.Entry{}}}
	notes := &tiles.Notes{}
	for _, entry := range r.Entries {
		group := ""
		if entry.Group != nil {
			group = *entry.Group
		}
		c.Layout.Entries = append(c.Layout.Entries, tiles.Entry{Tile: entry.Tile, Size: entry.Size, Group: group})
		if entry.Why != nil && strings.TrimSpace(*entry.Why) != "" {
			if notes.Why == nil {
				notes.Why = map[string]string{}
			}
			if _, ok := notes.Why[entry.Tile]; !ok {
				notes.Why[entry.Tile] = strings.TrimSpace(*entry.Why)
			}
		}
	}
	if r.ChangesShown != nil {
		shown := *r.ChangesShown
		c.Layout.ChangesShown = &shown
	}
	for _, p := range r.Promote {
		block, ok := inv.Blocks[p.Block]
		if !ok {
			c.Promotions = append(c.Promotions, tiles.Promotion{ID: p.ID, Block: -1, Title: p.Title})
			continue
		}
		c.Promotions = append(c.Promotions, tiles.Promotion{ID: p.ID, Change: block.Change, Block: block.Block, Title: p.Title})
	}
	notes.Moves = append(notes.Moves, r.Moves...)
	notes.CustomSuggestion = r.CustomSuggestion
	if notes.Why != nil || len(notes.Moves) > 0 || notes.CustomSuggestion != nil {
		c.Notes = notes
	}
	return c
}

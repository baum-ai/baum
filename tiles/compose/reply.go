package compose

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

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
      "minItems": 1,
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

// maxProblems bounds the problems ParseReply reports.
const maxProblems = 5

var (
	tilePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	handlePattern    = regexp.MustCompile(`^blk_[0-9]{1,4}$`)
	promotionPattern = regexp.MustCompile(`^(table|code)_[a-z0-9_]{1,60}$`)
)

// replyChecker collects a reply's problems in the host's words: a JSON
// pointer and the rule it breaks, never the value it holds.
type replyChecker struct{ problems []string }

func (c *replyChecker) fail(at, rule string) {
	if at == "" {
		at = "/"
	}
	if len(c.problems) < maxProblems {
		c.problems = append(c.problems, at+": "+rule)
	}
}

// object checks that v is an object with exactly the keys, and returns it.
func (c *replyChecker) object(at string, v any, keys ...string) (map[string]any, bool) {
	o, ok := v.(map[string]any)
	if !ok {
		c.fail(at, "must be an object")
		return nil, false
	}
	for _, key := range keys {
		if _, ok := o[key]; !ok {
			c.fail(at, fmt.Sprintf("must have the property %q", key))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(o)) {
		if !slices.Contains(keys, key) {
			c.fail(at, "has a property the schema does not allow")
			break
		}
	}
	return o, true
}

func (c *replyChecker) array(at string, v any, lo, hi int) []any {
	a, ok := v.([]any)
	switch {
	case !ok:
		c.fail(at, "must be an array")
	case len(a) < lo:
		c.fail(at, fmt.Sprintf("must have at least %d items", lo))
	case len(a) > hi:
		c.fail(at, fmt.Sprintf("must have at most %d items", hi))
	}
	return a
}

// text checks a string, optionally null, against a pattern or a set of
// values. Its length is not checked: normalisation bounds it.
func (c *replyChecker) text(at string, v any, nullable bool, pattern *regexp.Regexp, values ...string) {
	if v == nil && nullable {
		return
	}
	s, ok := v.(string)
	switch {
	case !ok && nullable:
		c.fail(at, "must be a string or null")
	case !ok:
		c.fail(at, "must be a string")
	case pattern != nil && !pattern.MatchString(s):
		c.fail(at, "does not match "+pattern.String())
	case len(values) > 0 && !slices.Contains(values, s):
		c.fail(at, "must be one of "+strings.Join(values, ", "))
	}
}

// ParseReply decodes a reply and checks it against ReplySchema, but for the
// strings' lengths, which normalisation bounds. It returns up to five
// problems, each a JSON pointer and the rule broken in the host's words (it
// never quotes the reply), when the reply is not a JSON object of the schema.
func ParseReply(raw []byte) (Reply, []string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Reply{}, []string{"/: the reply is not valid JSON"}
	}
	c := &replyChecker{}
	root, ok := c.object("", value, "entries", "changes_shown", "promote", "moves", "custom_suggestion")
	if !ok {
		return Reply{}, c.problems
	}
	for i, item := range c.array("/entries", root["entries"], 1, MaxReplyEntries) {
		at := fmt.Sprintf("/entries/%d", i)
		if e, ok := c.object(at, item, "tile", "size", "group", "why"); ok {
			c.text(at+"/tile", e["tile"], false, tilePattern)
			c.text(at+"/size", e["size"], false, nil, string(tiles.SizeFull), string(tiles.SizeHalf), string(tiles.SizeThird))
			c.text(at+"/group", e["group"], true, nil, tiles.GroupChanges)
			c.text(at+"/why", e["why"], true, nil)
		}
	}
	if shown, present := root["changes_shown"]; present && shown != nil {
		n, ok := shown.(float64)
		if !ok || n != float64(int(n)) || n < MinChangesShown || n > MaxChangesShown {
			c.fail("/changes_shown", fmt.Sprintf("must be null or an integer from %d to %d", MinChangesShown, MaxChangesShown))
		}
	}
	for i, item := range c.array("/promote", root["promote"], 0, tiles.MaxPromotions) {
		at := fmt.Sprintf("/promote/%d", i)
		if p, ok := c.object(at, item, "block", "id", "title"); ok {
			c.text(at+"/block", p["block"], false, handlePattern)
			c.text(at+"/id", p["id"], false, promotionPattern)
			if title, _ := p["title"].(string); title == "" {
				c.fail(at+"/title", "must be a non-empty string")
			}
		}
	}
	for i, item := range c.array("/moves", root["moves"], 0, tiles.MaxMoves) {
		at := fmt.Sprintf("/moves/%d", i)
		if m, ok := c.object(at, item, "tile", "why"); ok {
			c.text(at+"/tile", m["tile"], false, nil)
			c.text(at+"/why", m["why"], false, nil)
		}
	}
	if s := root["custom_suggestion"]; s != nil {
		if o, ok := c.object("/custom_suggestion", s, "title", "shows", "why"); ok {
			for _, key := range []string{"title", "shows", "why"} {
				c.text("/custom_suggestion/"+key, o[key], false, nil)
			}
		}
	}
	if len(c.problems) > 0 {
		return Reply{}, c.problems
	}
	var reply Reply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return Reply{}, []string{"/: the reply does not decode as a reply"}
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

package tilelibrary

import (
	"encoding/json"
	"strings"
)

// Invisible reports a character that draws nothing yet changes how text
// reads or what a model takes from it: bidirectional controls (they reorder
// text on screen), zero-width characters and Unicode tag characters (they
// carry hidden text). A file or save never keeps them in the texts a person
// reviews or the review agent is given.
func Invisible(r rune) bool {
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F: // Arabic letter mark, LRM, RLM
		return true
	case r >= 0x202A && r <= 0x202E: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r >= 0x200B && r <= 0x200D: // zero-width space, non-joiner, joiner
		return true
	case r == 0x2060, r == 0x2061, r == 0x2062, r == 0x2063, r == 0x2064, r == 0xFEFF, r == 0x180E, r == 0x00AD:
		return true
	case r >= 0xE0000 && r <= 0xE007F: // tag characters
		return true
	}
	return false
}

// StripInvisible removes every Invisible character from text.
func StripInvisible(text string) string {
	if !strings.ContainsFunc(text, Invisible) {
		return text
	}
	return strings.Map(func(r rune) rune {
		if Invisible(r) {
			return -1
		}
		return r
	}, text)
}

// sanitize returns the change with the invisible characters removed from the
// texts that are read by a person and by the review agent: the definition's
// title, use-when line and fill instructions, the glance and the review
// checks. The definition is left unsealed when nothing changed.
func sanitize(c Change) (Change, error) {
	c.Glance = StripInvisible(c.Glance)
	c.ReviewChecks = StripInvisible(c.ReviewChecks)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(c.Definition, &members); err != nil || members == nil {
		return c, nil
	}
	changed := false
	for _, key := range []string{"title", "use_when", "fill"} {
		raw, ok := members[key]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) != nil {
			continue
		}
		if clean := StripInvisible(text); clean != text {
			members[key], _ = json.Marshal(clean)
			changed = true
		}
	}
	if !changed {
		return c, nil
	}
	encoded, err := marshal(members)
	if err != nil {
		return c, err
	}
	c.Definition = encoded
	return c, nil
}

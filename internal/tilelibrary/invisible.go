package tilelibrary

import (
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/internal/invisible"
)

// Invisible reports a character that draws nothing yet changes how text
// reads or what a model takes from it: bidirectional controls (they reorder
// text on screen), zero-width characters, Unicode tag characters (they
// carry hidden text), variation selectors and the other default-ignorable
// code points (see invisible.Is). A file or save never keeps them in the texts a person
// reviews or the review agent is given.
func Invisible(r rune) bool { return invisible.Is(r) }

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

package reviewv2

import (
	"encoding/json"
	"slices"
)

// OwnerFor is the owner of a tile added to the document: asked when it is
// context or one of the document's current sections, else the current section
// whose hunks the covered hunks meet most, the first on a tie, else context.
// A stale section, whose code is gone, is never chosen.
func OwnerFor(d Document, asked string, covered []string) string {
	if asked == OwnerContext {
		return asked
	}
	best, most := OwnerContext, 0
	for _, s := range d.Sections {
		if isStale(s) {
			continue
		}
		if s.ID == asked {
			return asked
		}
		n := 0
		for _, h := range covered {
			if slices.Contains(s.Hunks, h) {
				n++
			}
		}
		if n > most {
			best, most = s.ID, n
		}
	}
	return best
}

func isStale(s Section) bool {
	var derived struct {
		State string `json:"state"`
	}
	return json.Unmarshal(s.Derived, &derived) == nil && derived.State == "stale"
}

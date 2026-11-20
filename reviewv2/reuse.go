package reviewv2

import (
	"slices"
)

// Reused is a previous version's section a new version keeps without
// writing it again: its body and active risks (without their
// lifecycle fields, which Complete gives them again) and its ready tiles.
type Reused struct {
	Section Section
	Tiles   []Tile
}

// ReusableSections returns, by section ID, the sections of the previous
// version's stored document (either schema) that a new map gives unchanged:
// the same ID and the same hunks (hunk IDs hash their content, so the same
// set is the same code, as baum.review/1's ReusableSectionDetails reads
// them). A previous section whose details are pending or failed is not
// reusable. Only the section agent's fields are taken (body,
// risks); the map's fields (title, gist, level) are the new map's. A
// reusable section's tiles are its ready tiles in the previous version.
//
// A host that offers images the previous version was not offered should
// reuse nothing (review.NewImages), since only a section agent places them.
func ReusableSections(previous []byte, sections []Section) map[string]Reused {
	if len(previous) == 0 {
		return nil
	}
	prior, err := Read(previous)
	if err != nil {
		return nil
	}
	byID := map[string]Section{}
	for _, s := range prior.Sections {
		byID[s.ID] = s
	}
	out := map[string]Reused{}
	for _, s := range sections {
		old, ok := byID[s.ID]
		if !ok || old.Details != "" || !sameHunks(old.Hunks, s.Hunks) {
			continue
		}
		reused := s
		reused.Body = slices.Clone(old.Body)
		reused.Risks = []Risk{}
		for _, r := range old.Risks {
			if r.State == "mitigated" {
				continue
			}
			r.State, r.IntroducedVersion, r.MitigatedVersion = "", nil, nil
			reused.Risks = append(reused.Risks, r)
		}
		reused.Details, reused.Derived = "", nil
		var kept []Tile
		for _, t := range prior.Tiles {
			if t.Owner == s.ID && t.State == TileReady && !t.Added {
				kept = append(kept, t)
			}
		}
		out[s.ID] = Reused{Section: reused, Tiles: kept}
	}
	return out
}

func sameHunks(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

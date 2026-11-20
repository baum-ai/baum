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
		reused.Body, reused.Risks = writtenText(old)
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

// Revised is a previous version's section that a new map gives again under
// the same ID but over other hunks, so its agent writes it again starting
// from the previous text. Previous is that section with the text Reused
// takes (its body, and its active risks without their lifecycle fields).
// Unchanged are the hunks both versions give the section, New those only
// the new map gives it, and Gone those only the previous version gave it,
// with where they were then.
type Revised struct {
	Previous  Section
	Unchanged []string
	New       []string
	Gone      []GoneHunk
}

// GoneHunk is a hunk the previous version gave a section and the new map
// does not: its path and its new side's first line and line count in the
// previous version's index (empty and 0 when the index does not say).
type GoneHunk struct {
	ID           string
	Path         string
	Start, Count int
}

// RevisableSections returns, by section ID, the logical sections of the
// previous version's stored document (either schema) whose text was complete
// and not empty and that a new map gives, logical with hunks, under the same
// ID over other hunks. A section ReusableSections returns never is one.
func RevisableSections(previous []byte, sections []Section) map[string]Revised {
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
	refs := RefsOfIndex(prior.Index)
	out := map[string]Revised{}
	for _, s := range sections {
		old, ok := byID[s.ID]
		if !ok || s.Kind != KindLogical || len(s.Hunks) == 0 || old.Kind != KindLogical || old.Details != "" || len(old.Body) == 0 || sameHunks(old.Hunks, s.Hunks) {
			continue
		}
		r := Revised{Previous: Section{ID: old.ID, Kind: old.Kind, Hunks: slices.Clone(old.Hunks)}}
		r.Previous.Body, r.Previous.Risks = writtenText(old)
		for _, h := range s.Hunks {
			if slices.Contains(old.Hunks, h) {
				r.Unchanged = append(r.Unchanged, h)
			} else {
				r.New = append(r.New, h)
			}
		}
		for _, h := range old.Hunks {
			if !slices.Contains(s.Hunks, h) {
				ref := refs.Hunks[h]
				r.Gone = append(r.Gone, GoneHunk{ID: h, Path: ref.Path, Start: ref.Start, Count: ref.Count})
			}
		}
		out[s.ID] = r
	}
	return out
}

// writtenText is a previous section's body and its active risks, without
// their lifecycle fields: Complete gives them again, and a risk keeps its ID,
// so its history carries.
func writtenText(old Section) ([]Block, []Risk) {
	risks := []Risk{}
	for _, r := range old.Risks {
		if r.State == "mitigated" {
			continue
		}
		r.State, r.IntroducedVersion, r.MitigatedVersion = "", nil, nil
		risks = append(risks, r)
	}
	return slices.Clone(old.Body), risks
}

func sameHunks(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

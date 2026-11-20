package reviewv2

import (
	"encoding/json"
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
// takes (its body, and its active risks without their lifecycle fields);
// Fixed are the IDs of its mitigated risks, which a new risk may not take.
//
// Its hunks against the previous version's: Unchanged are in both; New are
// only in the new section and were in no previous section; Joined were in
// another previous section (unchanged code that version reviewed there),
// with JoinedRisks, that section's active risks citing them; Moved were in
// the previous section and are in another section now; Gone were in the
// previous section and are in no section now (the pull request no longer
// has them), with where they were then.
type Revised struct {
	Previous    Section
	Fixed       []string
	Unchanged   []string
	New         []string
	Joined      []JoinedHunk
	JoinedRisks []JoinedRisk
	Moved       []MovedHunk
	Gone        []GoneHunk
}

// Additive reports whether the change only adds code to what the previous
// text described: some of its hunks are unchanged and none moved out or is
// gone. Only then may the previous text be kept as it was.
func (r Revised) Additive() bool {
	return len(r.Unchanged) > 0 && len(r.Moved) == 0 && len(r.Gone) == 0
}

// JoinedHunk is a hunk a section has now that another section of the
// previous version (From) had.
type JoinedHunk struct {
	ID, From string
}

// JoinedRisk is an active risk of another previous section (From), without
// its lifecycle fields, that cites a hunk joined to a section.
type JoinedRisk struct {
	From string
	Risk Risk
}

// MovedHunk is a hunk a section had in the previous version that another
// section (To) has now.
type MovedHunk struct {
	ID, To string
}

// GoneHunk is a hunk the previous version gave a section and no section of
// the new map has: its path and its new side's first line and line count in
// the previous version's index (empty and 0 when the index does not say).
type GoneHunk struct {
	ID           string
	Path         string
	Start, Count int
}

// RevisableSections returns, by section ID, the logical sections of the
// previous version's stored baum.review/2 document whose text was complete
// and not empty and that a new map (sections, every section of it) gives,
// logical with hunks, under the same ID over other hunks. A section
// ReusableSections returns never is one. A baum.review/1 previous version
// gives none: its converted text is not a body its agent wrote.
func RevisableSections(previous []byte, sections []Section) map[string]Revised {
	if len(previous) == 0 || !IsV2(previous) {
		return nil
	}
	prior, err := Read(previous)
	if err != nil {
		return nil
	}
	byID := map[string]Section{}
	priorOwner := map[string]string{}
	for _, s := range prior.Sections {
		byID[s.ID] = s
		for _, h := range s.Hunks {
			priorOwner[h] = s.ID
		}
	}
	owner := map[string]string{}
	for _, s := range sections {
		for _, h := range s.Hunks {
			if _, taken := owner[h]; !taken {
				owner[h] = s.ID
			}
		}
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
		for _, risk := range old.Risks {
			if risk.State == "mitigated" {
				r.Fixed = append(r.Fixed, risk.ID)
			}
		}
		joined := map[string]bool{}
		for _, h := range s.Hunks {
			from, had := priorOwner[h]
			switch {
			case slices.Contains(old.Hunks, h):
				r.Unchanged = append(r.Unchanged, h)
			case had:
				r.Joined = append(r.Joined, JoinedHunk{ID: h, From: from})
				joined[h] = true
			default:
				r.New = append(r.New, h)
			}
		}
		for _, other := range prior.Sections {
			if other.ID == s.ID {
				continue
			}
			_, active := writtenText(other)
			for _, risk := range active {
				if slices.ContainsFunc(CitedHunks(risk.Cites), func(h string) bool { return joined[h] }) {
					r.JoinedRisks = append(r.JoinedRisks, JoinedRisk{From: other.ID, Risk: risk})
				}
			}
		}
		for _, h := range old.Hunks {
			if slices.Contains(s.Hunks, h) {
				continue
			}
			if to, ok := owner[h]; ok {
				r.Moved = append(r.Moved, MovedHunk{ID: h, To: to})
				continue
			}
			ref := refs.Hunks[h]
			r.Gone = append(r.Gone, GoneHunk{ID: h, Path: ref.Path, Start: ref.Start, Count: ref.Count})
		}
		out[s.ID] = r
	}
	return out
}

// CitedHunks are the IDs of the hunks among cites.
func CitedHunks(cites []Anchor) []string {
	var out []string
	for _, raw := range cites {
		var a anchor
		if json.Unmarshal(raw, &a) == nil && a.Type == "hunk" {
			if id := a.id(); id != "" {
				out = append(out, id)
			}
		}
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

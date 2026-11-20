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
// takes (its body, and its active risks without their lifecycle fields).
//
// Its hunks against the previous version's: Unchanged are in both; New are
// only in the new section and were in no previous section; Joined were in
// another previous section (unchanged code that version reviewed there),
// with JoinedRisks, the risks handed over with them (see Joined); Moved
// were in the previous section and are in another section now; Gone were
// in the previous section and are in no section now (the pull request no
// longer has them), with where they were then.
type Revised struct {
	Previous    Section
	Unchanged   []string
	New         []string
	Joined      []JoinedHunk
	JoinedRisks []JoinedRisk
	Moved       []MovedHunk
	Gone        []GoneHunk
}

// Additive reports whether the change only adds code to what the previous
// text described: some of its hunks are unchanged, none moved out or is
// gone, and no risk was handed over with joined code (the section must
// weigh it). Only then may the previous text be kept as it was.
func (r Revised) Additive() bool {
	return len(r.Unchanged) > 0 && len(r.Moved) == 0 && len(r.Gone) == 0 && len(r.JoinedRisks) == 0
}

// Joined is what a new section takes over from other sections of the
// previous version: the hunks they had (unchanged code), and the active
// risks handed over with them.
type Joined struct {
	Hunks []JoinedHunk
	Risks []JoinedRisk
}

// JoinedHunk is a hunk a section has now that another section of the
// previous version (From) had.
type JoinedHunk struct {
	ID, From string
}

// JoinedRisk is an active risk of another previous section (From), without
// its lifecycle fields, handed over with joined code (HandedOver) to the
// section that has the first of its hunks any section has.
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

// HandedOver reports whether a previous risk citing the hunks cited goes
// with its code to another section: it cites a hunk the receiving section
// has (has), and none of the hunks it cites is among stayed, its old
// section's hunks now (none when the new map has no section of that ID).
func HandedOver(cited, stayed []string, has func(string) bool) bool {
	if !slices.ContainsFunc(cited, has) {
		return false
	}
	return !slices.ContainsFunc(cited, func(h string) bool { return slices.Contains(stayed, h) })
}

// heir is the section a handed-over risk goes to when its code split: the
// one that has, of the hunks it cites (cited, in its cite order), the first
// any section has (owner, by hunk); "" when none has one.
func heir(cited []string, owner map[string]string) string {
	for _, h := range cited {
		if s, ok := owner[h]; ok {
			return s
		}
	}
	return ""
}

// versions is a previous baum.review/2 document against a new map (every
// section of it): which section had and has each hunk.
type versions struct {
	prior      Document
	byID       map[string]Section
	priorOwner map[string]string
	owner      map[string]string
	hunks      map[string][]string
}

// compare reads a previous baum.review/2 document against a new map. A
// baum.review/1 previous version gives nothing: its converted text is not a
// body or risks an agent of this version wrote.
func compare(previous []byte, sections []Section) (*versions, bool) {
	if len(previous) == 0 || !IsV2(previous) {
		return nil, false
	}
	prior, err := Read(previous)
	if err != nil {
		return nil, false
	}
	v := &versions{prior: prior, byID: map[string]Section{}, priorOwner: map[string]string{}, owner: map[string]string{}, hunks: map[string][]string{}}
	for _, s := range prior.Sections {
		v.byID[s.ID] = s
		for _, h := range s.Hunks {
			v.priorOwner[h] = s.ID
		}
	}
	for _, s := range sections {
		v.hunks[s.ID] = s.Hunks
		for _, h := range s.Hunks {
			if _, taken := v.owner[h]; !taken {
				v.owner[h] = s.ID
			}
		}
	}
	return v, true
}

// joined is what s takes over from other previous sections.
func (v *versions) joined(s Section) Joined {
	var out Joined
	took := map[string]bool{}
	for _, h := range s.Hunks {
		from, had := v.priorOwner[h]
		if had && from != s.ID && !slices.Contains(v.byID[s.ID].Hunks, h) {
			out.Hunks = append(out.Hunks, JoinedHunk{ID: h, From: from})
			took[h] = true
		}
	}
	if len(took) == 0 {
		return out
	}
	for _, other := range v.prior.Sections {
		if other.ID == s.ID {
			continue
		}
		_, active := writtenText(other)
		for _, risk := range active {
			cited := CitedHunks(risk.Cites)
			if heir(cited, v.owner) == s.ID && HandedOver(cited, v.hunks[other.ID], func(h string) bool { return took[h] }) {
				out.Risks = append(out.Risks, JoinedRisk{From: other.ID, Risk: risk})
			}
		}
	}
	return out
}

// RevisableSections returns, by section ID, the logical sections of the
// previous version's stored baum.review/2 document whose text was complete
// and not empty and that a new map (sections, every section of it) gives,
// logical with hunks, under the same ID over other hunks. A section
// ReusableSections returns never is one; a baum.review/1 previous version
// gives none.
func RevisableSections(previous []byte, sections []Section) map[string]Revised {
	v, ok := compare(previous, sections)
	if !ok {
		return nil
	}
	refs := RefsOfIndex(v.prior.Index)
	out := map[string]Revised{}
	for _, s := range sections {
		old, ok := v.byID[s.ID]
		if !ok || s.Kind != KindLogical || len(s.Hunks) == 0 || old.Kind != KindLogical || old.Details != "" || len(old.Body) == 0 || sameHunks(old.Hunks, s.Hunks) {
			continue
		}
		r := Revised{Previous: Section{ID: old.ID, Kind: old.Kind, Hunks: slices.Clone(old.Hunks)}}
		r.Previous.Body, r.Previous.Risks = writtenText(old)
		j := v.joined(s)
		r.Joined, r.JoinedRisks = j.Hunks, j.Risks
		for _, h := range s.Hunks {
			switch {
			case slices.Contains(old.Hunks, h):
				r.Unchanged = append(r.Unchanged, h)
			case !slices.ContainsFunc(j.Hunks, func(x JoinedHunk) bool { return x.ID == h }):
				r.New = append(r.New, h)
			}
		}
		for _, h := range old.Hunks {
			if slices.Contains(s.Hunks, h) {
				continue
			}
			if to, ok := v.owner[h]; ok {
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

// JoinedSections returns, by section ID, what each logical section with
// hunks of a new map (sections, every section of it) takes over from other
// sections of the previous baum.review/2 version, for each that takes over
// a hunk: a section with a new ID as well as a revised one.
func JoinedSections(previous []byte, sections []Section) map[string]Joined {
	v, ok := compare(previous, sections)
	if !ok {
		return nil
	}
	out := map[string]Joined{}
	for _, s := range sections {
		if s.Kind != KindLogical || len(s.Hunks) == 0 {
			continue
		}
		if j := v.joined(s); len(j.Hunks) > 0 {
			out[s.ID] = j
		}
	}
	return out
}

// FixedRiskIDs are the IDs of the risks a previous version's stored
// document (either schema) holds mitigated and nowhere active, sorted: a new
// risk may not take one, or the fixed risk's history would revive.
func FixedRiskIDs(previous []byte) []string {
	if len(previous) == 0 {
		return nil
	}
	prior, err := Read(previous)
	if err != nil {
		return nil
	}
	fixed, active := map[string]bool{}, map[string]bool{}
	for _, s := range prior.Sections {
		for _, r := range s.Risks {
			if r.State == "mitigated" {
				fixed[r.ID] = true
			} else {
				active[r.ID] = true
			}
		}
	}
	var out []string
	for id := range fixed {
		if !active[id] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
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

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
// the same ID but over other hunks, or under a new ID over its code
// (RenamedSections), so its agent writes it again starting from the
// previous text. Previous is that section (its ID and title, and the text
// Reused takes: its body, and its active risks without their lifecycle
// fields).
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
// section of it): which section had and has each hunk, and which previous
// section each new ID continues (RenamedSections).
type versions struct {
	prior      Document
	byID       map[string]Section
	priorOwner map[string]string
	owner      map[string]string
	hunks      map[string][]string
	renamed    map[string]string
	successor  map[string]string
}

// predecessor is the previous section a new section continues: the one of
// its ID, or the one it was renamed from.
func (v *versions) predecessor(id string) string {
	if old, ok := v.renamed[id]; ok {
		return old
	}
	return id
}

// stayed are a previous section's hunks its successor has now: none when no
// section of the new map continues it.
func (v *versions) stayed(old string) []string {
	if next, ok := v.successor[old]; ok {
		return v.hunks[next]
	}
	return v.hunks[old]
}

// compare reads a previous baum.review/2 document against a new map, whose
// hunks current places. A baum.review/1 previous version gives nothing: its
// converted text is not a body or risks an agent of this version wrote.
func compare(previous []byte, sections []Section, current *Refs) (*versions, bool) {
	if len(previous) == 0 || !IsV2(previous) {
		return nil, false
	}
	prior, err := Read(previous)
	if err != nil {
		return nil, false
	}
	v := &versions{prior: prior, byID: map[string]Section{}, priorOwner: map[string]string{}, owner: map[string]string{}, hunks: map[string][]string{},
		renamed: renames(prior, sections, current), successor: map[string]string{}}
	for next, old := range v.renamed {
		v.successor[old] = next
	}
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
	pred := v.predecessor(s.ID)
	for _, h := range s.Hunks {
		from, had := v.priorOwner[h]
		if had && from != pred && !slices.Contains(v.byID[pred].Hunks, h) {
			out.Hunks = append(out.Hunks, JoinedHunk{ID: h, From: from})
			took[h] = true
		}
	}
	if len(took) == 0 {
		return out
	}
	for _, other := range v.prior.Sections {
		if other.ID == pred {
			continue
		}
		_, active := writtenText(other)
		for _, risk := range active {
			cited := CitedHunks(risk.Cites)
			if heir(cited, v.owner) == s.ID && HandedOver(cited, v.stayed(other.ID), func(h string) bool { return took[h] }) {
				out.Risks = append(out.Risks, JoinedRisk{From: other.ID, Risk: risk})
			}
		}
	}
	return out
}

// RevisableSections returns, by section ID, the logical sections of the
// previous version's stored baum.review/2 document whose text was complete
// and not empty and that a new map (sections, every section of it, its
// hunks placed by current) gives, logical with hunks, under the same ID
// over other hunks, or under a new ID over its code (RenamedSections; its
// Previous then has the old ID and title). A section ReusableSections
// returns never is one; a baum.review/1 previous version gives none.
func RevisableSections(previous []byte, sections []Section, current *Refs) map[string]Revised {
	v, ok := compare(previous, sections, current)
	if !ok {
		return nil
	}
	refs := RefsOfIndex(v.prior.Index)
	out := map[string]Revised{}
	for _, s := range sections {
		old, ok := v.byID[v.predecessor(s.ID)]
		if !ok || s.Kind != KindLogical || len(s.Hunks) == 0 || old.Kind != KindLogical || old.Details != "" || len(old.Body) == 0 || old.ID == s.ID && sameHunks(old.Hunks, s.Hunks) {
			continue
		}
		r := Revised{Previous: Section{ID: old.ID, Title: old.Title, Kind: old.Kind, Hunks: slices.Clone(old.Hunks)}}
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
// hunks of a new map (sections, every section of it, its hunks placed by
// current) takes over from other sections of the previous baum.review/2
// version, for each that takes over a hunk: a section with a new ID as well
// as a revised one. The section a renamed one continues is not another.
func JoinedSections(previous []byte, sections []Section, current *Refs) map[string]Joined {
	v, ok := compare(previous, sections, current)
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

// RenamedSections returns, by section ID, the previous logical section (its
// ID) that each logical section with hunks of a new map continues under an
// ID the previous baum.review/2 version did not have: its code is clearly
// that section's code, and the new map has no section of that ID. sections
// is every section of the new map; current places its hunks (the bundle's
// diff). A baum.review/1 previous version gives none.
//
// A pair matches on the hunks they share (the same IDs are the same code),
// or on hunks of one path whose new-side lines overlap (an edited hunk; each
// version numbers its own new file, so code added above shifts them and this
// is a heuristic), or on the path they share only when it is the one file of
// both. Shared paths alone say too little: two parts of a change often edit
// one file. The best pairs are taken first: most shared hunks, then most
// overlapping hunks, then most shared paths, then the earlier section of the
// new map and then of the previous version. A section on either side is in
// at most one pair.
func RenamedSections(previous []byte, sections []Section, current *Refs) map[string]string {
	if len(previous) == 0 || !IsV2(previous) {
		return nil
	}
	prior, err := Read(previous)
	if err != nil {
		return nil
	}
	return renames(prior, sections, current)
}

// codeMatch is how much code a new section and a previous one share.
type codeMatch struct {
	hunks, overlapping, paths int
}

func (m codeMatch) compare(o codeMatch) int {
	switch {
	case m.hunks != o.hunks:
		return m.hunks - o.hunks
	case m.overlapping != o.overlapping:
		return m.overlapping - o.overlapping
	}
	return m.paths - o.paths
}

// matchCode measures a new section's hunks (placed by current) against a
// previous section's (placed by before, its index), and says whether they
// are clearly the same code.
func matchCode(next []string, current *Refs, old []string, before *Refs) (codeMatch, bool) {
	var m codeMatch
	nextPaths, oldPaths := map[string]bool{}, map[string]bool{}
	for _, h := range old {
		if p := before.Hunks[h].Path; p != "" {
			oldPaths[p] = true
		}
	}
	for _, h := range next {
		shared := slices.Contains(old, h)
		if shared {
			m.hunks++
		}
		ref := current.Hunks[h]
		if ref.Path == "" {
			continue
		}
		nextPaths[ref.Path] = true
		if shared || ref.Start < 1 {
			continue
		}
		last := ref.Start + max(ref.Count, 1) - 1
		if slices.ContainsFunc(old, func(o string) bool {
			was := before.Hunks[o]
			return was.Path == ref.Path && was.Start > 0 && was.Start <= last && ref.Start <= was.Start+max(was.Count, 1)-1
		}) {
			m.overlapping++
		}
	}
	for p := range nextPaths {
		if oldPaths[p] {
			m.paths++
		}
	}
	onlyFile := m.paths == 1 && len(nextPaths) == 1 && len(oldPaths) == 1
	return m, m.hunks > 0 || m.overlapping > 0 || onlyFile
}

// renames is RenamedSections of a previous version read.
func renames(prior Document, sections []Section, current *Refs) map[string]string {
	if current == nil {
		current = newRefs()
	}
	before := RefsOfIndex(prior.Index)
	inMap, inPrior := map[string]bool{}, map[string]bool{}
	for _, s := range sections {
		inMap[s.ID] = true
	}
	for _, s := range prior.Sections {
		inPrior[s.ID] = true
	}
	type pair struct {
		next, old int
		match     codeMatch
	}
	var pairs []pair
	for i, s := range sections {
		if s.Kind != KindLogical || len(s.Hunks) == 0 || inPrior[s.ID] {
			continue
		}
		for j, old := range prior.Sections {
			if old.Kind != KindLogical || len(old.Hunks) == 0 || inMap[old.ID] {
				continue
			}
			if m, ok := matchCode(s.Hunks, current, old.Hunks, before); ok {
				pairs = append(pairs, pair{i, j, m})
			}
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := b.match.compare(a.match); c != 0 {
			return c
		}
		if a.next != b.next {
			return a.next - b.next
		}
		return a.old - b.old
	})
	out := map[string]string{}
	taken := map[int]bool{}
	for _, p := range pairs {
		id := sections[p.next].ID
		if _, done := out[id]; done || taken[p.old] {
			continue
		}
		out[id], taken[p.old] = prior.Sections[p.old].ID, true
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

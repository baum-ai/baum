package tiles

import (
	"encoding/json"
	"slices"
)

// The templates by change kind: the deterministic layouts a composer starts
// from, a draft shows before the composition is written, and the host falls
// back to when a composition is missing or unusable.
const (
	// TemplateFeature leads with the overview and risks, then the
	// composition's tiles, the changes, evidence and the rest. It is the
	// template of feature and bugfix pull requests, and of any change kind
	// without one of its own.
	TemplateFeature = "feature"
	// TemplateRefactor is the feature template with evidence and context
	// ahead of media, intent and decisions.
	TemplateRefactor = "refactor"
	// TemplateDependency leads with the overview and everything else, where
	// a dependency bump's mechanical change is, for dependency, upstream
	// sync and cherry-pick pull requests.
	TemplateDependency = "dependency"
	// TemplateDocs leads with a full-width overview and the changes, for
	// docs, ci, mechanical, translation, release and export pull requests.
	TemplateDocs = "docs"
)

// TemplateName returns the template of a change kind (the review's
// summary.change_kind).
func TemplateName(changeKind string) string {
	switch changeKind {
	case "refactor":
		return TemplateRefactor
	case "dependency", "upstream_sync", "cherry_pick":
		return TemplateDependency
	case "docs", "ci", "mechanical", "translation", "release", "export":
		return TemplateDocs
	}
	return TemplateFeature
}

// slot is a place in a template: a single tile at a size, or one of the
// runs below.
type slot struct {
	tile string
	size Size
}

const (
	// slotComposed places the composition's tiles: library tiles, then
	// promoted and authored tiles, at half width where their type allows.
	slotComposed = "*composed"
	// slotChanges places the changes group, in review order, a third each.
	slotChanges = "*changes"
	// slotRest places every tile not yet placed, in the converter's order
	// and sizes, then the composition's.
	slotRest = "*rest"
	// slotLead places the first change in review order outside the changes
	// group: a quiet review's lead (quietSlots).
	slotLead = "*lead"
)

var templates = map[string][]slot{
	TemplateFeature: {
		{TypeOverview, SizeHalf}, {TypeRisks, SizeHalf}, {slotComposed, SizeHalf}, {slotChanges, SizeThird},
		{TypeEvidence, SizeThird}, {TypeMedia, SizeThird}, {TypeIntent, SizeThird}, {TypeContext, SizeThird},
		{TypeDecisions, SizeThird}, {TypeEverythingElse, SizeThird}, {slotRest, ""},
	},
	TemplateRefactor: {
		{TypeOverview, SizeHalf}, {TypeRisks, SizeHalf}, {slotComposed, SizeHalf}, {slotChanges, SizeThird},
		{TypeEvidence, SizeThird}, {TypeContext, SizeThird}, {TypeEverythingElse, SizeThird}, {slotRest, ""},
	},
	TemplateDependency: {
		{TypeOverview, SizeHalf}, {TypeEverythingElse, SizeHalf}, {TypeRisks, SizeHalf}, {TypeEvidence, SizeThird},
		{slotComposed, SizeHalf}, {slotChanges, SizeThird}, {slotRest, ""},
	},
	TemplateDocs: {
		{TypeOverview, SizeFull}, {slotChanges, SizeThird}, {TypeEverythingElse, SizeThird}, {slotRest, ""},
	},
}

// Template lays out a converted document by the template of its change kind
// and returns the composition, normalised, with its generator's template
// named. content gives the composition's tiles (library, promoted and
// authored), normalised as NormalizeComposition does and reported; its
// layout is not used, and its generator and notes are kept. The changes
// group shows the converter's default number of changes; a single change
// stands alone at half, with no group. When the risks tile ranks no active
// risk above low, it gives its place on the first row to the lead change
// (quietSlots); otherwise the docs template sets the risks tile beside the
// overview at half, so the risks are on the landing page. When the evidence
// has EvidenceGapsShown gaps or more, the lead change and the evidence tile
// share the second row at half (gapSlots). A row the slots
// leave short is filled as RowFills says, as part of the template, not as a
// fix.
func Template(doc *Document, content *Composition) (Composition, Report) {
	if content == nil {
		content = &Composition{}
	}
	name := TemplateName(changeKindOf(doc))
	cm := newComposer(doc, modeWrite)
	cm.template = true
	if err := cm.prepare(content); err != nil {
		return Composition{Schema: CompositionName}, Report{Fixes: []Fix{{Kind: FixUnusable, Detail: err.Error()}}}
	}
	out := cm.finish(content, cm.templateLayout(templateSlots(doc)))
	generator := Generator{}
	if out.Generator != nil {
		generator = *out.Generator
	}
	generator.Template = name
	out.Generator = &generator
	return out, cm.report
}

// templateSlots returns the slots of a document's template.
func templateSlots(doc *Document) []slot {
	name := TemplateName(changeKindOf(doc))
	slots := templates[name]
	switch {
	case quietRisks(doc):
		slots = quietSlots(slots)
	case name == TemplateDocs:
		// A docs or CI pull request with an active risk above low keeps the
		// risks tile beside the overview, on the landing page.
		slots = append([]slot{{TypeOverview, SizeHalf}, {TypeRisks, SizeHalf}}, slots[1:]...)
	}
	if evidenceGaps(doc) >= EvidenceGapsShown {
		slots = gapSlots(slots)
	}
	return slots
}

// gapSlots adapts a template to a review whose evidence has
// EvidenceGapsShown gaps or more: the evidence tile takes half of the second
// row, beside the lead change, so the gaps are on the first screen.
func gapSlots(slots []slot) []slot {
	out := slices.Clone(slots)
	out = slices.DeleteFunc(out, func(s slot) bool { return s.tile == TypeEvidence })
	if at := slices.IndexFunc(out, func(s slot) bool { return s.tile == slotLead }); at >= 0 {
		return slices.Insert(out, at+1, slot{TypeEvidence, SizeHalf})
	}
	// After the first row: the overview and risks at half, or a full
	// overview.
	at := 1
	if len(out) > 1 && out[0].size == SizeHalf {
		at = 2
	}
	return slices.Insert(out, min(at, len(out)), slot{slotLead, SizeHalf}, slot{TypeEvidence, SizeHalf})
}

// templateOf returns the template's layout with this composer's own
// composition tiles, as Template lays it out; the template's fixes are not
// the composer's.
func (cm *composer) templateOf() CompositionLayout {
	t := &composer{doc: cm.doc, mode: cm.mode, byID: cm.byID, obsolete: cm.obsolete, library: cm.library, promoted: cm.promoted,
		authored: cm.authored, template: true}
	return t.finish(&Composition{}, t.templateLayout(templateSlots(cm.doc))).Layout
}

// changeKindOf returns the change kind the overview tile holds.
func changeKindOf(doc *Document) string {
	for _, tile := range doc.Tiles {
		if tile.Type == TypeOverview {
			var data OverviewData
			_ = json.Unmarshal(tile.Data, &data)
			return data.ChangeKind
		}
	}
	return ""
}

// quietRisks reports whether the risks tile ranks no active risk above low:
// its glance then says only that nothing needs a decision, which the strip's
// risk score already says.
func quietRisks(doc *Document) bool {
	ranked := map[string]bool{}
	for _, tile := range doc.Tiles {
		if tile.Type == TypeRisks {
			var data RisksData
			_ = json.Unmarshal(tile.Data, &data)
			for _, id := range data.Ranked {
				ranked[id] = true
			}
		}
	}
	for _, risk := range doc.Risks {
		if ranked[risk.ID] && risk.State == "active" && risk.Tier != "low" {
			return false
		}
	}
	return true
}

// quietSlots adapts a template to a review whose risks tile is quiet: the
// risks tile leaves the first rows and takes a third after the changes group
// (after evidence in the dependency template, whose everything else leads).
// In the feature and refactor templates the lead change, the first in review
// order, takes the half of the first row the risks tile held. The docs
// template, which places the risks tile with the rest, places it at a third
// before them.
func quietSlots(slots []slot) []slot {
	if len(slots) < 2 {
		return slots
	}
	if slots[0] != (slot{TypeOverview, SizeHalf}) {
		rest := slices.IndexFunc(slots, func(s slot) bool { return s.tile == slotRest })
		if rest < 0 || slices.ContainsFunc(slots, func(s slot) bool { return s.tile == TypeRisks }) {
			return slots
		}
		return slices.Insert(slices.Clone(slots), rest, slot{TypeRisks, SizeThird})
	}
	after, lead := slotChanges, true
	if slots[1].tile == TypeEverythingElse {
		after, lead = TypeEvidence, false
	}
	out := []slot{slots[0]}
	if lead {
		out = append(out, slot{slotLead, SizeHalf})
	}
	for _, s := range slots[1:] {
		if s.tile == TypeRisks {
			continue
		}
		out = append(out, s)
		if s.tile == after {
			out = append(out, slot{TypeRisks, SizeThird})
		}
	}
	return out
}

func (cm *composer) templateLayout(slots []slot) CompositionLayout {
	var composed []*Tile
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for i := range cands {
			composed = append(composed, &cands[i].tile)
		}
	}
	var entries []Entry
	placed := map[string]bool{}
	add := func(id string, size Size, group string) {
		tile := cm.byID[id]
		if tile == nil || cm.obsolete[id] || placed[id] {
			return
		}
		placed[id] = true
		entries = append(entries, Entry{Tile: id, Size: size, Group: group})
	}
	addComposed := func(tile *Tile, size Size) {
		if placed[tile.ID] {
			return
		}
		if allowed := AllowedSizes(*tile); !slices.Contains(allowed, size) {
			size = allowed[0]
		}
		placed[tile.ID] = true
		entries = append(entries, Entry{Tile: tile.ID, Size: size})
	}
	for _, s := range slots {
		switch s.tile {
		case slotLead:
			for _, entry := range cm.doc.Layout.Entries {
				if entry.Group == GroupChanges {
					add(entry.Tile, s.size, "")
					break
				}
			}
		case slotComposed:
			for _, tile := range composed {
				addComposed(tile, s.size)
			}
		case slotChanges:
			var members []Entry
			for _, entry := range cm.doc.Layout.Entries {
				if entry.Group == GroupChanges && !placed[entry.Tile] {
					members = append(members, entry)
				}
			}
			if len(members) == 1 {
				// One change needs no group: it stands alone at half, beside
				// the tile that follows.
				add(members[0].Tile, SizeHalf, "")
				continue
			}
			for _, entry := range members {
				add(entry.Tile, s.size, GroupChanges)
			}
		case slotRest:
			for _, entry := range cm.doc.Layout.Entries {
				add(entry.Tile, entry.Size, entry.Group)
			}
			for _, tile := range composed {
				addComposed(tile, SizeHalf)
			}
		default:
			add(s.tile, s.size, "")
		}
	}
	return CompositionLayout{Entries: entries}
}

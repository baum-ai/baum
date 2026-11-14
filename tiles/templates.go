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
// group shows the converter's default number of changes.
func Template(doc *Document, content *Composition) (Composition, Report) {
	if content == nil {
		content = &Composition{}
	}
	name := TemplateName(changeKindOf(doc))
	cm := newComposer(doc, modeWrite)
	if err := cm.prepare(content); err != nil {
		return Composition{Schema: CompositionName}, Report{Fixes: []Fix{{Kind: FixUnusable, Detail: err.Error()}}}
	}
	out := cm.finish(content, cm.templateLayout(name))
	generator := Generator{}
	if out.Generator != nil {
		generator = *out.Generator
	}
	generator.Template = name
	out.Generator = &generator
	return out, cm.report
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

func (cm *composer) templateLayout(name string) CompositionLayout {
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
	for _, s := range templates[name] {
		switch s.tile {
		case slotComposed:
			for _, tile := range composed {
				addComposed(tile, s.size)
			}
		case slotChanges:
			for _, entry := range cm.doc.Layout.Entries {
				if entry.Group == GroupChanges {
					add(entry.Tile, s.size, GroupChanges)
				}
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

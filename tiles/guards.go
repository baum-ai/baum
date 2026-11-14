package tiles

import (
	"slices"
)

// The guard rails keep a composer's layout from making the page worse than
// the change kind's template (Template) of the same review. They run on
// every composition that is not a template, after its layout is normalised
// and before its rows are filled, each counted as a fix of its own kind:
//
//  1. Evidence with EvidenceGapsShown or more missing or failed items of
//     current changes is on the landing page (FixEvidenceGaps): when the
//     layout puts the evidence tile past it, the tile moves up to the
//     changes group's start, or as far up as it takes to be shown, at its
//     size or, when that does not fit, at half or a third. Pinned
//     tiles and those always on the landing page come first, so when they
//     fill it nothing moves; a person who hid the tile keeps it hidden.
//  2. A change featured alone on its row at half, empty units beside it, is
//     widened to the full row (FixLoneFeatured).
//  3. The first screen, the first FirstScreenRows rows (what a 1440×900
//     window shows unscrolled), shows at least as many current changes as
//     the template's does, and so does the whole landing page, unless it shows instead something the template's
//     does not: the risks tile while it ranks an active critical or high
//     risk, a change the recommendation names, the evidence tile with gaps
//     as in 1, or a tile a person pinned; and a layout in which a person hid
//     tiles is theirs. Otherwise the composition takes the
//     template's layout (FixFirstScreen). A change that carries a breaking
//     change is not known to the document (the overview names the break,
//     not its change), so it cannot stand in.
const (
	// FirstScreenRows is how many rows of the canonical grid the first
	// screen of a wide window shows under the review's band.
	FirstScreenRows = 2
	// EvidenceGapsShown is the evidence gaps that keep the evidence tile on
	// the landing page.
	EvidenceGapsShown = 3
)

// The guard rails' fixes.
const (
	FixEvidenceGaps FixKind = "evidence_gaps"
	FixLoneFeatured FixKind = "lone_featured"
	FixFirstScreen  FixKind = "first_screen"
)

// guard applies the guard rails to a normalised composition (not a
// template's).
func (cm *composer) guard(c *Composition) {
	if cm.template {
		return
	}
	cm.guardEvidence(c)
	cm.guardLoneFeatured(c)
	cm.guardFirstScreen(c)
}

// trialDoc builds the document a composition gives, or nil.
func (cm *composer) trialDoc(c *Composition) *Document {
	trial := *c
	trial.Layout.Entries = slices.Clone(c.Layout.Entries)
	doc, err := cm.build(&trial)
	if err != nil {
		return nil
	}
	return doc
}

// evidenceGaps counts the missing and failed evidence of current changes.
func evidenceGaps(doc *Document) int {
	current := map[string]bool{}
	for _, change := range doc.Changes {
		current[change.ID] = currentChange(change)
	}
	n := 0
	for _, item := range doc.Evidence {
		if current[item.Change] && (item.Status == "missing" || item.Status == "failed") {
			n++
		}
	}
	return n
}

// shown reports whether a document's landing page shows a tile.
func shown(doc *Document, id string) bool {
	i := slices.IndexFunc(doc.Layout.Entries, func(e Entry) bool { return e.Tile == id })
	return i >= 0 && Visible(doc.Layout)[i]
}

func (cm *composer) guardEvidence(c *Composition) {
	if cm.byID[TypeEvidence] == nil || evidenceGaps(cm.doc) < EvidenceGapsShown {
		return
	}
	doc := cm.trialDoc(c)
	if doc == nil || shown(doc, TypeEvidence) {
		return
	}
	entries := c.Layout.Entries
	at := slices.IndexFunc(entries, func(e Entry) bool { return e.Tile == TypeEvidence })
	if at < 0 {
		return
	}
	evidence := entries[at]
	rest := slices.Delete(slices.Clone(entries), at, at+1)
	start := slices.IndexFunc(rest, func(e Entry) bool { return e.Group == GroupChanges })
	if start < 0 {
		start = len(rest)
	}
	// At its own size first, then smaller, so it fits beside what leads.
	sizes := []Size{evidence.Size}
	for _, size := range []Size{SizeHalf, SizeThird} {
		if size.Units() < evidence.Size.Units() {
			sizes = append(sizes, size)
		}
	}
	for _, size := range sizes {
		moved := evidence
		moved.Size = size
		for to := start; to >= 0; to-- {
			trial := *c
			trial.Layout.Entries = slices.Insert(slices.Clone(rest), to, moved)
			if doc := cm.trialDoc(&trial); doc != nil && shown(doc, TypeEvidence) {
				c.Layout.Entries = trial.Layout.Entries
				cm.fix(FixEvidenceGaps, TypeEvidence, "")
				return
			}
		}
	}
}

func (cm *composer) guardLoneFeatured(c *Composition) {
	doc := cm.trialDoc(c)
	if doc == nil {
		return
	}
	rows := Rows(doc.Layout)
	for k, row := range rows {
		if len(row.Entries) != 1 || row.Group != "" || k == len(rows)-1 {
			continue
		}
		entry := doc.Layout.Entries[row.Entries[0]]
		tile := cm.byID[entry.Tile]
		if tile == nil || tile.Type != TypeChange || entry.Size != SizeHalf || entry.Pin {
			continue
		}
		for i := range c.Layout.Entries {
			if c.Layout.Entries[i].Tile == entry.Tile {
				c.Layout.Entries[i].Size = SizeFull
				cm.fix(FixLoneFeatured, entry.Tile, "")
			}
		}
	}
}

// firstScreen sums what a document's first screen shows: its current
// changes, and the tiles that may stand in for changes.
type firstScreen struct {
	changes int
	// landing counts the current changes on the whole landing page.
	landing int
	others  map[string]bool
}

func firstScreenOf(doc *Document) firstScreen {
	current := map[string]bool{}
	for _, change := range doc.Changes {
		current[change.ID] = currentChange(change)
	}
	named := RecommendedChanges(doc)
	out := firstScreen{others: map[string]bool{}}
	visible := Visible(doc.Layout)
	for i, entry := range doc.Layout.Entries {
		if visible[i] && current[entry.Tile] {
			out.landing++
		}
	}
	for k, row := range Rows(doc.Layout) {
		if k >= FirstScreenRows || row.Entries[0] >= doc.Layout.Landing {
			break
		}
		for _, i := range row.Entries {
			entry := doc.Layout.Entries[i]
			if current[entry.Tile] {
				out.changes++
			}
			switch {
			case entry.Pin,
				entry.Tile == TypeRisks && mustShowRisks(doc),
				slices.Contains(named, entry.Tile),
				entry.Tile == TypeEvidence && evidenceGaps(doc) >= EvidenceGapsShown:
				out.others[entry.Tile] = true
			}
		}
	}
	return out
}

func (cm *composer) guardFirstScreen(c *Composition) {
	if len(c.Layout.Hidden) > 0 {
		// A person hid tiles: what the first screen shows is theirs.
		return
	}
	template := cm.templateOf()
	if slices.Equal(template.Entries, c.Layout.Entries) {
		return
	}
	composed := cm.trialDoc(c)
	templated := cm.trialDoc(&Composition{Schema: CompositionName, Library: c.Library, Promotions: c.Promotions, Authored: c.Authored, Layout: template})
	if composed == nil || templated == nil {
		return
	}
	got, want := firstScreenOf(composed), firstScreenOf(templated)
	if got.changes >= want.changes && got.landing >= want.landing {
		return
	}
	for id := range got.others {
		if !want.others[id] {
			return
		}
	}
	c.Layout.Entries = slices.Clone(template.Entries)
	c.Layout.ChangesShown = template.ChangesShown
	cm.fix(FixFirstScreen, "", "")
}

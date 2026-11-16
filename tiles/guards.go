package tiles

import (
	"slices"
)

// The guard rails keep a composer's layout from making the page worse than
// the change kind's template (Template) of the same review. They run on
// every composition that is not a template, after its layout is normalised
// and before its rows are filled, each counted as a fix of its own kind:
//
//  0. A library tile that does not lead (Leads) and an authored tile do not
//     push a core tile off the first screen (FixExtraPlace): the overview,
//     the risks and evidence tiles, a current change with an active
//     critical, high or medium risk, a change the recommendation names, a
//     library tile that leads and a pinned tile. When the first screen
//     shows fewer of them than it would with those tiles moved to the end,
//     the first of them on the first screen moves: beside the change it
//     supports (SupportedChange) at half when that change stands outside
//     the group and the two still leave the core tiles in view (the change
//     taking half is recorded as a fix of its own), else just after the
//     changes group when that change is a member, else just after the later
//     of the risks and evidence tiles, else to the first
//     place further down that shows the core tiles. It takes its default
//     size (LibrarySize, AuthoredSize) when the composer gave it a larger
//     one. A pinned tile stays; a layout in which a person hid tiles is
//     theirs. Templates hold this rule too, as part of the template.
//     Then such a tile that the layout gives the full row while it lists
//     three items or fewer (its LibrarySize or AuthoredSize is smaller)
//     takes that size when it then shares its row with the tile after it
//     (FixExtraSize); alone on its row it would only grow back.
//  1. Evidence with EvidenceGapsShown or more missing or failed items of
//     current changes is on the landing page (FixEvidenceGaps): when the
//     layout puts the evidence tile past it, the tile moves up to the
//     changes group's start, or as far up as it takes to be shown, at its
//     size or, when that does not fit, at half or a third, and never where
//     it would push a tile that must be seen (3) off the first screen. When
//     no such place shows it, it swaps places with a tile the landing page
//     shows that need not be seen, the last first. Pinned tiles and those
//     always on the landing page come first, so when they fill it nothing
//     moves; a person who hid the tile keeps it hidden.
//  2. A change featured alone on its row at half, empty units beside it, is
//     widened to the full row (FixLoneFeatured).
//  3. The first screen, the first FirstScreenRows rows (what a 1440×900
//     window shows unscrolled), shows at least as many current changes as
//     the template's does, and so does the whole landing page, unless it
//     shows instead something the template's does not: the risks tile while
//     it ranks an active critical or high risk, a change the recommendation
//     names, the evidence tile with gaps as in 1, or a library tile with a
//     destructive or breaking item (Leads). And what of those the
//     template's first screen shows stays on it. A tile a person pinned on
//     the first screen makes the arrangement theirs: nothing is checked.
//     Otherwise the composition takes the template's layout
//     (FixFirstScreen); a layout in which a person hid tiles is theirs. A
//     change that carries a breaking change is not known to the document
//     (the overview names the break, not its change), so it cannot stand
//     in.
const (
	// FirstScreenRows is how many rows of the canonical grid the first
	// screen of a wide window shows under the review's band.
	FirstScreenRows = 2
	// EvidenceGapsShown is the evidence gaps that keep the evidence tile on
	// the landing page.
	EvidenceGapsShown = 2
)

// The guard rails' fixes.
const (
	FixExtraPlace   FixKind = "extra_place"
	FixExtraSize    FixKind = "extra_size"
	FixEvidenceGaps FixKind = "evidence_gaps"
	FixLoneFeatured FixKind = "lone_featured"
	FixFirstScreen  FixKind = "first_screen"
)

// guard applies the guard rails to a normalised composition (not a
// template's). A layout a person edited (ApplyOverlay) is theirs: the rails
// leave it as it is, while Arrange still keeps what must be seen on the
// landing page.
func (cm *composer) guard(c *Composition) {
	if c.Layout.Edited {
		return
	}
	cm.guardExtras(c)
	cm.guardExtraSizes(c)
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
		// A layout that leaves the evidence out has it placed after
		// everything it lists: move it from there.
		i := slices.IndexFunc(doc.Layout.Entries, func(e Entry) bool { return e.Tile == TypeEvidence })
		if i < 0 {
			return
		}
		at = len(entries)
		entries = append(slices.Clone(entries), Entry{Tile: TypeEvidence, Size: doc.Layout.Entries[i].Size})
	}
	evidence := entries[at]
	rest := slices.Delete(slices.Clone(entries), at, at+1)
	start := slices.IndexFunc(rest, func(e Entry) bool { return e.Group == GroupChanges })
	if start < 0 {
		start = len(rest)
	}
	// What must be seen on the first screen now stays there.
	before := firstScreenOf(doc).others
	keeps := func(moved *Document) bool {
		after := firstScreenOf(moved).others
		for id := range before {
			if id != TypeEvidence && !after[id] {
				return false
			}
		}
		return true
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
			if doc := cm.trialDoc(&trial); doc != nil && shown(doc, TypeEvidence) && keeps(doc) {
				c.Layout.Entries = trial.Layout.Entries
				cm.fix(FixEvidenceGaps, TypeEvidence, "")
				return
			}
		}
	}
	// No place before the changes shows it when the landing page's own rows
	// hold only pinned tiles and those always on it, and the room left is in
	// the rows that library and authored tiles add (ExtraRows): a tile moved
	// in front of those pushes them, and the rows they add, off. The
	// evidence then takes the place of a tile the landing page shows that
	// need not be seen, the last first, and that tile takes the evidence's.
	// A tile that is not a current change (context, decisions, an extra)
	// gives way before a change does.
	must := mustSeeOf(doc)
	visible := Visible(doc.Layout)
	current := currentChanges(doc)
	for _, changes := range []bool{false, true} {
		for i := len(doc.Layout.Entries) - 1; i >= 0; i-- {
			other := doc.Layout.Entries[i]
			if !visible[i] || current[other.Tile] != changes || other.Tile == TypeOverview || other.Tile == TypeEvidence || must.stands(other) {
				continue
			}
			to := slices.IndexFunc(entries, func(e Entry) bool { return e.Tile == other.Tile })
			if to < 0 {
				continue
			}
			for _, size := range sizes {
				moved := evidence
				moved.Size = size
				trial := *c
				trial.Layout.Entries = slices.Clone(entries)
				trial.Layout.Entries[to], trial.Layout.Entries[at] = moved, entries[to]
				if doc := cm.trialDoc(&trial); doc != nil && shown(doc, TypeEvidence) && keeps(doc) {
					c.Layout.Entries = trial.Layout.Entries
					cm.fix(FixEvidenceGaps, TypeEvidence, "")
					return
				}
			}
		}
	}
}

// currentChanges names a document's current changes.
func currentChanges(doc *Document) map[string]bool {
	current := map[string]bool{}
	for _, change := range doc.Changes {
		if currentChange(change) {
			current[change.ID] = true
		}
	}
	return current
}

// coreTiles names the tiles a reviewer needs to decide, which a library or
// authored tile that does not lead never pushes off the first screen: the
// overview, the risks and evidence tiles, the current changes with an active
// critical, high or medium risk, the changes the recommendation names, the
// library tiles that lead and the pinned tiles.
func coreTiles(doc *Document) map[string]bool {
	core := map[string]bool{TypeOverview: true, TypeRisks: true, TypeEvidence: true}
	current := currentChanges(doc)
	for _, risk := range doc.Risks {
		if current[risk.Change] && risk.State == "active" && (risk.Tier == "critical" || risk.Tier == "high" || risk.Tier == "medium") {
			core[risk.Change] = true
		}
	}
	// What must be seen (the hard rules and the guard rails' stand-ins:
	// named changes, leading library tiles, pins) is core too.
	must := mustSeeOf(doc)
	for _, entry := range doc.Layout.Entries {
		if must.kept(entry.Tile) || must.stands(entry) {
			core[entry.Tile] = true
		}
	}
	return core
}

// firstScreenTiles names the tiles a document's first screen shows.
func firstScreenTiles(doc *Document) map[string]bool {
	out := map[string]bool{}
	for k, row := range Rows(doc.Layout) {
		if k >= FirstScreenRows || row.Entries[0] >= doc.Layout.Landing {
			break
		}
		for _, i := range row.Entries {
			out[doc.Layout.Entries[i].Tile] = true
		}
	}
	return out
}

// coreShown names the core tiles (coreTiles) a document's first screen
// shows.
func coreShown(doc *Document) map[string]bool {
	core, out := coreTiles(doc), map[string]bool{}
	for id := range firstScreenTiles(doc) {
		if core[id] {
			out[id] = true
		}
	}
	return out
}

func covers(got, want map[string]bool) bool {
	for id := range want {
		if !got[id] {
			return false
		}
	}
	return true
}

// guardExtras keeps the library tiles that do not lead and the authored
// tiles from pushing a core tile off the first screen (rule 0 above).
func (cm *composer) guardExtras(c *Composition) {
	if len(c.Layout.Hidden) > 0 {
		return
	}
	movable := map[string]Size{}
	for _, cand := range cm.library {
		if cand.library.Pending || !Leads(cand.library.Data) {
			movable[cand.tile.ID] = LibrarySize(cand.library)
		}
	}
	for _, cand := range cm.authored {
		movable[cand.tile.ID] = AuthoredSize(cand.authored)
	}
	if len(movable) == 0 {
		return
	}
	for range len(movable) {
		doc := cm.trialDoc(c)
		if doc == nil {
			return
		}
		// What the first screen would show with these tiles at the end.
		without := *c
		without.Layout.Entries = nil
		var moved []Entry
		for _, entry := range c.Layout.Entries {
			if _, ok := movable[entry.Tile]; ok && !entry.Pin {
				moved = append(moved, entry)
				continue
			}
			without.Layout.Entries = append(without.Layout.Entries, entry)
		}
		without.Layout.Entries = append(without.Layout.Entries, moved...)
		base := cm.trialDoc(&without)
		if base == nil {
			return
		}
		target := coreShown(base)
		if covers(coreShown(doc), target) {
			return
		}
		first := firstScreenTiles(doc)
		at := slices.IndexFunc(c.Layout.Entries, func(e Entry) bool {
			_, ok := movable[e.Tile]
			return ok && !e.Pin && first[e.Tile]
		})
		if at < 0 || !cm.moveExtra(c, at, movable[c.Layout.Entries[at].Tile], target) {
			return
		}
	}
}

// guardExtraSizes gives the library tiles that do not lead and the authored
// tiles that the layout sets at full, while their content takes less, the
// size their content takes, all at once, and keeps it for each that then
// shares its row: alone on its row, it would grow back to fill it.
func (cm *composer) guardExtraSizes(c *Composition) {
	if len(c.Layout.Hidden) > 0 {
		return
	}
	want := map[string]Size{}
	for _, cand := range cm.library {
		if cand.library.Pending || !Leads(cand.library.Data) {
			want[cand.tile.ID] = LibrarySize(cand.library)
		}
	}
	for _, cand := range cm.authored {
		want[cand.tile.ID] = AuthoredSize(cand.authored)
	}
	trial := *c
	trial.Layout.Entries = slices.Clone(c.Layout.Entries)
	shrunk := map[string]bool{}
	for i, entry := range trial.Layout.Entries {
		if size, ok := want[entry.Tile]; ok && !entry.Pin && entry.Size == SizeFull && size != SizeFull {
			trial.Layout.Entries[i].Size = size
			shrunk[entry.Tile] = true
		}
	}
	if len(shrunk) == 0 {
		return
	}
	doc := cm.trialDoc(&trial)
	if doc == nil {
		return
	}
	shares := map[string]bool{}
	for _, row := range Rows(doc.Layout) {
		for _, i := range row.Entries {
			shares[doc.Layout.Entries[i].Tile] = len(row.Entries) > 1
		}
	}
	for i, entry := range c.Layout.Entries {
		if shrunk[entry.Tile] && shares[entry.Tile] {
			c.Layout.Entries[i].Size = want[entry.Tile]
			if !cm.template {
				cm.fix(FixExtraSize, entry.Tile, "")
			}
		}
	}
}

// moveExtra moves the extra tile at an index of the layout to the first
// place, in guardExtras' order of preference, where it is off the first
// screen or the first screen shows the target's core tiles, at its default
// size when the layout gave it a larger one. It reports whether it moved.
func (cm *composer) moveExtra(c *Composition, at int, size Size, target map[string]bool) bool {
	entry := c.Layout.Entries[at]
	if size.Units() < entry.Size.Units() {
		entry.Size = size
	}
	rest := slices.Delete(slices.Clone(c.Layout.Entries), at, at+1)
	// resized names the change accept records as taking half beside the
	// tile, when it did.
	resized := ""
	accept := func(entries []Entry) bool {
		trial := *c
		trial.Layout.Entries = entries
		doc := cm.trialDoc(&trial)
		if doc == nil || !covers(coreShown(doc), target) && firstScreenTiles(doc)[entry.Tile] {
			return false
		}
		c.Layout.Entries = entries
		if !cm.template {
			cm.fix(FixExtraPlace, entry.Tile, "")
			if resized != "" {
				cm.fix(FixExtraPlace, resized, "it takes half beside a tile that supports it")
			}
		}
		return true
	}
	index := func(id string) int { return slices.IndexFunc(rest, func(e Entry) bool { return e.Tile == id }) }
	change := cm.supportedChange(entry.Tile)
	k := index(change)
	// Beside the change it supports, the two at half.
	if change != "" && k >= 0 && rest[k].Group == "" && !rest[k].Pin && cm.byID[change] != nil &&
		slices.Contains(SizesIn(cm.doc, *cm.byID[change]), SizeHalf) && slices.Contains(cm.sizesOf(entry.Tile), SizeHalf) {
		beside := slices.Clone(rest)
		if beside[k].Size != SizeHalf {
			beside[k].Size, resized = SizeHalf, change
		}
		half := entry
		half.Size = SizeHalf
		if accept(slices.Insert(beside, k+1, half)) {
			return true
		}
		resized = ""
	}
	// Just after the changes group, when the change it supports is a member
	// (a tile cannot join the group).
	if k >= 0 && rest[k].Group != "" {
		if last := lastIndex(rest, rest[k].Group); accept(slices.Insert(slices.Clone(rest), last+1, entry)) {
			return true
		}
	}
	// After the later of the risks and evidence tiles.
	if k := max(index(TypeRisks), index(TypeEvidence)); k >= 0 && accept(slices.Insert(slices.Clone(rest), k+1, entry)) {
		return true
	}
	// Further down, never inside the changes group.
	for to := at; to <= len(rest); to++ {
		if to > 0 && to < len(rest) && rest[to].Group != "" && rest[to].Group == rest[to-1].Group {
			continue
		}
		if accept(slices.Insert(slices.Clone(rest), to, entry)) {
			return true
		}
	}
	return false
}

// supportedChange is the change an extra tile supports (SupportedChange).
func (cm *composer) supportedChange(id string) string {
	for _, cand := range cm.library {
		if cand.tile.ID == id {
			return SupportedChange(cm.doc, cand.library.Data)
		}
	}
	for _, cand := range cm.authored {
		if cand.tile.ID == id {
			return SupportedChange(cm.doc, cand.authored.Data)
		}
	}
	return ""
}

// sizesOf returns the sizes a composition tile allows.
func (cm *composer) sizesOf(id string) []Size {
	for _, cands := range [][]candidate{cm.library, cm.authored} {
		for i := range cands {
			if cands[i].tile.ID == id {
				return SizesIn(cm.doc, cands[i].tile)
			}
		}
	}
	return nil
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

// mustBeSeen is what a document's page must show, in two strengths. The
// hard rules (kept) hold in every layout, a person's edited one included: the
// overview, the risks tile while it ranks an active critical or high risk and
// a change the recommendation names are never hidden and stay on the landing
// page (build, ApplyOverlay, CheckOp). The guard rails (stands) add the tiles
// that may stand in for changes on the first screen and must not be pushed
// off it: a pinned tile, the evidence with EvidenceGapsShown gaps and a
// library tile with a destructive or breaking item (leads).
type mustBeSeen struct {
	severe bool
	gaps   bool
	named  []string
	leads  map[string]bool
}

func mustSeeOf(doc *Document) mustBeSeen {
	return mustBeSeen{
		severe: mustShowRisks(doc),
		gaps:   evidenceGaps(doc) >= EvidenceGapsShown,
		named:  RecommendedChanges(doc),
		leads:  leadingLibrary(doc),
	}
}

// kept reports whether a tile is held by the hard rules.
func (m mustBeSeen) kept(id string) bool {
	return id == TypeOverview || id == TypeRisks && m.severe || slices.Contains(m.named, id)
}

// stands reports whether an entry may stand in for changes on the first
// screen: every tile the hard rules keep but the overview, which every
// layout shows first, and those the guard rails add.
func (m mustBeSeen) stands(entry Entry) bool {
	return entry.Tile != TypeOverview && m.kept(entry.Tile) ||
		entry.Pin ||
		entry.Tile == TypeEvidence && m.gaps ||
		m.leads[entry.Tile]
}

// leadingLibrary names a document's custom tiles with a destructive or
// breaking item (Leads): a library tile that loses data or breaks callers is
// a tile that must be seen, as a severe risk is. Other library tiles and
// authored tiles are not: a layout that takes a first-screen place from a
// change for one is held to the template's. It is a guard rail, not a hard
// rule: a person may hide such a tile or move it down.
func leadingLibrary(doc *Document) map[string]bool {
	out := map[string]bool{}
	for _, tile := range doc.Tiles {
		if tile.Type == TypeCustom && tile.State == StateReady && Leads(tile.Data) {
			out[tile.ID] = true
		}
	}
	return out
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
	must := mustSeeOf(doc)
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
			if must.stands(entry) {
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
	// A pinned tile on the first screen makes the arrangement the person's.
	if slices.ContainsFunc(c.Layout.Entries, func(e Entry) bool { return e.Pin && got.others[e.Tile] }) {
		return
	}
	lost := false
	for id := range want.others {
		lost = lost || !got.others[id]
	}
	if got.changes >= want.changes && got.landing >= want.landing && !lost {
		return
	}
	for id := range got.others {
		if !want.others[id] && !lost {
			return
		}
	}
	c.Layout.Entries = slices.Clone(template.Entries)
	c.Layout.ChangesShown = template.ChangesShown
	cm.fix(FixFirstScreen, "", "")
}

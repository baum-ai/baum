// Package tilestest checks conversions between baum.review/1 and
// baum.tiles/1 documents and the documents compositions generate, for tests
// and for runs over real review stores, and builds custom tiles and random
// compositions for tests. It uses only the tiles package's public API.
package tilestest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/baum-ai/baum/tiles"
)

// review is the part of a baum.review/1 document the checks read.
type review struct {
	Summary struct {
		Recommendation json.RawMessage `json:"recommendation"`
		RiskScore      json.RawMessage `json:"risk_score"`
	} `json:"summary"`
	Sections []struct {
		ID       string            `json:"id"`
		Kind     string            `json:"kind"`
		Hunks    []string          `json:"hunks"`
		Risks    []item            `json:"risks"`
		Evidence []item            `json:"evidence"`
		Blocks   []json.RawMessage `json:"blocks"`
		Derived  struct {
			State string `json:"state"`
		} `json:"derived"`
	} `json:"sections"`
	UnassignedHunks []string `json:"unassigned_hunks"`
	Index           struct {
		Hunks []item `json:"hunks"`
	} `json:"index"`
}

type item struct {
	ID string `json:"id"`
}

// block is the identity of a block: its kind and, for a decisions or images
// block, its items (decision IDs as the review wrote them, attachment IDs).
type block struct {
	Kind  string   `json:"kind"`
	Items []string `json:"items,omitempty"`
}

func identity(raw json.RawMessage) block {
	var value struct {
		Kind  string `json:"kind"`
		Items []struct {
			ID         string `json:"id"`
			Attachment string `json:"attachment"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &value)
	b := block{Kind: value.Kind}
	switch value.Kind {
	case "decisions":
		for _, i := range value.Items {
			b.Items = append(b.Items, i.ID)
		}
	case "images":
		for _, i := range value.Items {
			b.Items = append(b.Items, i.Attachment)
		}
	}
	return b
}

var validator = tiles.NewValidator()

// CheckConversion checks that a tile document converted from a normalized
// baum.review/1 document (review.NormalizeStored) presents all of it, and
// returns every failure, each prefixed with the number of the invariant it
// breaks:
//
//  1. The tiles' covers together are the current hunks (those of current
//     sections plus the unassigned ones), which are the index's hunks.
//  2. Each section's risks, active and mitigated, are in risks exactly once;
//     the risks tile ranks exactly the active risks of current changes; and a
//     change's derived review reason resolves (Document.RiskIn).
//  3. Each section's evidence items are in evidence exactly once.
//  4. Each block of each section is in exactly one tile, at its place: the
//     blocks a section's tile keeps and the blocks and items the aggregate
//     tiles hold for it, by origin, give back the section's blocks in order,
//     kind by kind, with the same decisions and images.
//  5. The recommendation and the risk score are JSON-equal to the review's,
//     present exactly when the review's are, and every risk a reason of the
//     risk score names resolves (Document.RiskIn).
//  6. Every active critical or high risk of a current change is ranked, and
//     the risks tile is on the landing page.
//  7. Every tile is placed exactly once, landing is what Landing derives,
//     and the overview and risks tiles are on the landing page.
//  8. Every tile passes the tile-level checks; none is unavailable.
//  9. ToReview of the tile document is JSON-equal to the review without the
//     retired per-section and per-risk tiles.
//
// The tenth invariant compares consecutive versions (CheckStability).
func CheckConversion(normalized []byte, doc *tiles.Document) []string {
	return check(normalized, doc, false)
}

// check checks the invariants CheckConversion lists, as CheckGenerated
// relaxes them for a generated document.
func check(normalized []byte, doc *tiles.Document, generated bool) []string {
	var failures []string
	fail := func(invariant int, format string, args ...any) {
		failures = append(failures, fmt.Sprintf("invariant %d: ", invariant)+fmt.Sprintf(format, args...))
	}
	var source review
	if err := json.Unmarshal(normalized, &source); err != nil {
		return []string{"review: " + err.Error()}
	}
	byID := map[string]*tiles.Tile{}
	for i := range doc.Tiles {
		byID[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	current := map[string]bool{}
	for _, s := range source.Sections {
		current[s.ID] = s.Derived.State != "stale"
	}

	// 1. Coverage.
	covered, want, indexed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, tile := range doc.Tiles {
		for _, hunk := range tile.Covers {
			covered[hunk] = true
		}
	}
	for _, s := range source.Sections {
		if current[s.ID] {
			for _, hunk := range s.Hunks {
				want[hunk] = true
			}
		}
	}
	for _, hunk := range source.UnassignedHunks {
		want[hunk] = true
	}
	for _, hunk := range source.Index.Hunks {
		indexed[hunk.ID] = true
	}
	if !reflect.DeepEqual(covered, want) {
		fail(1, "the tiles cover %d hunks, the current sections and unassigned hunks are %d", len(covered), len(want))
	}
	if !reflect.DeepEqual(want, indexed) {
		fail(1, "the current sections and unassigned hunks are %d hunks, the index lists %d", len(want), len(indexed))
	}

	// 2 and 3. Every risk and evidence item once, by section.
	risks := 0
	for _, s := range source.Sections {
		var wantRisks, gotRisks, wantEvidence, gotEvidence []string
		for _, r := range s.Risks {
			wantRisks = append(wantRisks, r.ID)
		}
		for _, e := range s.Evidence {
			wantEvidence = append(wantEvidence, e.ID)
		}
		for _, risk := range doc.Risks {
			if risk.Change == s.ID {
				gotRisks = append(gotRisks, original(risk.ID, risk.Origin))
			}
		}
		for _, e := range doc.Evidence {
			if e.Change == s.ID {
				gotEvidence = append(gotEvidence, original(e.ID, e.Origin))
			}
		}
		risks += len(s.Risks)
		if !slices.Equal(wantRisks, gotRisks) {
			fail(2, "section %s has %d risks, the tile document %d for it", s.ID, len(wantRisks), len(gotRisks))
		}
		if !slices.Equal(wantEvidence, gotEvidence) {
			fail(3, "section %s has %d evidence items, the tile document %d for it", s.ID, len(wantEvidence), len(gotEvidence))
		}
	}
	if len(doc.Risks) != risks {
		fail(2, "the review has %d risks, the tile document %d", risks, len(doc.Risks))
	}
	for _, change := range doc.Changes {
		var derived struct {
			ReviewReason *struct {
				Risk *string `json:"risk"`
			} `json:"review_reason"`
		}
		_ = json.Unmarshal(change.Derived, &derived)
		if derived.ReviewReason != nil && derived.ReviewReason.Risk != nil && doc.RiskIn(change.ID, *derived.ReviewReason.Risk) == nil {
			fail(2, "the review reason of change %s names a risk that does not resolve", change.ID)
		}
	}
	var ranked tiles.RisksData
	if tile := byID[tiles.TypeRisks]; tile == nil {
		fail(2, "there is no risks tile")
	} else if err := json.Unmarshal(tile.Data, &ranked); err != nil {
		fail(2, "risks data: %v", err)
	}
	var active []string
	for _, risk := range doc.Risks {
		if risk.State == "active" && current[risk.Change] {
			active = append(active, risk.ID)
		}
	}
	if !slices.Equal(sorted(active), sorted(ranked.Ranked)) {
		fail(2, "the risks tile ranks %d risks, the active risks of current changes are %d", len(ranked.Ranked), len(active))
	}

	// 4. Blocks, by identity and place.
	for _, failure := range blockFailures(source, doc) {
		fail(4, "%s", failure)
	}

	// 5. Recommendation and risk score.
	if !jsonEqual(source.Summary.Recommendation, doc.Recommendation) {
		fail(5, "the recommendation differs from the review's")
	}
	if !jsonEqual(source.Summary.RiskScore, doc.RiskScore) {
		fail(5, "the risk score differs from the review's")
	}
	var score struct {
		Reasons []struct {
			Section string  `json:"section"`
			Risk    *string `json:"risk"`
		} `json:"reasons"`
	}
	if len(doc.RiskScore) > 0 {
		_ = json.Unmarshal(doc.RiskScore, &score)
	}
	for _, reason := range score.Reasons {
		if reason.Risk != nil && doc.RiskIn(reason.Section, *reason.Risk) == nil {
			fail(5, "a risk score reason of change %s names a risk that does not resolve", reason.Section)
		}
	}

	// 6 and 7. Landing page and placement.
	position := map[string]int{}
	for i, entry := range doc.Layout.Entries {
		if _, ok := position[entry.Tile]; ok {
			fail(7, "tile %s is placed more than once", entry.Tile)
		}
		position[entry.Tile] = i
	}
	for _, tile := range doc.Tiles {
		if _, ok := position[tile.ID]; !ok {
			fail(7, "tile %s is not placed", tile.ID)
		}
	}
	if len(doc.Layout.Entries) != len(doc.Tiles) {
		fail(7, "the layout has %d entries for %d tiles", len(doc.Layout.Entries), len(doc.Tiles))
	}
	if landing := tiles.Landing(doc.Layout); landing != doc.Layout.Landing {
		fail(7, "landing is %d, the layout packs %d", doc.Layout.Landing, landing)
	}
	visible := tiles.Visible(doc.Layout)
	severe := false
	for _, risk := range doc.Risks {
		if risk.State == "active" && current[risk.Change] && (risk.Tier == "critical" || risk.Tier == "high") {
			severe = true
			if !slices.Contains(ranked.Ranked, risk.ID) {
				fail(6, "%s risk %s is not ranked", risk.Tier, risk.ID)
			}
		}
	}
	landing := map[string]int{tiles.TypeOverview: 7, tiles.TypeRisks: 6}
	if generated {
		// A generated layout may put the risks tile anywhere while it ranks
		// no critical or high risk.
		landing = map[string]int{tiles.TypeOverview: 6}
		if severe {
			landing[tiles.TypeRisks] = 6
		}
	}
	for id, invariant := range landing {
		if p, ok := position[id]; !ok || !visible[p] {
			fail(invariant, "the %s tile is not on the landing page", id)
		}
	}
	for _, entry := range doc.Layout.Entries {
		if entry.Tile == tiles.TypeRisks && entry.Group != "" && !(generated && entry.Group == tiles.GroupHidden) {
			fail(6, "the risks tile is in a group")
		}
	}

	// 8. Tile-level checks.
	for i := range doc.Tiles {
		tile := &doc.Tiles[i]
		if generated && composed(tile) && tile.State == tiles.StateUnavailable {
			// A library or authored tile that fails its checks is shown
			// unavailable, with its problems.
			if len(tile.Problems) == 0 {
				fail(8, "tile %s is unavailable without a problem", tile.ID)
			}
			continue
		}
		if tile.State == tiles.StateUnavailable {
			fail(8, "tile %s is unavailable", tile.ID)
		}
		problems, err := validator.TileProblems(doc, tile)
		if err != nil {
			fail(8, "%v", err)
		}
		for _, problem := range problems {
			fail(8, "tile %s: %s", tile.ID, problem)
		}
	}

	// 9. Round trip.
	back, err := tiles.ToReview(doc)
	if err != nil {
		fail(9, "ToReview: %v", err)
	} else if equal, err := RoundTripEqual(normalized, back); err != nil {
		fail(9, "%v", err)
	} else if !equal {
		fail(9, "ToReview differs from the review")
	}
	return failures
}

// blockFailures rebuilds each section's blocks, by identity, from the tiles
// and compares them with the review's.
func blockFailures(source review, doc *tiles.Document) []string {
	var failures []string
	fail := func(format string, args ...any) { failures = append(failures, fmt.Sprintf(format, args...)) }
	moved := map[string]map[int]block{}
	claim := func(origin *tiles.BlockOrigin, b block, what string) {
		if origin == nil {
			fail("a moved %s has no origin", what)
			return
		}
		if moved[origin.Change] == nil {
			moved[origin.Change] = map[int]block{}
		}
		if _, ok := moved[origin.Change][origin.Block]; ok {
			fail("two moved blocks claim block %d of %s", origin.Block, origin.Change)
		}
		moved[origin.Change][origin.Block] = b
	}
	kept := map[string][]json.RawMessage{}
	for _, tile := range doc.Tiles {
		switch tile.Type {
		case tiles.TypeChange:
			var data tiles.ChangeData
			_ = json.Unmarshal(tile.Data, &data)
			kept[data.Change] = data.Blocks
		case tiles.TypeEverythingElse:
			var data tiles.EverythingElseData
			_ = json.Unmarshal(tile.Data, &data)
			if data.Change != nil && data.Blocks != nil {
				kept[*data.Change] = *data.Blocks
			}
		case tiles.TypeIntent:
			var data tiles.IntentData
			_ = json.Unmarshal(tile.Data, &data)
			for _, line := range data.Lines {
				claim(line.Origin, block{Kind: "intent"}, "intent line")
			}
		case tiles.TypeMedia:
			var data tiles.MediaData
			_ = json.Unmarshal(tile.Data, &data)
			for _, m := range data.Items {
				claim(m.Origin, identity(m.Block), "media block")
			}
		case tiles.TypeTable, tiles.TypeCode:
			if origin := promotedOrigin(&tile); origin != nil {
				claim(origin, block{Kind: tile.Type}, "promoted block")
			}
		case tiles.TypeDecisions:
			var data tiles.DecisionsData
			_ = json.Unmarshal(tile.Data, &data)
			type position struct {
				change   string
				block    int
				item     int
				original string
			}
			var items []position
			for _, raw := range data.Items {
				var d struct {
					ID     string             `json:"id"`
					Origin *tiles.BlockOrigin `json:"origin"`
				}
				_ = json.Unmarshal(raw, &d)
				if d.Origin == nil || d.Origin.Item == nil {
					fail("a decision has no origin")
					continue
				}
				id := d.ID
				if d.Origin.ID != "" {
					id = d.Origin.ID
				}
				items = append(items, position{d.Origin.Change, d.Origin.Block, *d.Origin.Item, id})
			}
			type key struct {
				change string
				block  int
			}
			groups := map[key][]position{}
			var order []key
			for _, p := range items {
				k := key{p.change, p.block}
				if groups[k] == nil {
					order = append(order, k)
				}
				groups[k] = append(groups[k], p)
			}
			for _, k := range order {
				group := groups[k]
				slices.SortStableFunc(group, func(a, b position) int { return a.item - b.item })
				b := block{Kind: "decisions"}
				for i, p := range group {
					if p.item != i {
						fail("decision %d of block %d of %s is out of place", p.item, p.block, p.change)
					}
					b.Items = append(b.Items, p.original)
				}
				claim(&tiles.BlockOrigin{Change: k.change, Block: k.block}, b, "decisions block")
			}
		}
	}
	sections := map[string]bool{}
	for _, s := range source.Sections {
		sections[s.ID] = true
		var want, got []block
		for _, raw := range s.Blocks {
			want = append(want, identity(raw))
		}
		rest := kept[s.ID]
		total := len(rest) + len(moved[s.ID])
		for p := range total {
			if b, ok := moved[s.ID][p]; ok {
				got = append(got, b)
				continue
			}
			if len(rest) == 0 {
				fail("a block moved from %s is past its blocks", s.ID)
				break
			}
			got = append(got, identity(rest[0]))
			rest = rest[1:]
		}
		if !reflect.DeepEqual(want, got) {
			fail("the blocks of %s are not the review's, in order", s.ID)
		}
	}
	for change := range moved {
		if !sections[change] {
			fail("a moved block names change %s, which the review does not have", change)
		}
	}
	return failures
}

// RoundTripEqual reports whether a baum.review/1 document converted back by
// ToReview is JSON-equal to the review it was converted from, ignoring the
// retired per-section and per-risk tiles, which the conversion drops.
func RoundTripEqual(normalized, back []byte) (bool, error) {
	var want, got any
	if err := json.Unmarshal(normalized, &want); err != nil {
		return false, err
	}
	if err := json.Unmarshal(back, &got); err != nil {
		return false, err
	}
	root, _ := want.(map[string]any)
	sections, _ := root["sections"].([]any)
	for _, raw := range sections {
		section, _ := raw.(map[string]any)
		delete(section, "tiles")
		risks, _ := section["risks"].([]any)
		for _, raw := range risks {
			if risk, ok := raw.(map[string]any); ok {
				delete(risk, "tiles")
			}
		}
	}
	return reflect.DeepEqual(want, got), nil
}

// CheckStability checks the tenth invariant on two consecutive versions of
// one pull request: a change both versions have is presented by a tile with
// the same ID and type in both (its change tile, or everything_else for the
// current mechanical change; a change that is mechanical in either version
// may move between them, as it becomes obsolete or current again or changes
// kind); a tile ID both versions have names a tile of the same subject in
// both: a change or single tile of the same type, a library tile of the same
// type, a promoted block of the same change and kind, or an authored tile of
// the same type; and a promoted block whose change and kind exactly one
// promoted tile of each version has keeps its ID, unless another tile of the
// next version has that ID (tiles.StabilizeIDs).
func CheckStability(previous, next *tiles.Document) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, "invariant 10: "+fmt.Sprintf(format, args...))
	}
	subjects := func(doc *tiles.Document) map[string]string {
		byID := map[string]string{}
		for i := range doc.Tiles {
			byID[doc.Tiles[i].ID] = stableSubject(&doc.Tiles[i])
		}
		return byID
	}
	before, after := subjects(previous), subjects(next)
	for id, subject := range before {
		other, ok := after[id]
		if !ok {
			continue
		}
		if other != subject {
			fail("tile %s is a %s tile in one version and a %s tile in the next", id, subject, other)
		}
	}
	promotions := func(doc *tiles.Document) map[string][]string {
		byKey := map[string][]string{}
		for i := range doc.Tiles {
			tile := &doc.Tiles[i]
			if composed(tile) || (tile.Type != tiles.TypeTable && tile.Type != tiles.TypeCode) {
				continue
			}
			if origin := promotedOrigin(tile); origin != nil {
				key := tile.Type + " " + origin.Change
				byKey[key] = append(byKey[key], tile.ID)
			}
		}
		return byKey
	}
	was, now := promotions(previous), promotions(next)
	for key, ids := range was {
		if len(ids) != 1 || len(now[key]) != 1 || ids[0] == now[key][0] {
			continue
		}
		if _, taken := after[ids[0]]; !taken {
			fail("a promoted %s keeps neither its ID %s nor gives it to another tile", key, ids[0])
		}
	}
	presenters := func(doc *tiles.Document) (map[string]string, map[string]string) {
		presenter, kinds := map[string]string{}, map[string]string{}
		for _, change := range doc.Changes {
			kinds[change.ID] = change.Kind
		}
		for _, tile := range doc.Tiles {
			switch tile.Type {
			case tiles.TypeChange:
				presenter[tile.ID] = tile.ID
			case tiles.TypeEverythingElse:
				var data tiles.EverythingElseData
				if json.Unmarshal(tile.Data, &data) == nil && data.Change != nil {
					presenter[*data.Change] = tile.ID
				}
			}
		}
		return presenter, kinds
	}
	presented, kinds := presenters(previous)
	presentedNow, _ := presenters(next)
	for _, change := range next.Changes {
		if _, ok := kinds[change.ID]; !ok {
			continue
		}
		if presented[change.ID] != presentedNow[change.ID] && change.Kind != tiles.KindMechanical && kinds[change.ID] != tiles.KindMechanical {
			fail("change %s is shown by tile %q in one version and %q in the next", change.ID, presented[change.ID], presentedNow[change.ID])
		}
	}
	return failures
}

func original(id string, origin *tiles.RenameOrigin) string {
	if origin != nil {
		return origin.ID
	}
	return id
}

func sorted(values []string) []string {
	values = slices.Clone(values)
	slices.Sort(values)
	return values
}

// jsonEqual reports whether two raw values are JSON-equal, an absent value
// equal only to an absent one.
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// SealDefinition encodes a custom tile type definition with its digest
// written, as the host writes it. It panics on a definition that cannot be
// encoded, which only a test can build.
func SealDefinition(def tiles.TypeDefinition) json.RawMessage {
	def.Digest = ""
	encoded, err := json.Marshal(def)
	if err != nil {
		panic(err)
	}
	digest, err := tiles.DefinitionDigest(encoded)
	if err != nil {
		panic(err)
	}
	def.Digest = digest
	encoded, err = json.Marshal(def)
	if err != nil {
		panic(err)
	}
	return encoded
}

// CustomTile returns a ready custom tile with a sealed definition.
func CustomTile(id, title string, def tiles.TypeDefinition, data json.RawMessage) tiles.Tile {
	return tiles.Tile{ID: id, Type: tiles.TypeCustom, Title: title, Data: data, State: tiles.StateReady, Definition: SealDefinition(def)}
}

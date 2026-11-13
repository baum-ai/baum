package tiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// CheckConversion checks that a tile document converted from a normalized
// baum.review/1 document (review.NormalizeStored) presents all of it, and
// returns every failure, each prefixed with the number of the invariant it
// breaks:
//
//  1. The tiles' covers together are the current hunks (those of current
//     sections plus the unassigned ones), which are the index's hunks.
//  2. Each section's risks, active and mitigated, are in risks exactly once,
//     and the risks tile ranks exactly the active risks of current changes.
//  3. Each section's evidence items are in evidence exactly once.
//  4. Each block is in exactly one tile, with as many blocks of each kind as
//     the review has, and so is each decision and each image.
//  5. The recommendation and the risk score are JSON-equal to the review's,
//     present exactly when the review's are.
//  6. Every active critical or high risk of a current change is ranked, and
//     the risks tile is on the landing page.
//  7. Every tile is placed exactly once, landing is what Landing derives,
//     and the overview and risks tiles are on the landing page.
//  8. Every tile passes the tile-level checks; none is unavailable.
//  9. ToReview of the tile document is JSON-equal to the review without the
//     retired per-section and per-risk tiles.
//
// The tenth invariant compares consecutive versions (CheckStability).
func CheckConversion(review []byte, doc *Document) []string {
	var failures []string
	fail := func(invariant int, format string, args ...any) {
		failures = append(failures, fmt.Sprintf("invariant %d: ", invariant)+fmt.Sprintf(format, args...))
	}
	var source reviewDocument
	if err := json.Unmarshal(review, &source); err != nil {
		return []string{"review: " + err.Error()}
	}
	tiles := map[string]*Tile{}
	for i := range doc.Tiles {
		tiles[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	current := map[string]bool{}
	for _, s := range source.Sections {
		var derived derivedView
		_ = json.Unmarshal(s.Derived, &derived)
		current[s.ID] = derived.State != stateStale
	}

	// 1. Coverage.
	covered := map[string]bool{}
	for _, tile := range doc.Tiles {
		for _, hunk := range tile.Covers {
			covered[hunk] = true
		}
	}
	want := map[string]bool{}
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
	indexed, err := indexHunks(source.Index)
	if err != nil {
		fail(1, "index: %v", err)
	}
	if !sameKeys(covered, want) {
		fail(1, "the tiles cover %d hunks, the current sections and unassigned hunks are %d", len(covered), len(want))
	}
	if indexed != nil && !sameKeys(want, indexed) {
		fail(1, "the current sections and unassigned hunks are %d hunks, the index lists %d", len(want), len(indexed))
	}

	// 2 and 3. Every risk and evidence item once, by section.
	for _, s := range source.Sections {
		var wantRisks, gotRisks, wantEvidence, gotEvidence []string
		for _, raw := range s.Risks {
			wantRisks = append(wantRisks, itemID(raw))
		}
		for _, raw := range s.Evidence {
			wantEvidence = append(wantEvidence, itemID(raw))
		}
		for _, risk := range doc.Risks {
			if risk.Change == s.ID {
				gotRisks = append(gotRisks, originalID(risk.ID, risk.Origin))
			}
		}
		for _, item := range doc.Evidence {
			if item.Change == s.ID {
				gotEvidence = append(gotEvidence, originalID(item.ID, item.Origin))
			}
		}
		if !slices.Equal(wantRisks, gotRisks) {
			fail(2, "section %s has %d risks, the tile document %d for it", s.ID, len(wantRisks), len(gotRisks))
		}
		if !slices.Equal(wantEvidence, gotEvidence) {
			fail(3, "section %s has %d evidence items, the tile document %d for it", s.ID, len(wantEvidence), len(gotEvidence))
		}
	}
	if len(doc.Risks) != countRisks(source) {
		fail(2, "the review has %d risks, the tile document %d", countRisks(source), len(doc.Risks))
	}
	var ranked RisksData
	if tile := tiles[TypeRisks]; tile == nil {
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
	if !sameSet(active, ranked.Ranked) || len(ranked.Ranked) != len(active) {
		fail(2, "the risks tile ranks %d risks, the active risks of current changes are %d", len(ranked.Ranked), len(active))
	}

	// 4. Blocks.
	failures = append(failures, blockFailures(source, doc, tiles)...)

	// 5. Recommendation and risk score.
	if !jsonEqual(source.Summary.Recommendation, doc.Recommendation) {
		fail(5, "the recommendation differs from the review's")
	}
	if !jsonEqual(source.Summary.RiskScore, doc.RiskScore) {
		fail(5, "the risk score differs from the review's")
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
	if landing := Landing(doc.Layout); landing != doc.Layout.Landing {
		fail(7, "landing is %d, the layout packs %d", doc.Layout.Landing, landing)
	}
	for _, id := range []string{TypeOverview, TypeRisks} {
		if p, ok := position[id]; !ok || p >= doc.Layout.Landing {
			fail(map[string]int{TypeOverview: 7, TypeRisks: 6}[id], "the %s tile is not on the landing page", id)
		}
	}
	rankedSet := map[string]bool{}
	for _, id := range ranked.Ranked {
		rankedSet[id] = true
	}
	for _, risk := range doc.Risks {
		if risk.State == "active" && current[risk.Change] && (risk.Tier == "critical" || risk.Tier == "high") && !rankedSet[risk.ID] {
			fail(6, "%s risk %s is not ranked", risk.Tier, risk.ID)
		}
	}

	// 8. Tile-level checks.
	data, err := defaultValidator.data()
	if err != nil {
		fail(8, "%v", err)
	} else {
		refs := newTileRefs(doc)
		for i := range doc.Tiles {
			tile := &doc.Tiles[i]
			if tile.State == StateUnavailable {
				fail(8, "tile %s is unavailable", tile.ID)
			}
			for _, problem := range tileProblems(data, refs, tile) {
				fail(8, "tile %s: %s", tile.ID, problem)
			}
		}
	}

	// 9. Round trip.
	back, err := ToReview(doc)
	if err != nil {
		fail(9, "ToReview: %v", err)
	} else if equal, err := RoundTripEqual(review, back); err != nil {
		fail(9, "%v", err)
	} else if !equal {
		fail(9, "ToReview differs from the review")
	}
	return failures
}

// RoundTripEqual reports whether a baum.review/1 document converted back by
// ToReview is JSON-equal to the review it was converted from, ignoring the
// retired per-section and per-risk tiles, which the conversion drops.
func RoundTripEqual(review, back []byte) (bool, error) {
	var want, got any
	if err := json.Unmarshal(review, &want); err != nil {
		return false, err
	}
	if err := json.Unmarshal(back, &got); err != nil {
		return false, err
	}
	withoutRetiredTiles(want)
	return reflect.DeepEqual(want, got), nil
}

func withoutRetiredTiles(document any) {
	root, _ := document.(map[string]any)
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
}

func blockFailures(source reviewDocument, doc *Document, tiles map[string]*Tile) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, "invariant 4: "+fmt.Sprintf(format, args...))
	}
	want := map[string]int{}
	wantDecisions, wantImages := 0, 0
	for _, s := range source.Sections {
		for _, raw := range s.Blocks {
			kind, _ := blockKind(raw)
			want[kind]++
			wantDecisions += len(decisionItems(raw))
			wantImages += len(imageItems(raw))
		}
	}
	got := map[string]int{}
	gotDecisions, gotImages := 0, 0
	count := func(blocks []json.RawMessage) {
		for _, raw := range blocks {
			kind, _ := blockKind(raw)
			got[kind]++
			gotDecisions += len(decisionItems(raw))
			gotImages += len(imageItems(raw))
		}
	}
	type place struct {
		change string
		block  int
	}
	placed := map[place]bool{}
	claim := func(origin *BlockOrigin, item bool) {
		if origin == nil {
			fail("a moved block has no origin")
			return
		}
		key := place{origin.Change, origin.Block}
		if placed[key] && !item {
			fail("two moved blocks claim block %d of %s", origin.Block, origin.Change)
		}
		placed[key] = true
	}
	for _, tile := range doc.Tiles {
		switch tile.Type {
		case TypeChange:
			var data ChangeData
			_ = json.Unmarshal(tile.Data, &data)
			count(data.Blocks)
		case TypeEverythingElse:
			var data EverythingElseData
			_ = json.Unmarshal(tile.Data, &data)
			if data.Blocks != nil {
				count(*data.Blocks)
			}
		case TypeIntent:
			var data IntentData
			_ = json.Unmarshal(tile.Data, &data)
			for _, line := range data.Lines {
				got[blockIntent]++
				claim(line.Origin, false)
			}
		case TypeMedia:
			var data MediaData
			_ = json.Unmarshal(tile.Data, &data)
			for _, item := range data.Items {
				count([]json.RawMessage{item.Block})
				claim(item.Origin, false)
			}
		case TypeDecisions:
			blocks, err := decisionBlocks(tile.Data)
			if err != nil {
				fail("decisions data: %v", err)
			}
			for _, block := range blocks {
				count([]json.RawMessage{block.raw})
				claim(&BlockOrigin{Change: block.change, Block: block.block}, false)
			}
		}
	}
	for _, kind := range sortedKeys(want, got) {
		if want[kind] != got[kind] {
			fail("the review has %d %s blocks, the tiles %d", want[kind], kind, got[kind])
		}
	}
	if wantDecisions != gotDecisions {
		fail("the review has %d decisions, the tiles %d", wantDecisions, gotDecisions)
	}
	if wantImages != gotImages {
		fail("the review has %d images, the tiles %d", wantImages, gotImages)
	}
	return failures
}

func decisionItems(raw json.RawMessage) []json.RawMessage {
	block, _ := decodeDecisions(raw)
	if block == nil {
		return nil
	}
	return block.Items
}

func imageItems(raw json.RawMessage) []json.RawMessage {
	var block struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &block) != nil || block.Kind != blockImages {
		return nil
	}
	return block.Items
}

func sortedKeys(counts ...map[string]int) []string {
	var keys []string
	for _, m := range counts {
		for key := range m {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

func itemID(raw json.RawMessage) string {
	var item struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &item)
	return item.ID
}

func originalID(id string, origin *RenameOrigin) string {
	if origin != nil {
		return origin.ID
	}
	return id
}

func countRisks(source reviewDocument) int {
	n := 0
	for _, s := range source.Sections {
		n += len(s.Risks)
	}
	return n
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	set := map[string]bool{}
	for _, id := range a {
		set[id] = true
	}
	other := map[string]bool{}
	for _, id := range b {
		other[id] = true
	}
	return sameKeys(set, other)
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

// CheckStability checks the tenth invariant on two consecutive versions of
// one pull request: the tile IDs both have are exactly the IDs of the changes
// both show as change tiles plus the single tiles both have, and a tile ID
// both have names a tile of the same type.
func CheckStability(previous, next *Document) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, "invariant 10: "+fmt.Sprintf(format, args...))
	}
	types := func(doc *Document) map[string]string {
		byID := map[string]string{}
		for _, tile := range doc.Tiles {
			byID[tile.ID] = tile.Type
		}
		return byID
	}
	before, after := types(previous), types(next)
	expected := map[string]bool{}
	for id, kind := range before {
		if after[id] != kind {
			continue
		}
		builtin, ok := Builtin(kind)
		if kind == TypeChange || ok && builtin.Single {
			expected[id] = true
		}
	}
	for id, kind := range before {
		if other, ok := after[id]; ok {
			if other != kind {
				fail("tile %s is a %s tile in one version and a %s tile in the next", id, kind, other)
			}
			if !expected[id] {
				fail("tile %s is in both versions but is neither a change nor a single tile", id)
			}
		}
	}
	changes := func(doc *Document) map[string]bool {
		ids := map[string]bool{}
		for _, change := range doc.Changes {
			ids[change.ID] = true
		}
		return ids
	}
	beforeChanges, afterChanges := changes(previous), changes(next)
	for id := range expected {
		if before[id] == TypeChange && !(beforeChanges[id] && afterChanges[id]) {
			fail("change tile %s is in both versions but its change is not", id)
		}
	}
	return failures
}

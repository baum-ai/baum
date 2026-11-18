package tiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	reviewschema "github.com/baum-ai/baum/schema"
)

// ToReview converts a tile document back into a baum.review/1 document. For a
// converted document it is the inverse of FromReview: renamed items get their
// IDs back, and so do the references FromReview rewrote. It reads the
// substrate and the tiles that hold review content (overview, change,
// intent, decisions, context, media and everything_else); the risks and
// evidence tiles and the layout only present the substrate. A table or code
// tile with an origin holds a block a composition promoted, which goes back
// to its change. baum.review/1 has no place for custom tiles, or for other
// prose, table, diagram and code tiles, so the review it returns leaves them
// out.
func ToReview(doc *Document) ([]byte, error) {
	if doc.Schema != Name {
		return nil, fmt.Errorf("tiles: the document's schema is %q, not %q", doc.Schema, Name)
	}
	byID := make(map[string]*Tile, len(doc.Tiles))
	for i := range doc.Tiles {
		// A custom tile may have any ID, including a built-in single tile's
		// the document does not use; it holds no review content.
		if doc.Tiles[i].Type != TypeCustom {
			byID[doc.Tiles[i].ID] = &doc.Tiles[i]
		}
	}
	overviewTile := byID[TypeOverview]
	if overviewTile == nil || overviewTile.Type != TypeOverview {
		return nil, errors.New("tiles: the document has no overview tile")
	}
	var overview OverviewData
	if err := json.Unmarshal(overviewTile.Data, &overview); err != nil {
		return nil, fmt.Errorf("tiles: overview data: %w", err)
	}
	summary := reviewSummary{
		Title:          doc.Facts.Title,
		Text:           overview.Text,
		ReviewExchange: doc.Facts.ReviewExchange,
		ChangeKind:     overview.ChangeKind,
		Implements:     json.RawMessage(`[]`),
		ReleaseNote:    overview.ReleaseNote,
		BreakingChange: overview.BreakingChange,
		ReviewMinutes:  doc.Facts.ReviewMinutes,
		Attention:      overview.Attention,
		Recommendation: doc.Recommendation,
		Author:         doc.Facts.Author,
		Authorship:     doc.Facts.Authorship,
		RiskScore:      doc.RiskScore,
	}
	moved := map[string]map[int]json.RawMessage{}
	addMoved := func(change string, block int, raw json.RawMessage) error {
		if moved[change] == nil {
			moved[change] = map[int]json.RawMessage{}
		}
		if moved[change][block] != nil {
			return fmt.Errorf("tiles: two moved blocks claim one position of a change")
		}
		moved[change][block] = raw
		return nil
	}
	if tile := byID[TypeIntent]; tile != nil {
		var intent IntentData
		if err := json.Unmarshal(tile.Data, &intent); err != nil {
			return nil, fmt.Errorf("tiles: intent data: %w", err)
		}
		summary.Implements = intent.Implements
		for _, line := range intent.Lines {
			if line.Origin == nil {
				continue
			}
			raw, err := json.Marshal(intentBlock{Kind: blockIntent, Text: line.Text, Anchors: line.Anchors})
			if err != nil {
				return nil, err
			}
			if err := addMoved(line.Origin.Change, line.Origin.Block, raw); err != nil {
				return nil, err
			}
		}
	}
	// A context tile without data is a draft's context still being written.
	if tile := byID[TypeContext]; tile != nil && len(tile.Data) > 0 && !isEmptyObject(tile.Data) {
		summary.Context = tile.Data
	}
	if tile := byID[TypeMedia]; tile != nil {
		var media MediaData
		if err := json.Unmarshal(tile.Data, &media); err != nil {
			return nil, fmt.Errorf("tiles: media data: %w", err)
		}
		for _, item := range media.Items {
			if item.Origin == nil {
				continue
			}
			if err := addMoved(item.Origin.Change, item.Origin.Block, item.Block); err != nil {
				return nil, err
			}
		}
	}
	if tile := byID[TypeDecisions]; tile != nil {
		blocks, err := decisionBlocks(tile.Data)
		if err != nil {
			return nil, err
		}
		for _, block := range blocks {
			if err := addMoved(block.change, block.block, block.raw); err != nil {
				return nil, err
			}
		}
	}

	for i := range doc.Tiles {
		tile := &doc.Tiles[i]
		if tile.Type != TypeTable && tile.Type != TypeCode {
			continue
		}
		raw, origin, err := promotedBlock(tile)
		if err != nil {
			return nil, err
		}
		if origin != nil {
			if err := addMoved(origin.Change, origin.Block, raw); err != nil {
				return nil, err
			}
		}
	}

	var everythingElse *EverythingElseData
	if tile := byID[TypeEverythingElse]; tile != nil {
		everythingElse = &EverythingElseData{}
		if err := json.Unmarshal(tile.Data, everythingElse); err != nil {
			return nil, fmt.Errorf("tiles: everything_else data: %w", err)
		}
	}

	out := reviewDocument{
		Schema:          reviewschema.Name,
		Review:          doc.Review,
		Summary:         summary,
		Stats:           doc.Stats,
		Stack:           doc.Stack,
		Sections:        []reviewSection{},
		AgentReviews:    doc.AgentReviews,
		UnassignedHunks: nonNil(doc.UnassignedHunks),
		Index:           doc.Index,
	}
	for _, change := range doc.Changes {
		section, err := toSection(doc, change, byID, everythingElse, moved[change.ID])
		if err != nil {
			return nil, err
		}
		out.Sections = append(out.Sections, section)
	}
	return json.Marshal(out)
}

// promotedBlock rebuilds the block a promoted table or code tile holds,
// with its origin, or returns a nil origin for a tile without one.
func promotedBlock(tile *Tile) (json.RawMessage, *BlockOrigin, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(tile.Data, &fields); err != nil {
		return nil, nil, fmt.Errorf("tiles: %s data: %w", tile.Type, err)
	}
	if fields["origin"] == nil {
		return nil, nil, nil
	}
	var origin BlockOrigin
	if err := json.Unmarshal(fields["origin"], &origin); err != nil {
		return nil, nil, fmt.Errorf("tiles: %s origin: %w", tile.Type, err)
	}
	delete(fields, "origin")
	fields["kind"] = jsonString(tile.Type)
	raw, err := json.Marshal(fields)
	return raw, &origin, err
}

type movedDecisions struct {
	change string
	block  int
	raw    json.RawMessage
}

// decisionBlocks rebuilds the decisions blocks the decisions tile's items
// came from, each with its items in their order and their own IDs.
func decisionBlocks(data json.RawMessage) ([]movedDecisions, error) {
	var decisions DecisionsData
	if err := json.Unmarshal(data, &decisions); err != nil {
		return nil, fmt.Errorf("tiles: decisions data: %w", err)
	}
	type key struct {
		change string
		block  int
	}
	type positioned struct {
		item  int
		value json.RawMessage
	}
	var order []key
	items := map[key][]positioned{}
	for _, raw := range decisions.Items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("tiles: decision item: %w", err)
		}
		var origin BlockOrigin
		if fields["origin"] == nil {
			continue
		}
		if err := json.Unmarshal(fields["origin"], &origin); err != nil {
			return nil, fmt.Errorf("tiles: decision item origin: %w", err)
		}
		delete(fields, "origin")
		delete(fields, "change")
		if origin.ID != "" {
			fields["id"] = jsonString(origin.ID)
		}
		value, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		k := key{origin.Change, origin.Block}
		if items[k] == nil {
			order = append(order, k)
		}
		position := len(items[k])
		if origin.Item != nil {
			position = *origin.Item
		}
		items[k] = append(items[k], positioned{position, value})
	}
	blocks := make([]movedDecisions, 0, len(order))
	for _, k := range order {
		group := items[k]
		slices.SortStableFunc(group, func(a, b positioned) int { return a.item - b.item })
		values := make([]json.RawMessage, len(group))
		for i, item := range group {
			values[i] = item.value
		}
		raw, err := json.Marshal(decisionsBlock{Kind: blockDecisions, Items: values})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, movedDecisions{k.change, k.block, raw})
	}
	return blocks, nil
}

func jsonString(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

// toSection rebuilds one section from its change, its tile, the blocks the
// aggregate tiles hold for it and its risks and evidence.
func toSection(doc *Document, change Change, byID map[string]*Tile, everythingElse *EverythingElseData, moved map[int]json.RawMessage) (reviewSection, error) {
	section := reviewSection{
		ID:           change.ID,
		Title:        change.Title,
		Kind:         change.Kind,
		Hunks:        nonNil(change.Hunks),
		PullRequests: change.PullRequests,
		Derived:      change.Derived,
		Assessment:   change.Assessment,
		Details:      change.Details,
		Risks:        []json.RawMessage{},
		Evidence:     []json.RawMessage{},
	}
	var kept []json.RawMessage
	switch {
	case byID[change.ID] != nil && byID[change.ID].Type == TypeChange:
		var data ChangeData
		if err := json.Unmarshal(byID[change.ID].Data, &data); err != nil {
			return section, fmt.Errorf("tiles: change tile data: %w", err)
		}
		section.Summary, section.Attention, kept = data.Summary, data.Attention, data.Blocks
	case everythingElse != nil && everythingElse.Change != nil && *everythingElse.Change == change.ID:
		section.Summary, section.Attention = everythingElse.Summary, everythingElse.Attention
		if everythingElse.Blocks != nil {
			kept = *everythingElse.Blocks
		}
	default:
		return section, fmt.Errorf("tiles: a change has no tile")
	}
	total := len(kept) + len(moved)
	blocks := make([]json.RawMessage, 0, total)
	next := 0
	for position := range total {
		if raw, ok := moved[position]; ok {
			blocks = append(blocks, raw)
			continue
		}
		if next == len(kept) {
			return section, fmt.Errorf("tiles: a moved block's position is past the change's blocks")
		}
		blocks = append(blocks, kept[next])
		next++
	}
	section.Blocks = blocks

	refs := sectionRefs{}
	seen := map[string]bool{}
	for _, risk := range doc.Risks {
		if risk.Change != change.ID {
			continue
		}
		id := risk.ID
		if risk.Origin != nil {
			id = risk.Origin.ID
		}
		refs.add(id, risk.ID, seen)
		raw, err := json.Marshal(reviewRisk{
			ID:                id,
			Tier:              risk.Tier,
			Category:          risk.Category,
			Source:            risk.Source,
			Text:              risk.Text,
			Anchors:           risk.Anchors,
			Finding:           risk.Finding,
			State:             risk.State,
			IntroducedVersion: risk.IntroducedVersion,
			MitigatedVersion:  risk.MitigatedVersion,
		})
		if err != nil {
			return section, err
		}
		section.Risks = append(section.Risks, raw)
	}
	for _, item := range doc.Evidence {
		if item.Change != change.ID {
			continue
		}
		id := item.ID
		if item.Origin != nil {
			id = item.Origin.ID
		}
		refs.add(id, item.ID, seen)
		raw, err := json.Marshal(reviewEvidence{
			ID:          id,
			Kind:        item.Kind,
			Claim:       item.Claim,
			Status:      item.Status,
			Granularity: item.Granularity,
			Source:      item.Source,
			Anchors:     item.Anchors,
			Detail:      item.Detail,
			Expects:     item.Expects,
		})
		if err != nil {
			return section, err
		}
		section.Evidence = append(section.Evidence, raw)
	}
	if err := addDecisionRefs(doc, change.ID, refs, seen); err != nil {
		return section, err
	}
	if len(refs) == 0 {
		return section, nil
	}
	inverse := refs.inverse()
	for _, field := range []*json.RawMessage{&section.Summary, &section.Attention} {
		if len(*field) == 0 {
			continue
		}
		rewritten, err := inverse.rewrite(*field)
		if err != nil {
			return section, err
		}
		*field = rewritten
	}
	for _, values := range [][]json.RawMessage{section.Risks, section.Evidence, section.Blocks} {
		if err := inverse.rewriteAll(values); err != nil {
			return section, err
		}
	}
	return section, nil
}

// addDecisionRefs adds the change's decision items, in block and item order,
// to the references of its renamed items.
func addDecisionRefs(doc *Document, change string, refs sectionRefs, seen map[string]bool) error {
	var tile *Tile
	for i := range doc.Tiles {
		if doc.Tiles[i].Type == TypeDecisions {
			tile = &doc.Tiles[i]
		}
	}
	if tile == nil {
		return nil
	}
	var decisions DecisionsData
	if err := json.Unmarshal(tile.Data, &decisions); err != nil {
		return err
	}
	type item struct {
		id, original string
		block, item  int
	}
	var items []item
	for _, raw := range decisions.Items {
		var decision struct {
			ID     string       `json:"id"`
			Origin *BlockOrigin `json:"origin"`
		}
		if err := json.Unmarshal(raw, &decision); err != nil {
			return err
		}
		if decision.Origin == nil || decision.Origin.Change != change {
			continue
		}
		original := decision.ID
		if decision.Origin.ID != "" {
			original = decision.Origin.ID
		}
		position := 0
		if decision.Origin.Item != nil {
			position = *decision.Origin.Item
		}
		items = append(items, item{decision.ID, original, decision.Origin.Block, position})
	}
	slices.SortStableFunc(items, func(a, b item) int {
		if a.block != b.block {
			return a.block - b.block
		}
		return a.item - b.item
	})
	for _, item := range items {
		refs.add(item.original, item.id, seen)
	}
	return nil
}

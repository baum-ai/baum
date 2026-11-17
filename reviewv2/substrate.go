package reviewv2

import (
	"encoding/json"

	"github.com/baum-ai/baum/tiles"
)

// Substrate is the tile document (baum.tiles/1) that a baum.review/2
// document's library and custom tiles are checked against, as one converted
// from a baum.review/1 review is (tiles.FromReview): the header, stats and
// index, each section as a change with its derived facts, and each risk with
// its change. It holds no tiles of its own, so a library tile's citations
// resolve to the document's hunks, files, sections and risks
// (tiles.ExtraTiles).
func Substrate(d Document) *tiles.Document {
	doc := &tiles.Document{
		Schema: tiles.Name, Origin: tiles.Origin{Kind: tiles.OriginConverted, From: Name, Converter: tiles.ConverterVersion},
		Review: d.Review, Stats: d.Stats, Index: d.Index, AgentReviews: json.RawMessage("[]"), UnassignedHunks: []string{},
		Facts:     tiles.Facts{Title: d.Summary.Title},
		RiskScore: d.Summary.RiskScore,
		Changes:   []tiles.Change{}, Risks: []tiles.Risk{}, Evidence: []tiles.Evidence{}, Tiles: []tiles.Tile{},
	}
	for _, s := range d.Sections {
		derived := s.Derived
		if len(derived) == 0 {
			derived = json.RawMessage(`{"state":"new"}`)
		}
		doc.Changes = append(doc.Changes, tiles.Change{ID: s.ID, Title: s.Title, Kind: s.Kind, Hunks: s.Hunks, Details: s.Details, Derived: derived})
		for _, r := range s.Risks {
			anchors, _ := json.Marshal(r.Cites)
			risk := tiles.Risk{ID: r.ID, Tier: r.Tier, Category: r.Category, Source: "model", Text: r.Text, Anchors: anchors, State: r.State, MitigatedVersion: r.MitigatedVersion, Change: s.ID}
			if risk.State == "" {
				risk.State = "active"
			}
			if r.IntroducedVersion != nil {
				risk.IntroducedVersion = *r.IntroducedVersion
			}
			doc.Risks = append(doc.Risks, risk)
		}
	}
	return doc
}

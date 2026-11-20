package prompt

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/tiles/view"
)

// V2Version is the prompt version of a baum.review/2 generation (the
// levels of agent/orchestrate). Change it with the text of any of its files:
// review-v2-shared.md, review-map-v1.md, review-section-v2.md,
// review-context-v2.md, review-tile-v1.md and its review-tile-<type>-v1.md
// parts, and review-overview-v1.md.
const V2Version = "review-v2-public-v2"

//go:embed review-v2-shared.md
var v2Shared string

//go:embed review-map-v1.md
var v2Map string

//go:embed review-section-v2.md
var v2Section string

//go:embed review-context-v2.md
var v2Context string

//go:embed review-tile-v1.md
var v2Tile string

//go:embed review-tile-flow-v1.md
var v2TileFlow string

//go:embed review-tile-columns-v1.md
var v2TileColumns string

//go:embed review-tile-sequence-v1.md
var v2TileSequence string

//go:embed review-tile-table-v1.md
var v2TileTable string

//go:embed review-overview-v1.md
var v2Overview string

// The baum.review/2 prompts a host can replace.
const (
	IDReviewV2Shared  ID = "review-v2-shared"
	IDReviewMap       ID = "review-map"
	IDReviewSection   ID = "review-section"
	IDReviewContextV2 ID = "review-context-v2"
	IDReviewTile      ID = "review-tile"
	IDReviewOverview  ID = "review-overview"
)

// V2ReviewIDs are the prompts a baum.review/2 generation composes, in the
// order of its levels.
func V2ReviewIDs() []ID {
	return []ID{IDReviewV2Shared, IDReviewMap, IDReviewSection, IDReviewContextV2, IDReviewTile, IDReviewOverview}
}

// What each call returns, in its submit block.
const (
	mapPart      = "the map"
	sectionPart  = "the section"
	contextPartV = "the context"
	tilePart     = "the tile"
	overviewPart = "the overview"
)

// The built-in texts of the level prompts, without their Submitting
// heading, which stays baum's.
var (
	v2MapBody      = strings.TrimSuffix(v2Map, stagedSubmitting)
	v2SectionBody  = strings.TrimSuffix(v2Section, stagedSubmitting)
	v2ContextBody  = strings.TrimSuffix(v2Context, stagedSubmitting)
	v2TileBody     = strings.TrimSuffix(v2Tile, stagedSubmitting)
	v2OverviewBody = strings.TrimSuffix(v2Overview, stagedSubmitting)
	v2Natives      = map[string]string{
		view.TypeFlow: v2TileFlow, view.TypeColumns: v2TileColumns,
		view.TypeSequence: v2TileSequence, view.TypeTable: v2TileTable,
	}
)

// V2Prompts implements agent.ReviewV2Prompts with this module's public
// baum.review/2 prompts.
type V2Prompts struct {
	// Overrides replaces built-in texts; V2Prompts uses the entries of
	// V2ReviewIDs. Callers must Validate the text first; composition does
	// not.
	Overrides Overrides
}

var _ agent.ReviewV2Prompts = V2Prompts{}

// ReviewV2Instructions returns the baum.review/2 prompts for a backend: the
// shared text, and each level's text with the backend's stage submit block
// appended under a Submitting heading, naming what the call returns. The
// native tile types' parts are not replaceable. Digest is the sha256 of
// every text, each followed by a NUL byte, the natives in type order.
func (p V2Prompts) ReviewV2Instructions(_ context.Context, backend string) (agent.ReviewV2Prompt, error) {
	var submit string
	switch backend {
	case agent.BackendClaude, agent.BackendAnthropic:
		submit = submitStageClaude
	case agent.BackendCodex:
		submit = submitStageCodex
	case agent.BackendCursorAgent:
		submit = submitStageCursor
	default:
		return agent.ReviewV2Prompt{}, fmt.Errorf("no public baum.review/2 prompt for backend %q", backend)
	}
	block := func(part string) string { return stagedSubmitting + "\n" + strings.ReplaceAll(submit, "{part}", part) }
	shared, c0 := p.Overrides.text(IDReviewV2Shared, v2Shared)
	mapText, c1 := p.Overrides.text(IDReviewMap, v2MapBody)
	section, c2 := p.Overrides.text(IDReviewSection, v2SectionBody)
	contextText, c3 := p.Overrides.text(IDReviewContextV2, v2ContextBody)
	tile, c4 := p.Overrides.text(IDReviewTile, v2TileBody)
	overview, c5 := p.Overrides.text(IDReviewOverview, v2OverviewBody)
	out := agent.ReviewV2Prompt{
		Version: withCustom(V2Version, c0 || c1 || c2 || c3 || c4 || c5), Shared: shared,
		Map: mapText + block(mapPart), Section: section + block(sectionPart), Context: contextText + block(contextPartV),
		Tile: tile + block(tilePart), Overview: overview + block(overviewPart), Natives: map[string]string{},
	}
	for kind, text := range v2Natives {
		out.Natives[kind] = text
	}
	out.Digest = V2Digest(out)
	return out, nil
}

// V2Digest is the digest of a set of baum.review/2 texts: the sha256 of
// each text followed by a NUL byte, the shared text and the levels first,
// then the native parts in type order. A host that changes a text (such as
// the marker it puts first) computes it again.
func V2Digest(p agent.ReviewV2Prompt) string {
	digest := sha256.New()
	for _, text := range []string{p.Shared, p.Map, p.Section, p.Context, p.Tile, p.Overview} {
		digest.Write([]byte(text))
		digest.Write([]byte{0})
	}
	kinds := make([]string, 0, len(p.Natives))
	for kind := range p.Natives {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		digest.Write([]byte(kind + "\x00" + p.Natives[kind]))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

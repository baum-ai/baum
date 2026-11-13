package tiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	reviewschema "github.com/baum-ai/baum/schema"
)

// Options configure FromReview.
type Options struct {
	// Draft marks a live draft: a missing context is still being written, so
	// the context tile is pending rather than absent.
	Draft bool
}

// These mirror baum.review/1 where the converter splits it. Shapes it passes
// through stay raw. Decoding rejects a field the review schema does not
// define: the stored schema is closed, so such a document fails it, and a
// field the converter does not know would otherwise be lost.

type reviewDocument struct {
	Schema          string          `json:"schema"`
	Review          json.RawMessage `json:"review"`
	Summary         reviewSummary   `json:"summary"`
	Stats           json.RawMessage `json:"stats"`
	Stack           json.RawMessage `json:"stack,omitempty"`
	Sections        []reviewSection `json:"sections"`
	AgentReviews    json.RawMessage `json:"agent_reviews"`
	UnassignedHunks []string        `json:"unassigned_hunks"`
	Index           json.RawMessage `json:"index"`
}

type reviewSummary struct {
	Title          string          `json:"title"`
	Text           string          `json:"text"`
	Context        json.RawMessage `json:"context,omitempty"`
	ReviewExchange json.RawMessage `json:"review_exchange,omitempty"`
	ChangeKind     string          `json:"change_kind"`
	Implements     json.RawMessage `json:"implements"`
	ReleaseNote    json.RawMessage `json:"release_note,omitempty"`
	BreakingChange json.RawMessage `json:"breaking_change,omitempty"`
	ReviewMinutes  json.RawMessage `json:"review_minutes,omitempty"`
	Attention      json.RawMessage `json:"attention,omitempty"`
	Recommendation json.RawMessage `json:"recommendation,omitempty"`
	Author         json.RawMessage `json:"author"`
	Authorship     json.RawMessage `json:"authorship"`
	RiskScore      json.RawMessage `json:"risk_score,omitempty"`
}

type reviewSection struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Kind         string            `json:"kind"`
	Hunks        []string          `json:"hunks"`
	PullRequests []int             `json:"pull_requests,omitempty"`
	Summary      json.RawMessage   `json:"summary,omitempty"`
	Attention    json.RawMessage   `json:"attention"`
	Risks        []json.RawMessage `json:"risks"`
	Evidence     []json.RawMessage `json:"evidence"`
	Blocks       []json.RawMessage `json:"blocks"`
	Derived      json.RawMessage   `json:"derived"`
	Assessment   json.RawMessage   `json:"assessment,omitempty"`
	Details      string            `json:"details,omitempty"`
	// Tiles is the retired per-section tile list, which the converter drops.
	Tiles json.RawMessage `json:"tiles,omitempty"`
}

type reviewRisk struct {
	ID                string          `json:"id"`
	Tier              string          `json:"tier"`
	Category          string          `json:"category"`
	Source            string          `json:"source"`
	Text              string          `json:"text"`
	Anchors           json.RawMessage `json:"anchors"`
	Finding           json.RawMessage `json:"finding,omitempty"`
	State             string          `json:"state"`
	IntroducedVersion int             `json:"introduced_version"`
	MitigatedVersion  *int            `json:"mitigated_version"`
	// Tiles is the retired per-risk tile list, which the converter drops.
	Tiles json.RawMessage `json:"tiles,omitempty"`
}

type reviewEvidence struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Claim       string          `json:"claim"`
	Status      string          `json:"status"`
	Granularity *string         `json:"granularity,omitempty"`
	Source      string          `json:"source"`
	Anchors     json.RawMessage `json:"anchors"`
	Detail      *string         `json:"detail,omitempty"`
	Expects     *string         `json:"expects,omitempty"`
}

type intentBlock struct {
	Kind    string          `json:"kind"`
	Text    string          `json:"text"`
	Anchors json.RawMessage `json:"anchors"`
}

type decisionsBlock struct {
	Kind  string            `json:"kind"`
	Items []json.RawMessage `json:"items"`
}

// The block kinds a current change's tile does not hold: the aggregate tiles
// (intent, decisions, media) hold them.
const (
	blockIntent    = "intent"
	blockDecisions = "decisions"
	blockImages    = "images"
	blockMedia     = "media"
)

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// section is a review section as the converter works on it.
type section struct {
	reviewSection
	index   int
	derived derivedView
	risks   []reviewRisk
	ids     struct{ risks, evidence []string }
	refs    sectionRefs
}

func (s *section) current() bool { return s.derived.State != stateStale }

// FromReview converts a stored baum.review/1 document, as review.NormalizeStored
// returns it, into a tile document. The conversion is deterministic and loses
// nothing: ToReview of the result is JSON-equal to the input without the
// retired per-section and per-risk tiles. It fails only when the input is not
// a baum.review/1 document it can split: invalid JSON, another schema, a field
// the review schema does not define (matched by its exact name) or a repeated
// section ID.
func FromReview(normalized []byte, opts Options) (*Document, error) {
	var source reviewDocument
	if err := decodeStrict(normalized, &source); err != nil {
		return nil, fmt.Errorf("tiles: decoding the review: %w", err)
	}
	if err := checkKeyCase(normalized); err != nil {
		return nil, fmt.Errorf("tiles: decoding the review: %w", err)
	}
	if source.Schema != reviewschema.Name {
		return nil, fmt.Errorf("tiles: the review's schema is %q, not %q", source.Schema, reviewschema.Name)
	}
	c := converter{source: &source, raw: normalized, opts: opts}
	if err := c.prepare(); err != nil {
		return nil, err
	}
	return c.convert()
}

type converter struct {
	source   *reviewDocument
	raw      []byte
	opts     Options
	sections []*section
	renamed  renamed
	doc      *Document
	moved    struct {
		lines     []IntentLine
		decisions []json.RawMessage
		media     []MediaItem
	}
}

// prepare decodes the sections, checks the document-level rules the
// conversion relies on, and renames repeated item IDs.
func (c *converter) prepare() error {
	ids := map[string]bool{}
	var riskIDs, evidenceIDs, decisionIDs []string
	for i := range c.source.Sections {
		s := &section{reviewSection: c.source.Sections[i], index: i}
		if ids[s.ID] {
			return fmt.Errorf("tiles: section %d repeats a section ID", i+1)
		}
		ids[s.ID] = true
		if err := json.Unmarshal(s.Derived, &s.derived); err != nil {
			return fmt.Errorf("tiles: section %d: derived: %w", i+1, err)
		}
		s.risks = make([]reviewRisk, len(s.Risks))
		for j, raw := range s.Risks {
			if err := decodeStrict(raw, &s.risks[j]); err != nil {
				return fmt.Errorf("tiles: section %d, risk %d: %w", i+1, j+1, err)
			}
			s.ids.risks = append(s.ids.risks, s.risks[j].ID)
		}
		for j, raw := range s.Evidence {
			var item struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("tiles: section %d, evidence %d: %w", i+1, j+1, err)
			}
			s.ids.evidence = append(s.ids.evidence, item.ID)
		}
		riskIDs = append(riskIDs, s.ids.risks...)
		evidenceIDs = append(evidenceIDs, s.ids.evidence...)
		decisions, err := decisionIDsOf(s.Blocks)
		if !s.current() {
			decisions = nil
		}
		if err != nil {
			return fmt.Errorf("tiles: section %d: %w", i+1, err)
		}
		decisionIDs = append(decisionIDs, decisions...)
		c.sections = append(c.sections, s)
	}
	return c.rename(riskIDs, evidenceIDs, decisionIDs)
}

func decisionIDsOf(blocks []json.RawMessage) ([]string, error) {
	var ids []string
	for _, raw := range blocks {
		block, err := decodeDecisions(raw)
		if err != nil || block == nil {
			return nil, err
		}
		for _, item := range block.Items {
			var decision struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(item, &decision); err != nil {
				return nil, err
			}
			ids = append(ids, decision.ID)
		}
	}
	return ids, nil
}

// blockKind returns a block's kind.
func blockKind(raw json.RawMessage) (string, error) {
	var block struct {
		Kind string `json:"kind"`
	}
	err := json.Unmarshal(raw, &block)
	return block.Kind, err
}

// decodeDecisions returns a decisions block, or nil for a block of another
// kind.
func decodeDecisions(raw json.RawMessage) (*decisionsBlock, error) {
	kind, err := blockKind(raw)
	if err != nil || kind != blockDecisions {
		return nil, err
	}
	var block decisionsBlock
	if err := decodeStrict(raw, &block); err != nil {
		return nil, fmt.Errorf("decisions block: %w", err)
	}
	return &block, nil
}

// renamed holds the IDs the converter gives repeated items, by section and
// position.
type renamed struct {
	risks, evidence [][]string
	decisions       []map[[2]int]string
}

func (c *converter) rename(riskIDs, evidenceIDs, decisionIDs []string) error {
	source := &sourceText{raw: c.raw}
	risks := newRenamer(source, riskIDs)
	evidence := newRenamer(source, evidenceIDs)
	decisions := newRenamer(source, decisionIDs)
	c.renamed.risks = make([][]string, len(c.sections))
	c.renamed.evidence = make([][]string, len(c.sections))
	c.renamed.decisions = make([]map[[2]int]string, len(c.sections))
	for i, s := range c.sections {
		s.refs = sectionRefs{}
		seen := map[string]bool{}
		for _, id := range s.ids.risks {
			next := risks.next(id, s.ID)
			c.renamed.risks[i] = append(c.renamed.risks[i], next)
			s.refs.add(id, next, seen)
		}
		for _, id := range s.ids.evidence {
			next := evidence.next(id, s.ID)
			c.renamed.evidence[i] = append(c.renamed.evidence[i], next)
			s.refs.add(id, next, seen)
		}
		c.renamed.decisions[i] = map[[2]int]string{}
		for b, raw := range s.Blocks {
			block, err := decodeDecisions(raw)
			if err != nil {
				return err
			}
			if block == nil {
				continue
			}
			for j, item := range block.Items {
				var decision struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(item, &decision); err != nil {
					return err
				}
				next := decision.ID
				if s.current() {
					next = decisions.next(decision.ID, s.ID)
				}
				if next != decision.ID {
					c.renamed.decisions[i][[2]int{b, j}] = next
				}
				s.refs.add(decision.ID, next, seen)
			}
		}
		if len(s.refs) > 0 {
			if err := s.rewriteRefs(); err != nil {
				return fmt.Errorf("tiles: section %d: rewriting references: %w", i+1, err)
			}
		}
	}
	return nil
}

// rewriteRefs rewrites the section's references to its renamed items, and
// decodes its risks again from the rewritten text.
func (s *section) rewriteRefs() error {
	for _, field := range []*json.RawMessage{&s.Summary, &s.Attention} {
		if len(*field) == 0 {
			continue
		}
		rewritten, err := s.refs.rewrite(*field)
		if err != nil {
			return err
		}
		*field = rewritten
	}
	s.Risks, s.Evidence, s.Blocks = slices.Clone(s.Risks), slices.Clone(s.Evidence), slices.Clone(s.Blocks)
	for _, values := range [][]json.RawMessage{s.Risks, s.Evidence, s.Blocks} {
		if err := s.refs.rewriteAll(values); err != nil {
			return err
		}
	}
	for j, raw := range s.Risks {
		s.risks[j] = reviewRisk{}
		if err := decodeStrict(raw, &s.risks[j]); err != nil {
			return err
		}
	}
	return nil
}

func (c *converter) convert() (*Document, error) {
	src := c.source
	var header struct {
		Generator struct {
			SchemaRevision string `json:"schema_revision"`
		} `json:"generator"`
	}
	if err := json.Unmarshal(src.Review, &header); err != nil {
		return nil, fmt.Errorf("tiles: decoding the review header: %w", err)
	}
	revision := header.Generator.SchemaRevision
	c.doc = &Document{
		Schema: Name,
		Origin: Origin{Kind: OriginConverted, From: reviewschema.Name, SchemaRevision: &revision, Converter: ConverterVersion},
		Review: src.Review,
		Facts: Facts{
			Title:          src.Summary.Title,
			Author:         src.Summary.Author,
			Authorship:     src.Summary.Authorship,
			ReviewExchange: src.Summary.ReviewExchange,
			ReviewMinutes:  src.Summary.ReviewMinutes,
		},
		Stack:           src.Stack,
		Stats:           src.Stats,
		Index:           src.Index,
		AgentReviews:    src.AgentReviews,
		UnassignedHunks: nonNil(src.UnassignedHunks),
		Recommendation:  src.Summary.Recommendation,
		RiskScore:       src.Summary.RiskScore,
		Changes:         []Change{},
		Risks:           []Risk{},
		Evidence:        []Evidence{},
	}
	if err := c.substrate(); err != nil {
		return nil, err
	}
	tiles, entries, err := c.tiles()
	if err != nil {
		return nil, err
	}
	c.doc.Tiles = tiles
	c.doc.Layout = Layout{Entries: entries, Groups: groupsOf(entries)}
	Arrange(c.doc)
	return c.doc, nil
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func (c *converter) substrate() error {
	for i, s := range c.sections {
		c.doc.Changes = append(c.doc.Changes, Change{
			ID:           s.ID,
			Title:        s.Title,
			Kind:         s.Kind,
			Hunks:        nonNil(s.Hunks),
			Assessment:   s.Assessment,
			PullRequests: s.PullRequests,
			Details:      s.Details,
			Derived:      s.Derived,
		})
		for j, risk := range s.risks {
			converted := Risk{
				ID:                c.renamed.risks[i][j],
				Tier:              risk.Tier,
				Category:          risk.Category,
				Source:            risk.Source,
				Text:              risk.Text,
				Anchors:           risk.Anchors,
				Finding:           risk.Finding,
				State:             risk.State,
				IntroducedVersion: risk.IntroducedVersion,
				MitigatedVersion:  risk.MitigatedVersion,
				Change:            s.ID,
			}
			if converted.ID != risk.ID {
				converted.Origin = &RenameOrigin{Change: s.ID, ID: risk.ID}
			}
			c.doc.Risks = append(c.doc.Risks, converted)
		}
		for j, raw := range s.Evidence {
			var item reviewEvidence
			if err := decodeStrict(raw, &item); err != nil {
				return fmt.Errorf("tiles: section %d, evidence %d: %w", i+1, j+1, err)
			}
			converted := Evidence{
				ID:          c.renamed.evidence[i][j],
				Kind:        item.Kind,
				Claim:       item.Claim,
				Status:      item.Status,
				Granularity: item.Granularity,
				Source:      item.Source,
				Anchors:     item.Anchors,
				Detail:      item.Detail,
				Expects:     item.Expects,
				Change:      s.ID,
			}
			if converted.ID != item.ID {
				converted.Origin = &RenameOrigin{Change: s.ID, ID: item.ID}
			}
			c.doc.Evidence = append(c.doc.Evidence, converted)
		}
	}
	return nil
}

// split returns the blocks a current change's own tile keeps, and moves the
// rest into the aggregate tiles' items.
func (c *converter) split(i int, s *section) ([]json.RawMessage, error) {
	kept := []json.RawMessage{}
	for b, raw := range s.Blocks {
		kind, err := blockKind(raw)
		if err != nil {
			return nil, fmt.Errorf("tiles: section %d, block %d: %w", i+1, b+1, err)
		}
		switch kind {
		case blockIntent:
			var block intentBlock
			if err := decodeStrict(raw, &block); err != nil {
				return nil, fmt.Errorf("tiles: section %d, block %d: %w", i+1, b+1, err)
			}
			c.moved.lines = append(c.moved.lines, IntentLine{Change: s.ID, Text: block.Text, Anchors: block.Anchors, Origin: &BlockOrigin{Change: s.ID, Block: b}})
		case blockDecisions:
			block, err := decodeDecisions(raw)
			if err != nil {
				return nil, fmt.Errorf("tiles: section %d, block %d: %w", i+1, b+1, err)
			}
			for j, item := range block.Items {
				origin := BlockOrigin{Change: s.ID, Block: b, Item: &j}
				if renamed, ok := c.renamed.decisions[i][[2]int{b, j}]; ok {
					var original struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(item, &original); err != nil {
						return nil, err
					}
					origin.ID = original.ID
					item, err = withField(item, "id", renamed)
					if err != nil {
						return nil, err
					}
				}
				if item, err = withField(item, "change", s.ID); err != nil {
					return nil, err
				}
				if item, err = withField(item, "origin", origin); err != nil {
					return nil, err
				}
				c.moved.decisions = append(c.moved.decisions, item)
			}
		case blockImages, blockMedia:
			c.moved.media = append(c.moved.media, MediaItem{Change: s.ID, Block: raw, Origin: &BlockOrigin{Change: s.ID, Block: b}})
		default:
			kept = append(kept, raw)
		}
	}
	return kept, nil
}

// withField returns a JSON object with one field set. A decision item the
// converter adds "change" or "origin" to must not have the field already.
func withField(object json.RawMessage, key string, value any) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields[key]; ok && key != "id" {
		return nil, fmt.Errorf("a decision item already has %q", key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[key] = encoded
	return json.Marshal(fields)
}

func (c *converter) tiles() ([]Tile, []Entry, error) {
	src := c.source
	var tiles []Tile
	var entries []Entry
	place := func(tile Tile, size Size, group string) {
		tiles = append(tiles, tile)
		entries = append(entries, Entry{Tile: tile.ID, Size: size, Group: group})
	}

	overview, err := json.Marshal(OverviewData{
		Text:           src.Summary.Text,
		ChangeKind:     src.Summary.ChangeKind,
		ReleaseNote:    src.Summary.ReleaseNote,
		BreakingChange: src.Summary.BreakingChange,
		Attention:      src.Summary.Attention,
	})
	if err != nil {
		return nil, nil, err
	}
	place(Tile{ID: TypeOverview, Type: TypeOverview, Title: tileTitle(src.Summary.Title, TypeOverview), Headline: headline(src.Summary.Text), Data: overview, State: StateReady}, SizeHalf, "")

	order := reviewOrder(c.sections)
	ranked := c.rankedRisks(order)
	risks, err := json.Marshal(RisksData{Ranked: ranked})
	if err != nil {
		return nil, nil, err
	}
	place(Tile{ID: TypeRisks, Type: TypeRisks, Title: "Risks", Headline: risksHeadline(c.doc.Risks, ranked), Data: risks, State: StateReady}, SizeHalf, "")

	var mechanical *section
	changeTiles := map[string]Tile{}
	for i, s := range c.sections {
		if !s.current() {
			continue
		}
		blocks, err := c.split(i, s)
		if err != nil {
			return nil, nil, err
		}
		// The schema allows one mechanical section; should a review hold
		// more, the first is everything else and the others are shown as
		// changes.
		if s.Kind == KindMechanical && mechanical == nil {
			mechanical = s
			s.Blocks = blocks
			continue
		}
		tile, err := changeTile(s, blocks)
		if err != nil {
			return nil, nil, err
		}
		changeTiles[s.ID] = tile
	}
	for _, s := range order {
		if tile, ok := changeTiles[s.ID]; ok {
			place(tile, SizeThird, GroupChanges)
		}
	}

	place(Tile{ID: TypeEvidence, Type: TypeEvidence, Title: "Evidence", Headline: evidenceHeadline(c.doc.Evidence), Data: json.RawMessage(`{}`), State: StateReady}, SizeThird, "")

	if len(c.moved.media) > 0 {
		data, err := json.Marshal(MediaData{Items: c.moved.media})
		if err != nil {
			return nil, nil, err
		}
		place(Tile{ID: TypeMedia, Type: TypeMedia, Title: "Media", Headline: countHeadline(len(c.moved.media), "media block", "media blocks"), Data: data, State: StateReady}, SizeThird, "")
	}

	var implements []json.RawMessage
	if err := json.Unmarshal(src.Summary.Implements, &implements); err != nil {
		return nil, nil, fmt.Errorf("tiles: summary.implements: %w", err)
	}
	if implements == nil {
		return nil, nil, errors.New("tiles: summary.implements is not an array")
	}
	if len(implements) > 0 || len(c.moved.lines) > 0 {
		data, err := json.Marshal(IntentData{Implements: src.Summary.Implements, Lines: nonNil(c.moved.lines)})
		if err != nil {
			return nil, nil, err
		}
		place(Tile{ID: TypeIntent, Type: TypeIntent, Title: "Intent", Data: data, State: StateReady}, SizeThird, "")
	}

	switch {
	case len(src.Summary.Context) > 0 && !bytes.Equal(src.Summary.Context, []byte("null")):
		var context struct {
			Area struct {
				Text string `json:"text"`
			} `json:"area"`
		}
		if err := json.Unmarshal(src.Summary.Context, &context); err != nil {
			return nil, nil, fmt.Errorf("tiles: summary.context: %w", err)
		}
		place(Tile{ID: TypeContext, Type: TypeContext, Title: "Context", Headline: headline(context.Area.Text), Data: src.Summary.Context, State: StateReady}, SizeThird, "")
	case len(src.Summary.Context) > 0:
		return nil, nil, errors.New("tiles: summary.context is null")
	case c.opts.Draft:
		place(Tile{ID: TypeContext, Type: TypeContext, Title: "Context", Data: json.RawMessage(`{}`), State: StatePending}, SizeThird, "")
	}

	if len(c.moved.decisions) > 0 {
		data, err := json.Marshal(DecisionsData{Items: c.moved.decisions})
		if err != nil {
			return nil, nil, err
		}
		place(Tile{ID: TypeDecisions, Type: TypeDecisions, Title: "Decisions", Headline: countHeadline(len(c.moved.decisions), "decision", "decisions"), Data: data, State: StateReady}, SizeThird, "")
	}

	if mechanical != nil || len(src.UnassignedHunks) > 0 {
		tile, err := everythingElseTile(mechanical, nonNil(src.UnassignedHunks))
		if err != nil {
			return nil, nil, err
		}
		place(tile, SizeThird, "")
	}

	for _, s := range c.sections {
		if s.current() {
			continue
		}
		tile, err := changeTile(s, nonNil(s.Blocks))
		if err != nil {
			return nil, nil, err
		}
		tile.Covers = nil
		place(tile, SizeThird, GroupObsolete)
	}
	return nonNil(tiles), nonNil(entries), nil
}

func changeTile(s *section, blocks []json.RawMessage) (Tile, error) {
	data, err := json.Marshal(ChangeData{Change: s.ID, Summary: s.Summary, Attention: s.Attention, Blocks: blocks})
	if err != nil {
		return Tile{}, err
	}
	return Tile{
		ID:       s.ID,
		Type:     TypeChange,
		Title:    tileTitle(s.Title, TypeChange),
		Headline: summaryHeadline(s.Summary),
		Data:     data,
		Covers:   nonNil(s.Hunks),
		State:    detailsState(s),
	}, nil
}

func everythingElseTile(mechanical *section, unassigned []string) (Tile, error) {
	data := EverythingElseData{Unassigned: unassigned}
	tile := Tile{ID: TypeEverythingElse, Type: TypeEverythingElse, Title: "Everything else", State: StateReady}
	var covers []string
	if mechanical != nil {
		data.Change = &mechanical.ID
		data.Summary = mechanical.Summary
		data.Attention = mechanical.Attention
		blocks := nonNil(mechanical.Blocks)
		data.Blocks = &blocks
		covers = append(covers, mechanical.Hunks...)
		tile.Headline = summaryHeadline(mechanical.Summary)
		tile.State = detailsState(mechanical)
	}
	seen := map[string]bool{}
	for _, hunk := range covers {
		seen[hunk] = true
	}
	for _, hunk := range unassigned {
		if !seen[hunk] {
			seen[hunk] = true
			covers = append(covers, hunk)
		}
	}
	tile.Covers = covers
	if tile.Headline == "" {
		tile.Headline = countHeadline(len(unassigned), "unassigned hunk", "unassigned hunks")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Tile{}, err
	}
	tile.Data = encoded
	return tile, nil
}

// detailsState is the state of the tile of a change: pending while its
// details are being written, failed when they could not be. An obsolete
// change still marked pending is failed, since nothing will write it.
func detailsState(s *section) string {
	switch s.Details {
	case StatePending:
		if s.current() {
			return StatePending
		}
		return StateFailed
	case StateFailed:
		return StateFailed
	}
	return StateReady
}

// The Changes group shows every change up to ChangesShownAll; with more it
// shows ChangesShown, two rows of thirds, and folds the rest behind its own
// control, which then always holds at least three.
const (
	ChangesShown    = 6
	ChangesShownAll = 8
)

func groupsOf(entries []Entry) []Group {
	groups := []Group{}
	present := map[string]bool{}
	count := map[string]int{}
	for _, entry := range entries {
		present[entry.Group] = true
		count[entry.Group]++
	}
	if present[GroupChanges] {
		changes := Group{ID: GroupChanges, Title: "Changes"}
		if count[GroupChanges] > ChangesShownAll {
			changes.CollapseAfter = new(ChangesShown)
		}
		groups = append(groups, changes)
	}
	if present[GroupObsolete] {
		groups = append(groups, Group{ID: GroupObsolete, Title: "Obsolete changes", CollapseAfter: new(0)})
	}
	return groups
}

// reviewOrder returns the sections in the order a reviewer reads them, as the
// viewer's sectionsInReviewOrder does: by review score, highest first, a
// section with attention first within a score, otherwise in document order;
// mechanical, unscored and obsolete sections follow. A document whose
// sections carry no score keeps its order.
func reviewOrder(sections []*section) []*section {
	scored := slices.ContainsFunc(sections, func(s *section) bool { return s.derived.ReviewScore != nil })
	ordered := slices.Clone(sections)
	if !scored {
		return ordered
	}
	rank := func(s *section) int {
		switch {
		case !s.current():
			return 8
		case s.Kind == KindMechanical:
			return 6
		case s.derived.ReviewScore == nil:
			return 7
		}
		return 5 - *s.derived.ReviewScore
	}
	attention := func(s *section) int {
		if len(s.Attention) == 0 || bytes.Equal(s.Attention, []byte("null")) {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(ordered, func(a, b *section) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return attention(b) - attention(a)
	})
	return ordered
}

var tierRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "mechanical": 4}

// rankedRisks returns the IDs of the active risks of current changes by tier,
// most severe first, then by their change's review order, then in document
// order.
func (c *converter) rankedRisks(order []*section) []string {
	position := map[string]int{}
	current := map[string]bool{}
	for i, s := range order {
		position[s.ID] = i
		current[s.ID] = s.current()
	}
	type candidate struct {
		risk  Risk
		index int
	}
	var candidates []candidate
	for i, risk := range c.doc.Risks {
		if risk.State == "active" && current[risk.Change] {
			candidates = append(candidates, candidate{risk, i})
		}
	}
	rankOf := func(tier string) int {
		if rank, ok := tierRank[tier]; ok {
			return rank
		}
		return len(tierRank)
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		if d := rankOf(a.risk.Tier) - rankOf(b.risk.Tier); d != 0 {
			return d
		}
		if d := position[a.risk.Change] - position[b.risk.Change]; d != 0 {
			return d
		}
		return a.index - b.index
	})
	ranked := make([]string, len(candidates))
	for i, candidate := range candidates {
		ranked[i] = candidate.risk.ID
	}
	return ranked
}

const (
	maxTitle    = 160
	maxHeadline = 300
)

// tileTitle is a tile's title: the text, cut to the schema's bound, or the
// type's catalogue title when it is blank.
func tileTitle(text, typeName string) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if text == "" {
		builtin, _ := Builtin(typeName)
		return builtin.Title
	}
	return truncate(text, maxTitle)
}

// headline is the first sentence of a text on one line, as the app's
// firstSentence takes it (oss/app/src/pending-review-model.ts): up to the
// first sentence end followed by a space, without the final punctuation, and
// cut at the first "; ".
func headline(text string) string {
	text = strings.TrimSpace(text)
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = strings.TrimSpace(line)
	}
	if text == "" {
		return ""
	}
	sentence := text
	for i, r := range text {
		if r != '.' && r != '!' && r != '?' || i == 0 {
			continue
		}
		next := i + utf8.RuneLen(r)
		if next == len(text) || text[next] == ' ' || text[next] == '\t' {
			sentence = text[:next]
			break
		}
	}
	sentence = strings.TrimRight(sentence, ".!?")
	if clause := strings.Index(sentence, "; "); clause > 0 {
		sentence = sentence[:clause]
	}
	return truncate(strings.TrimSpace(sentence), maxHeadline)
}

func summaryHeadline(summary json.RawMessage) string {
	if len(summary) == 0 {
		return ""
	}
	var value struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(summary, &value) != nil {
		return ""
	}
	return headline(value.Text)
}

func countHeadline(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func risksHeadline(risks []Risk, ranked []string) string {
	if len(ranked) == 0 {
		return "No open risks"
	}
	tiers := map[string]string{}
	for _, risk := range risks {
		tiers[risk.ID] = risk.Tier
	}
	severe := 0
	for _, id := range ranked {
		if tier := tiers[id]; tier == "critical" || tier == "high" {
			severe++
		}
	}
	text := countHeadline(len(ranked), "open risk", "open risks")
	if severe > 0 {
		text += ", " + strconv.Itoa(severe) + " high or critical"
	}
	return text
}

func evidenceHeadline(evidence []Evidence) string {
	if len(evidence) == 0 {
		return "No evidence"
	}
	missing := 0
	for _, item := range evidence {
		if item.Status == "missing" {
			missing++
		}
	}
	text := countHeadline(len(evidence), "item", "items")
	if missing > 0 {
		text += ", " + strconv.Itoa(missing) + " missing"
	}
	return text
}

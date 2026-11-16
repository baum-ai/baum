// Package tiles holds the baum.tiles/1 tile document: its types, a validator,
// the catalogue of built-in tile types, the contract of custom tile types
// (TypeDefinition, the data schema dialect CheckDataSchema checks, the digest
// and citations) and a lossless conversion from and to a stored
// baum.review/1 document.
//
// A tile document is a substrate, the parts of a review that are scored,
// refreshed and cited (the review header, host facts, the change outline,
// risks and evidence, in the same shapes as baum.review/1), plus tiles that
// present it and a layout that orders them. Shapes the review schema defines
// stay raw JSON here, so a document passes through without losing a field.
package tiles

import (
	"encoding/json"

	"github.com/baum-ai/baum/schema"
)

// Document is a baum.tiles/1 document.
type Document struct {
	Schema string `json:"schema"`
	Origin Origin `json:"origin"`
	// Review is baum.review/1's review header.
	Review json.RawMessage `json:"review"`
	Facts  Facts           `json:"facts"`
	// Stack is present only on a stack review.
	Stack           json.RawMessage `json:"stack,omitempty"`
	Stats           json.RawMessage `json:"stats"`
	Index           json.RawMessage `json:"index"`
	AgentReviews    json.RawMessage `json:"agent_reviews"`
	UnassignedHunks []string        `json:"unassigned_hunks"`
	// Recommendation is absent on reviews written before it existed and null
	// when the review gives none; both are kept as they were.
	Recommendation json.RawMessage `json:"recommendation,omitempty"`
	RiskScore      json.RawMessage `json:"risk_score,omitempty"`
	Changes        []Change        `json:"changes"`
	Risks          []Risk          `json:"risks"`
	Evidence       []Evidence      `json:"evidence"`
	Tiles          []Tile          `json:"tiles"`
	Layout         Layout          `json:"layout"`
}

// Origin says where a document came from: converted from a stored
// baum.review/1 document, or generated as tiles.
type Origin struct {
	Kind string `json:"kind"`
	// From, SchemaRevision and Converter are set on a converted document:
	// the source schema, the revision its document was validated against
	// and the converter version.
	From           string  `json:"from,omitempty"`
	SchemaRevision *string `json:"schema_revision,omitempty"`
	Converter      int     `json:"converter,omitempty"`
}

// The origin kinds.
const (
	OriginConverted = "converted"
	OriginGenerated = "generated"
)

// ConverterVersion is the version of FromReview recorded in a converted
// document's origin.
const ConverterVersion = 1

// Facts are the host's facts about the pull request.
type Facts struct {
	Title      string          `json:"title"`
	Author     json.RawMessage `json:"author"`
	Authorship json.RawMessage `json:"authorship"`
	// ReviewExchange is an object, or a plain string on a review stored
	// before it was an object.
	ReviewExchange json.RawMessage `json:"review_exchange,omitempty"`
	ReviewMinutes  json.RawMessage `json:"review_minutes,omitempty"`
}

// Change is a section's outline. Its ID is a section ID and the ID of the
// tile that presents it.
type Change struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	Hunks      []string        `json:"hunks"`
	Assessment json.RawMessage `json:"assessment,omitempty"`
	// PullRequests names the stack members the change covers.
	PullRequests []int `json:"pull_requests,omitempty"`
	// Details is "pending" or "failed" on a change whose details are missing.
	Details string          `json:"details,omitempty"`
	Derived json.RawMessage `json:"derived"`
}

// The change kinds.
const (
	KindLogical    = "logical"
	KindMechanical = "mechanical"
)

// Risk is a baum.review/1 risk with the change it belongs to.
type Risk struct {
	ID       string          `json:"id"`
	Tier     string          `json:"tier"`
	Category string          `json:"category"`
	Source   string          `json:"source"`
	Text     string          `json:"text"`
	Anchors  json.RawMessage `json:"anchors"`
	// Finding is absent, null or a finding ID, as it was stored.
	Finding           json.RawMessage `json:"finding,omitempty"`
	State             string          `json:"state"`
	IntroducedVersion int             `json:"introduced_version"`
	MitigatedVersion  *int            `json:"mitigated_version"`
	Change            string          `json:"change"`
	// Origin is set when the risk's ID was not unique across the document
	// and the risk was renamed.
	Origin *RenameOrigin `json:"origin,omitempty"`
}

// Evidence is a baum.review/1 evidence item with the change it belongs to.
type Evidence struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Claim       string          `json:"claim"`
	Status      string          `json:"status"`
	Granularity *string         `json:"granularity,omitempty"`
	Source      string          `json:"source"`
	Anchors     json.RawMessage `json:"anchors"`
	Detail      *string         `json:"detail,omitempty"`
	Expects     *string         `json:"expects,omitempty"`
	Change      string          `json:"change"`
	// Origin is set when the item's ID was not unique across the document
	// and the item was renamed.
	Origin *RenameOrigin `json:"origin,omitempty"`
}

// RiskIn returns the risk a reference that names a change and a risk ID
// there means, or nil. References kept from baum.review/1 with the original
// ID resolve this way: the risk score's reasons (risk_score.reasons[].risk
// with .section) and a change's derived review reason
// (changes[].derived.review_reason.risk). A renamed risk matches by its
// origin; any other by its ID and change.
func (d *Document) RiskIn(change, id string) *Risk {
	for i := range d.Risks {
		risk := &d.Risks[i]
		if risk.Origin != nil {
			if *risk.Origin == (RenameOrigin{Change: change, ID: id}) {
				return risk
			}
			continue
		}
		if risk.ID == id && risk.Change == change {
			return risk
		}
	}
	return nil
}

// RenameOrigin records a renamed item's change and its ID there.
type RenameOrigin struct {
	Change string `json:"change"`
	ID     string `json:"id"`
}

// BlockOrigin records where a block, or an item of one, that an aggregate
// tile holds came from: the change, the block's position among the change's
// blocks, the item's position in the block and, for a renamed item, its ID
// in the change.
type BlockOrigin struct {
	Change string `json:"change"`
	Block  int    `json:"block"`
	Item   *int   `json:"item,omitempty"`
	ID     string `json:"id,omitempty"`
}

// Tile presents part of the substrate.
type Tile struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	// Headline is one plain line the host derives.
	Headline string `json:"headline,omitempty"`
	// Data has the shape of the type's $defs/tileData_<type>.
	Data json.RawMessage `json:"data"`
	// Covers lists the hunks the tile presents.
	Covers   []string `json:"covers,omitempty"`
	State    string   `json:"state"`
	Problems []string `json:"problems,omitempty"`
	// Definition is the embedded definition of a custom tile.
	Definition json.RawMessage `json:"definition,omitempty"`
}

// The tile states.
const (
	StateReady       = "ready"
	StatePending     = "pending"
	StateFailed      = "failed"
	StateUnavailable = "unavailable"
)

// Layout orders the tiles.
type Layout struct {
	// Entries places every tile once, in reading order.
	Entries []Entry `json:"entries"`
	Groups  []Group `json:"groups"`
	// Landing is the number of leading entries on the landing page (Landing).
	Landing int `json:"landing"`
	// ExtraRows, host-derived, is how many rows the landing page grows by
	// for the composition's tiles (ExtraRows), at most MaxExtraRows.
	ExtraRows int `json:"extra_rows,omitempty"`
}

// Entry places a tile.
type Entry struct {
	Tile  string `json:"tile"`
	Size  Size   `json:"size"`
	Group string `json:"group,omitempty"`
	Pin   bool   `json:"pin,omitempty"`
}

// Size is a tile's width on the landing grid.
type Size string

// The tile sizes: the whole grid, half of it and a third of it.
const (
	SizeFull  Size = "full"
	SizeHalf  Size = "half"
	SizeThird Size = "third"
)

// Group is a run of contiguous entries under a heading. A group with
// CollapseAfter shows that many members until it is expanded.
type Group struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	CollapseAfter *int   `json:"collapse_after,omitempty"`
}

// The groups the default layout uses.
const (
	GroupChanges  = "changes"
	GroupObsolete = "obsolete"
	// GroupHidden holds, last and collapsed, the tiles a reader hid from a
	// generated review (CompositionLayout.Hidden).
	GroupHidden = "hidden"
)

// OverviewData is the data of an overview tile.
type OverviewData struct {
	Text           string          `json:"text"`
	ChangeKind     string          `json:"change_kind"`
	ReleaseNote    json.RawMessage `json:"release_note,omitempty"`
	BreakingChange json.RawMessage `json:"breaking_change,omitempty"`
	Attention      json.RawMessage `json:"attention,omitempty"`
}

// ChangeData is the data of a change tile: the change's details. Summary is
// absent or null on a change without its details.
type ChangeData struct {
	Change    string            `json:"change"`
	Summary   json.RawMessage   `json:"summary,omitempty"`
	Attention json.RawMessage   `json:"attention"`
	Blocks    []json.RawMessage `json:"blocks"`
}

// RisksData is the data of the risks tile.
type RisksData struct {
	Ranked []string `json:"ranked"`
}

// IntentData is the data of the intent tile.
type IntentData struct {
	Implements json.RawMessage `json:"implements"`
	Lines      []IntentLine    `json:"lines"`
}

// IntentLine is an intent block of a change, or a line of its own.
type IntentLine struct {
	Change  string          `json:"change,omitempty"`
	Text    string          `json:"text"`
	Anchors json.RawMessage `json:"anchors"`
	Origin  *BlockOrigin    `json:"origin,omitempty"`
}

// DecisionsData is the data of the decisions tile. Each item is a
// baum.review/1 decision item with "change" and "origin" added.
type DecisionsData struct {
	Items []json.RawMessage `json:"items"`
}

// MediaData is the data of the media tile.
type MediaData struct {
	Items []MediaItem `json:"items"`
}

// MediaItem is an images or media block of a change.
type MediaItem struct {
	Change string          `json:"change,omitempty"`
	Block  json.RawMessage `json:"block"`
	Origin *BlockOrigin    `json:"origin,omitempty"`
}

// EverythingElseData is the data of the everything_else tile: the current
// mechanical change, if any, with its details, and the unassigned hunks.
type EverythingElseData struct {
	Change     *string            `json:"change"`
	Unassigned []string           `json:"unassigned"`
	Summary    json.RawMessage    `json:"summary,omitempty"`
	Attention  json.RawMessage    `json:"attention,omitempty"`
	Blocks     *[]json.RawMessage `json:"blocks,omitempty"`
}

// Name is the value of a tile document's "schema" field.
const Name = schema.TilesName

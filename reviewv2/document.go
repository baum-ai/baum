// Package reviewv2 is the baum.review/2 document: the desktop app's review,
// shaped like its page (an overview, the sections with their steps, risks
// and tiles, and where the change fits). The model writes only judgement:
// explanations, groupings, risks, diagrams and choices. The host fills
// everything it can fetch or compute: the review header, the index of hunks,
// sessions, prompts, attachments and checks, the stats, the risk score, the
// reading time and each section's state across versions.
//
// A document is written in three levels (see oss/agent/orchestrate): the top
// level maps the pull request into sections and plans the tiles, then, once
// the sections and tiles are written, writes the overview; one section agent
// per section writes its summary, steps and risks; one tile agent per planned
// tile fills it. Fields say which level writes them.
//
// The hosted app keeps baum.review/1. A stored baum.review/1 version is
// converted to this shape when it is served to the desktop app (FromV1).
package reviewv2

import (
	"encoding/json"
	"slices"
)

// Name is the value of a document's "schema" field.
const Name = "baum.review/2"

// Document is a baum.review/2 document.
type Document struct {
	Schema string `json:"schema"`
	// Review is the host's header, the same object as baum.review/1's
	// "review" (repository, pull request, head, base, merge base, version,
	// generation state, generator, sources, relations).
	Review json.RawMessage `json:"review"`
	// Summary keeps the paths the summaries table and the lists read
	// (summary.title, summary.risk_score, summary.review_minutes).
	Summary  Summary   `json:"summary"`
	Overview Overview  `json:"overview"`
	Context  *Context  `json:"context"`
	Sections []Section `json:"sections"`
	// Tiles are every tile of the document, placed by their owner: the
	// overview's main diagram, a section, or the context.
	Tiles []Tile `json:"tiles"`
	// Stats and Index are the host's, the same objects as baum.review/1's.
	Stats json.RawMessage `json:"stats"`
	Index json.RawMessage `json:"index"`
	// Generation records how the levels went, for the page's states and for
	// replay: which calls ran, failed or were skipped. Host-written.
	Generation *Generation `json:"generation,omitempty"`
}

// Summary is the document's standing facts.
type Summary struct {
	// Title is the pull request's title. Host.
	Title string `json:"title"`
	// Text is the overview's lede, two or three plain sentences. Top level,
	// overview call.
	Text string `json:"text"`
	// ChangeKind is the kind of change (feature, fix, refactor, …), the same
	// values as baum.review/1. Top level, map call.
	ChangeKind string `json:"change_kind"`
	// Verdict replaces baum.review/1's recommendation. Top level, overview
	// call. The page shows a banner only when Points is not empty.
	Verdict *Verdict `json:"verdict"`
	// RiskWhy justifies the score in at most two sentences. Top level,
	// overview call.
	RiskWhy string `json:"risk_why"`
	// RiskScore is the host's, computed with rubric risk-v1 from the
	// sections' levels and the risks' tiers; the same object as
	// baum.review/1's summary.risk_score.
	RiskScore json.RawMessage `json:"risk_score"`
	// ReviewMinutes is the host's reading-time estimate.
	ReviewMinutes *int `json:"review_minutes"`
}

// Verdict is what the review recommends.
type Verdict struct {
	// Decision is approve, look_first or request_changes.
	Decision string `json:"decision"`
	// Text is one line saying what to do.
	Text string `json:"text"`
	// Points are what must happen before merging, each one plain sentence,
	// at most 3; empty for most reviews.
	Points []VerdictPoint `json:"points"`
}

// VerdictPoint is one thing that blocks the merge.
type VerdictPoint struct {
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// Overview is the overview tab's model-chosen parts. Top level, overview
// call, except Diagram's tile, which a tile agent fills.
type Overview struct {
	// Screenshots are image attachment ids from the index, at most 3.
	Screenshots []string `json:"screenshots"`
	// Videos are video clip attachment ids from the index, at most 3. A
	// document from before them has none.
	Videos []string `json:"videos,omitempty"`
	// Diagram is the id of the main diagram tile (owner "overview"), or
	// null.
	Diagram *string `json:"diagram"`
	// Intent is what the linked sessions asked for that this pull request
	// set out to do, at most 3.
	Intent []Intent `json:"intent"`
}

// Intent is one ask from a linked session and what the pull request does
// about it.
type Intent struct {
	Session string `json:"session"`
	Prompt  int    `json:"prompt"`
	Asked   string `json:"asked"`
	Got     string `json:"got"`
}

// Context is "Where this fits". A context agent (section level) writes it.
type Context struct {
	Area      ContextPart `json:"area"`
	ReachedBy ContextPart `json:"reached_by"`
	Contracts ContextPart `json:"contracts"`
	Related   ContextPart `json:"related"`
}

// ContextPart is one or two plain sentences and the files they rest on.
type ContextPart struct {
	Text  string   `json:"text"`
	Files []string `json:"files"`
}

// Section is one part of the change a reviewer can understand and approve
// on its own.
type Section struct {
	// ID (sec_…), Title (two to five words, a subject), Kind (logical or
	// mechanical), Hunks, Gist (one line: what the reader should come away
	// with), Scope (where it ends when it shares hunks; optional), Level
	// (1-5) and LevelReasons: top level, map call.
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Kind         string        `json:"kind"`
	Hunks        []string      `json:"hunks"`
	Gist         string        `json:"gist"`
	Scope        string        `json:"scope,omitempty"`
	Level        *int          `json:"level"`
	LevelReasons []LevelReason `json:"level_reasons"`
	// Body (the blocks the page draws between the gist and the risks, in
	// order) and Risks: section agent. The page owns the frame around the
	// body (title, gist, views, risks); the body is the model's to compose.
	Body  []Block `json:"body"`
	Risks []Risk  `json:"risks"`
	// Details is "pending" while the section agent writes, "failed" when it
	// did not finish, "" once written. Host.
	Details string `json:"details,omitempty"`
	// Derived is the host's: state across versions, review score and the
	// rest, the same object as baum.review/1's section derived.
	Derived json.RawMessage `json:"derived,omitempty"`
}

// LevelReason says why a section has its level, citing what it rests on.
type LevelReason struct {
	Text  string   `json:"text"`
	Cites []Anchor `json:"cites"`
}

// The block types of a section's body. A tile block places one of the
// document's tiles (Tile, by ID): a diagram, a table, an excerpt, a library
// tile; the others are text.
const (
	BlockProse  = "prose"
	BlockPoints = "points"
	BlockNote   = "note"
	BlockTile   = "tile"
)

// NoteKinds are what a note can be, each drawn with its own icon: a caveat
// (an exception or a limit to watch), a decision (a choice made on purpose),
// what stays unchanged, and a follow-up (work left for later). A note
// without a kind is a plain aside.
var NoteKinds = []string{"caveat", "decision", "unchanged", "follow_up"}

// Limits of the text blocks, in characters. The host cuts what runs over.
const (
	MaxBlocks      = 6
	MaxBlockTitle  = 60
	MaxProse       = 450
	MaxNote        = 240
	MaxPoints      = 8
	MaxPoint       = 180
	MaxPointDetail = 450
)

// Block is one block of a section's body. Text (prose, note) and the
// points' text are inline markdown: bold and code only. Title is an
// optional label above the block; a tile block's title is the tile's own.
// Kind (optional, one of NoteKinds) is a note's.
type Block struct {
	Type  string  `json:"type"`
	Title string  `json:"title,omitempty"`
	Kind  string  `json:"kind,omitempty"`
	Text  string  `json:"text,omitempty"`
	Items []Point `json:"items,omitempty"`
	Tile  string  `json:"tile,omitempty"`
}

// Point is one line of a points block: Text on the page, More (optional)
// behind a click with the files it cites. Tone (optional, one of
// PointTones) marks the line as new, good, bad, muted or missing.
type Point struct {
	Text  string   `json:"text"`
	More  string   `json:"more,omitempty"`
	Tone  string   `json:"tone,omitempty"`
	Cites []Anchor `json:"cites"`
}

// Risk is what could go wrong in a section. Tier and category take
// baum.review/1's values. State, IntroducedVersion and MitigatedVersion are
// the host's, across versions.
type Risk struct {
	ID                string   `json:"id"`
	Tier              string   `json:"tier"`
	Category          string   `json:"category"`
	Text              string   `json:"text"`
	Cites             []Anchor `json:"cites"`
	State             string   `json:"state,omitempty"`
	IntroducedVersion *int     `json:"introduced_version,omitempty"`
	MitigatedVersion  *int     `json:"mitigated_version,omitempty"`
}

// Anchor is a citation, the same object as baum.review/1's anchor (hunk,
// file, prompt, session_event, attachment, …).
type Anchor = json.RawMessage

// Tile is one tile of the document.
type Tile struct {
	// ID (tl_…), Type, Owner ("overview", "context" or a section id) and
	// Shows (what the tile is to show, the brief its tile agent fills): top
	// level, map call; a section agent may sharpen Shows and Cites before
	// the tile is filled.
	ID    string `json:"id"`
	Type  string `json:"type"`
	Owner string `json:"owner"`
	Shows string `json:"shows"`
	// Title, Caption and Data (the type's data shape): tile agent. Cites
	// are what the tile rests on.
	Title   string          `json:"title"`
	Caption string          `json:"caption,omitempty"`
	Cites   []Anchor        `json:"cites"`
	Data    json.RawMessage `json:"data,omitempty"`
	// State is pending, ready, failed or unavailable. Host.
	State string `json:"state"`
	// Definition is the embedded definition of a library or saved type,
	// drawn in a sandboxed frame; absent for a native type. Host.
	Definition json.RawMessage `json:"definition,omitempty"`
	Problems   []string        `json:"problems,omitempty"`
	// Added, From and Filling are set only when the tile is served: Added
	// marks a tile the person added to the pull request (internal/
	// addedtiles), which they can remove; From is the earlier version its
	// data was written for, and Filling says it is being filled for this
	// one. Never stored.
	Added   bool `json:"added,omitempty"`
	From    int  `json:"from,omitempty"`
	Filling bool `json:"filling,omitempty"`
}

// CodeData is a code tile's data: the model names the hunk and, optionally,
// its first and last line on the new side and the few lines of them that
// matter (Focus, new-side line numbers, which the page highlights); the
// host fills Text, the new side's lines from the diff, and Lines when the
// model gave none (Complete).
type CodeData struct {
	Hunk  string `json:"hunk"`
	Lines []int  `json:"lines,omitempty"`
	Focus []int  `json:"focus,omitempty"`
	Text  string `json:"text,omitempty"`
}

// FocusWithin is focus kept to the lines from first to last, each once, in
// order; nil when none is left.
func FocusWithin(focus []int, first, last int) []int {
	var out []int
	for _, line := range focus {
		if line >= first && line <= last && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return out
}

// TypeImages is a tile of images a person added to their review page
// (internal/addedtiles): screenshots the host stored, drawn by the page's
// own image layout. A review generation never writes one.
const TypeImages = "images"

// HunkIDs are the ids of the hunks anchors cite, in order.
func HunkIDs(anchors []Anchor) []string {
	var out []string
	for _, raw := range anchors {
		var a struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(raw, &a) == nil && a.Type == "hunk" && a.ID != "" {
			out = append(out, a.ID)
		}
	}
	return out
}

// ImagesData is an images tile's data: each image by the id the host
// stored it under (an attachment id the attachment route serves for the
// pull request), with its caption, type and size.
type ImagesData struct {
	Items []ImageItem `json:"items"`
}

// ImageItem is one image of an images tile.
type ImageItem struct {
	Attachment  string `json:"attachment"`
	Caption     string `json:"caption,omitempty"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// Tile states.
const (
	TilePending     = "pending"
	TileReady       = "ready"
	TileFailed      = "failed"
	TileUnavailable = "unavailable"
)

// Owners other than a section.
const (
	OwnerOverview = "overview"
	OwnerContext  = "context"
)

// Generation records the levels' calls. Host.
type Generation struct {
	// Map, Overview and Context are the top-level and context calls; each
	// section and tile has its own entry by id.
	Map      CallRecord            `json:"map"`
	Overview CallRecord            `json:"overview"`
	Context  CallRecord            `json:"context"`
	Sections map[string]CallRecord `json:"sections,omitempty"`
	Tiles    map[string]CallRecord `json:"tiles,omitempty"`
}

// CallRecord is how one call went: ran, failed, skipped (no budget left or
// nothing to do) or reused (unchanged since the previous version).
type CallRecord struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ReadyTypes are the names of the saved (library or added) types a stored
// baum.review/2 document holds a ready tile of, each once: the tiles that
// embed a definition. A document of another schema, or one that does not
// parse, holds none.
func ReadyTypes(document []byte) []string {
	var head struct {
		Schema string `json:"schema"`
		Tiles  []Tile `json:"tiles"`
	}
	if json.Unmarshal(document, &head) != nil || head.Schema != Name {
		return nil
	}
	var names []string
	for _, t := range head.Tiles {
		var def struct {
			Name string `json:"name"`
		}
		if t.State != TileReady || len(t.Definition) == 0 || json.Unmarshal(t.Definition, &def) != nil || def.Name == "" {
			continue
		}
		if !slices.Contains(names, def.Name) {
			names = append(names, def.Name)
		}
	}
	return names
}

package view

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NativeTile is a tile the view call wrote, drawn by the app's own
// components. Data has its type's shape (FlowData, ColumnsData, SequenceData,
// TableData).
type NativeTile struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}

// The native tile types.
const (
	TypeFlow     = "flow"
	TypeColumns  = "columns"
	TypeSequence = "sequence"
	TypeTable    = "table"
)

// NativeTypes are the native tile types, in the order the prompt lists them.
var NativeTypes = []string{TypeFlow, TypeColumns, TypeSequence, TypeTable}

// Tones say how a part of a native tile reads; empty is plain.
var Tones = []string{"plain", "new", "good", "bad", "muted", "missing"}

// Joins are the connectors a flow step may have before it; empty is arrow.
var Joins = []string{"arrow", "both", "none"}

// Anchor cites a hunk of the pull request.
type Anchor struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Lines []int  `json:"lines,omitempty"`
}

// FlowData is a flow: lanes of steps joined by arrows. One lane without a
// label is a plain flow.
type FlowData struct {
	Caption string   `json:"caption,omitempty"`
	Source  []Anchor `json:"source"`
	Lanes   []Lane   `json:"lanes"`
}

// Lane is a row of a flow.
type Lane struct {
	Label string     `json:"label,omitempty"`
	Steps []FlowStep `json:"steps"`
}

// FlowStep is a box of a flow. Join is the connector drawn before it,
// ignored on a lane's first step.
type FlowStep struct {
	Title string `json:"title"`
	Note  string `json:"note,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Join  string `json:"join,omitempty"`
}

// ColumnsData is two or three labelled lists side by side (before and
// after, can and can't).
type ColumnsData struct {
	Caption string   `json:"caption,omitempty"`
	Source  []Anchor `json:"source"`
	Columns []Column `json:"columns"`
}

// Column is one list of a columns tile.
type Column struct {
	Label string       `json:"label"`
	Tone  string       `json:"tone,omitempty"`
	Items []ColumnItem `json:"items"`
}

// ColumnItem is one entry of a column.
type ColumnItem struct {
	Title string `json:"title"`
	Note  string `json:"note,omitempty"`
}

// SequenceData is a sequence diagram: actors and the messages between them,
// in order. From and To index Actors; From == To is a note on that actor.
type SequenceData struct {
	Caption string         `json:"caption,omitempty"`
	Source  []Anchor       `json:"source"`
	Actors  []string       `json:"actors"`
	Steps   []SequenceStep `json:"steps"`
}

// SequenceStep is one message of a sequence.
type SequenceStep struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// TableData is a table; every row is as long as Columns.
type TableData struct {
	Caption string     `json:"caption,omitempty"`
	Source  []Anchor   `json:"source"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// The limits of the native tiles.
const (
	MaxLanes        = 4
	MaxLaneSteps    = 6
	MaxLaneLabel    = 24
	MaxStepTitle    = 60
	MaxNote         = 80
	MinColumns      = 2
	MaxColumns      = 3
	MaxColumnItems  = 8
	MaxColumnLabel  = 40
	MaxItemTitle    = 80
	MinActors       = 2
	MaxActors       = 4
	MaxActor        = 24
	MaxSequence     = 10
	MaxSequenceText = 80
	MinTableColumns = 2
	MaxTableColumns = 5
	MaxTableRows    = 12
	MaxTableHeading = 40
	MaxCell         = 120
)

var (
	nativeID    = regexp.MustCompile(`^dg_[a-z0-9_]{1,60}$`)
	hunkMention = regexp.MustCompile(`\bh_[0-9a-f]{12}\b`)
	mdLink      = regexp.MustCompile(`\[([^\]\n]+)\]\([^)\s]+\)`)
	mdHeading   = regexp.MustCompile(`^#{1,6}\s+`)
)

// nativeCheck checks one native tile's text and data, as the normaliser
// does: it says why the tile is dropped, or returns it with its text
// cleaned and cut and its anchors kept to the review's hunks.
type nativeCheck struct {
	review *Review
	report *Report
	// problem is the first reason the tile is dropped.
	problem string
}

func (n *nativeCheck) fail(format string, args ...any) {
	if n.problem == "" {
		n.problem = fmt.Sprintf(format, args...)
	}
}

// text cleans a text of the tile: required text that is empty, or any text
// that names a hunk, fails the tile; text over its limit is cut.
func (n *nativeCheck) text(s string, limit int, required bool, what string) string {
	out, problem := n.report.text(s, limit)
	switch {
	case problem != "":
		n.fail("%s %s", what, problem)
	case required && out == "":
		n.fail("%s is empty", what)
	}
	return out
}

func (n *nativeCheck) count(got, lo, hi int, what string) {
	if got < lo || got > hi {
		n.fail("it has %d %s, not %d to %d", got, what, lo, hi)
	}
}

func oneOf(value string, allowed []string) string {
	if value == "plain" || value == "arrow" || !slices.Contains(allowed, value) {
		return ""
	}
	return value
}

// anchors keeps the hunk anchors that cite a hunk of the review, once each,
// at most MaxSource; each anchor dropped is counted.
func (n *nativeCheck) anchors(in []Anchor) []Anchor {
	out := []Anchor{}
	seen := map[string]bool{}
	for _, a := range in {
		if a.Type != "hunk" || !n.review.HasHunk(a.ID) || seen[a.ID] || len(out) == MaxSource {
			n.report.fix(FixAnchor, "an anchor cites no hunk of this pull request")
			continue
		}
		seen[a.ID] = true
		if len(a.Lines) != 2 || a.Lines[0] < 1 || a.Lines[1] < a.Lines[0] {
			a.Lines = nil
		}
		out = append(out, Anchor{Type: "hunk", ID: a.ID, Lines: a.Lines})
	}
	return out
}

// check returns the tile checked, or false with the problem when it is
// dropped.
func (n *nativeCheck) check(t NativeTile) (NativeTile, bool) {
	if !nativeID.MatchString(t.ID) {
		n.fail("its ID is not dg_ followed by a slug")
		return t, false
	}
	t.Title = n.text(t.Title, MaxTitle, true, "its title")
	var data any
	switch t.Type {
	case TypeFlow:
		data = n.flow(t.Data)
	case TypeColumns:
		data = n.columns(t.Data)
	case TypeSequence:
		data = n.sequence(t.Data)
	case TypeTable:
		data = n.table(t.Data)
	default:
		n.fail("its type is not one of %s", strings.Join(NativeTypes, ", "))
	}
	if n.problem != "" {
		return t, false
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		n.fail("its data does not encode")
		return t, false
	}
	t.Data = encoded
	return t, true
}

func (n *nativeCheck) decode(raw json.RawMessage, into any) bool {
	if err := json.Unmarshal(raw, into); err != nil {
		n.fail("its data does not have its type's shape")
		return false
	}
	return true
}

func (n *nativeCheck) flow(raw json.RawMessage) any {
	var d FlowData
	if !n.decode(raw, &d) {
		return nil
	}
	d.Caption = n.text(d.Caption, MaxCaption, false, "its caption")
	d.Source = n.anchors(d.Source)
	n.count(len(d.Lanes), 1, MaxLanes, "lanes")
	for i := range d.Lanes {
		lane := &d.Lanes[i]
		lane.Label = n.text(lane.Label, MaxLaneLabel, false, "a lane's label")
		n.count(len(lane.Steps), 1, MaxLaneSteps, "steps in a lane")
		for j := range lane.Steps {
			step := &lane.Steps[j]
			step.Title = n.text(step.Title, MaxStepTitle, true, "a step's title")
			step.Note = n.text(step.Note, MaxNote, false, "a step's note")
			step.Tone, step.Join = oneOf(step.Tone, Tones), oneOf(step.Join, Joins)
			if j == 0 {
				step.Join = ""
			}
		}
	}
	return d
}

func (n *nativeCheck) columns(raw json.RawMessage) any {
	var d ColumnsData
	if !n.decode(raw, &d) {
		return nil
	}
	d.Caption = n.text(d.Caption, MaxCaption, false, "its caption")
	d.Source = n.anchors(d.Source)
	n.count(len(d.Columns), MinColumns, MaxColumns, "columns")
	for i := range d.Columns {
		c := &d.Columns[i]
		c.Label = n.text(c.Label, MaxColumnLabel, true, "a column's label")
		c.Tone = oneOf(c.Tone, Tones)
		n.count(len(c.Items), 1, MaxColumnItems, "items in a column")
		for j := range c.Items {
			c.Items[j].Title = n.text(c.Items[j].Title, MaxItemTitle, true, "an item's title")
			c.Items[j].Note = n.text(c.Items[j].Note, MaxNote, false, "an item's note")
		}
	}
	return d
}

func (n *nativeCheck) sequence(raw json.RawMessage) any {
	var d SequenceData
	if !n.decode(raw, &d) {
		return nil
	}
	d.Caption = n.text(d.Caption, MaxCaption, false, "its caption")
	d.Source = n.anchors(d.Source)
	n.count(len(d.Actors), MinActors, MaxActors, "actors")
	n.count(len(d.Steps), 1, MaxSequence, "steps")
	for i := range d.Actors {
		d.Actors[i] = n.text(d.Actors[i], MaxActor, true, "an actor")
	}
	for i := range d.Steps {
		s := &d.Steps[i]
		if s.From < 0 || s.To < 0 || s.From >= len(d.Actors) || s.To >= len(d.Actors) {
			n.fail("a step names an actor it does not have")
		}
		s.Text = n.text(s.Text, MaxSequenceText, true, "a step's text")
		s.Tone = oneOf(s.Tone, Tones)
	}
	return d
}

func (n *nativeCheck) table(raw json.RawMessage) any {
	var d TableData
	if !n.decode(raw, &d) {
		return nil
	}
	d.Caption = n.text(d.Caption, MaxCaption, false, "its caption")
	d.Source = n.anchors(d.Source)
	n.count(len(d.Columns), MinTableColumns, MaxTableColumns, "columns")
	for i := range d.Columns {
		d.Columns[i] = n.text(d.Columns[i], MaxTableHeading, true, "a column heading")
	}
	rows := [][]string{}
	for _, row := range d.Rows {
		if len(row) != len(d.Columns) {
			n.report.fix(FixNativeItem, "a table row is not as long as its columns")
			continue
		}
		for j := range row {
			row[j] = n.text(row[j], MaxCell, false, "a cell")
		}
		rows = append(rows, row)
	}
	if len(rows) > MaxTableRows {
		n.report.fix(FixNativeItem, "a table has more rows than it may")
		rows = rows[:MaxTableRows]
	}
	d.Rows = rows
	n.count(len(d.Rows), 1, MaxTableRows, "rows")
	return d
}

// clean makes text one plain line: control and zero-width characters
// removed, whitespace folded, and markdown other than `code` spans taken
// out (emphasis markers, links to their text, a heading's hashes).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r), r == 0x200b, r == 0x200c, r == 0x200d, r == 0x2060, r == 0xfeff:
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	if !strings.Contains(s, "`") {
		s = strings.NewReplacer("**", "", "__", "").Replace(s)
	} else {
		// Emphasis is taken out only outside code spans.
		parts := strings.Split(s, "`")
		for i := 0; i < len(parts); i += 2 {
			parts[i] = strings.NewReplacer("**", "", "__", "").Replace(parts[i])
		}
		s = strings.Join(parts, "`")
	}
	return strings.TrimSpace(s)
}

// cut shortens text to at most limit characters at a word boundary, with
// an ellipsis.
func cut(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)[:limit-1]
	out := string(runes)
	if i := strings.LastIndex(out, " "); i > limit/2 {
		out = out[:i]
	}
	return strings.TrimRight(out, " ,;:.-") + "…"
}

// CheckNative checks a native tile's data of type typ (flow, columns,
// sequence or table) as Normalize checks a diagram: it returns the data
// cleaned (text made plain and cut to its limits, anchors kept to the hunks
// hasHunk knows, rows of the wrong length dropped), the reason the tile
// cannot be shown ("" when it can) and what the check fixed (Report.Counts:
// FixCut for each text cut, FixAnchor and FixNativeItem for each anchor or
// row dropped). A baum.review/2 tile carries its caption and citations
// beside its data; data without them checks the same.
func CheckNative(typ string, data json.RawMessage, hasHunk func(string) bool) (json.RawMessage, string, Report) {
	hunks := map[string]bool{}
	var anchors struct {
		Source []Anchor `json:"source"`
	}
	_ = json.Unmarshal(data, &anchors)
	for _, a := range anchors.Source {
		if hasHunk != nil && hasHunk(a.ID) {
			hunks[a.ID] = true
		}
	}
	report := &Report{}
	check := &nativeCheck{review: &Review{hunks: hunks}, report: report}
	out, ok := check.check(NativeTile{ID: "dg_check", Type: typ, Title: "check", Data: data})
	if !ok {
		return nil, check.problem, *report
	}
	return out.Data, "", *report
}

// Clean makes text one plain line as the native tiles' checks do: control
// and zero-width characters removed, whitespace folded and markdown other
// than `code` spans taken out.
func Clean(s string) string { return clean(s) }

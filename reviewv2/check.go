package reviewv2

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles/view"
)

// The limits of the model's text, which the checks hold every level to.
// Counts are characters (runes); a sentence ends at ".", "!" or "?".
const (
	MaxTitle            = 60
	MaxTitleWords       = 6
	MaxGist             = 140
	MaxScope            = 200
	MaxSummary          = 450
	MaxSummarySentences = 3
	MinSteps            = 2
	MaxSteps            = 5
	MaxStepTitle        = 60
	MaxStepText         = 220
	MaxRiskText         = 300
	MaxLevelReasons     = 3
	MaxLevelReason      = 220
	MaxContextPart      = 320
	MaxContextSentences = 2
	MaxContextFiles     = 8
	MaxVerdictText      = 120
	MaxVerdictPoints    = 3
	MaxPointText        = 200
	MaxRiskWhy          = 260
	MaxRiskWhySentences = 2
	MaxIntent           = 3
	MaxIntentText       = 160
	MaxScreenshots      = 3
	MaxTiles            = 8
	MaxShows            = 300
	MaxTileTitle        = 80
	MaxCaption          = 200
	MaxCites            = 20
)

// The native tile types: drawn by the app, with the data shapes of
// tiles/view (flow, columns, sequence, table) and the code excerpt the host
// fills.
const (
	TypeFlow     = view.TypeFlow
	TypeColumns  = view.TypeColumns
	TypeSequence = view.TypeSequence
	TypeTable    = view.TypeTable
	TypeCode     = "code"
)

// NativeTypes are the native tile types.
var NativeTypes = []string{TypeFlow, TypeColumns, TypeSequence, TypeTable, TypeCode}

// The section kinds.
const (
	KindLogical    = "logical"
	KindMechanical = "mechanical"
)

// The details states of a section.
const (
	DetailsPending = "pending"
	DetailsFailed  = "failed"
)

// The verdict decisions.
var Decisions = []string{"approve", "look_first", "request_changes"}

// ChangeKinds are the values of summary.change_kind, baum.review/1's.
var ChangeKinds = []string{"feature", "bugfix", "refactor", "dependency", "cherry_pick", "upstream_sync", "mechanical", "docs", "ci", "translation", "release", "export"}

// RiskTiers are the tiers a model gives a risk.
var RiskTiers = []string{"critical", "high", "medium", "low"}

var (
	sectionID   = regexp.MustCompile(`^sec_[a-z0-9_]+$`)
	stepID      = regexp.MustCompile(`^st_[a-z0-9_]+$`)
	riskID      = regexp.MustCompile(`^rsk_[a-z0-9_]+$`)
	tileID      = regexp.MustCompile(`^tl_[a-z0-9_]{1,60}$`)
	typeName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	category    = regexp.MustCompile(`^[a-z0-9_]+$`)
	evidence    = regexp.MustCompile(`(?i)\bevidence\b`)
	hunkMention = regexp.MustCompile(`\bh_[0-9a-f]{12}\b`)
	codeSpan    = regexp.MustCompile("`[^`]*`")
	abbrev      = regexp.MustCompile(`(?i)\b(e\.g|i\.e|etc|vs|cf)\.`)
	sentenceEnd = regexp.MustCompile(`[.!?]+["')\]]*(\s|$)`)
)

// findings collects the problems of a part, each saying where it is.
type findings struct {
	list []string
}

func (f *findings) add(where, format string, args ...any) {
	f.list = append(f.list, where+": "+fmt.Sprintf(format, args...))
}

// Sentences counts the sentences of a text: code spans and common
// abbreviations are not read, and text without an ending is one sentence.
func Sentences(s string) int {
	s = strings.TrimSpace(abbrev.ReplaceAllString(codeSpan.ReplaceAllString(s, "code"), "x"))
	if s == "" {
		return 0
	}
	n := len(sentenceEnd.FindAllStringIndex(s, -1))
	if trail := strings.TrimRight(s, "\"')]"); trail == "" || !strings.ContainsAny(trail[len(trail)-1:], ".!?") {
		n++
	}
	return n
}

// textRule is how one model text is checked.
type textRule struct {
	limit     int
	sentences int
	required  bool
	// line says the text is one line: no line breaks.
	line bool
}

// text checks one model text: present when required, within its length and
// sentence count, on one line when it must be, naming no hunk by its ID and
// never using the word "evidence".
func (f *findings) text(where, s string, rule textRule) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		if rule.required {
			f.add(where, "it is empty")
		}
		return
	}
	if n := utf8.RuneCountInString(trimmed); rule.limit > 0 && n > rule.limit {
		f.add(where, "it is %d characters; write at most %d", n, rule.limit)
	}
	if n := Sentences(trimmed); rule.sentences > 0 && n > rule.sentences {
		f.add(where, "it has %d sentences; write at most %d", n, rule.sentences)
	}
	if rule.line && strings.ContainsAny(trimmed, "\n\r") {
		f.add(where, "it spans lines; write one line")
	}
	f.plain(where, trimmed)
}

// plain checks the copy rules every model text keeps.
func (f *findings) plain(where, s string) {
	if evidence.MatchString(s) {
		f.add(where, `it uses the word "evidence"; say what was checked or shown instead`)
	}
	if hunkMention.MatchString(s) {
		f.add(where, "it names a hunk by its ID; say what the code does instead")
	}
}

// title checks a title: a plain subject of a few words, never two joined
// by a comma.
func (f *findings) title(where, s string, limit int) {
	f.text(where, s, textRule{limit: limit, required: true, line: true})
	if words := len(strings.Fields(s)); words > MaxTitleWords {
		f.add(where, "it is %d words; a title is a subject of two to five words", words)
	}
	if strings.Contains(s, ",") || strings.Contains(s, ";") {
		f.add(where, "it joins two things; a title is one subject without a comma")
	}
}

// cites checks citations: at least min, at most MaxCites, each resolving.
func (f *findings) cites(where string, cites []Anchor, minimum int, refs *Refs) {
	if len(cites) < minimum {
		f.add(where, "it cites nothing; cite the hunks it rests on")
	}
	if len(cites) > MaxCites {
		f.add(where, "it has %d citations; cite at most %d", len(cites), MaxCites)
	}
	for _, c := range cites {
		if problem := refs.AnchorProblem(c); problem != "" {
			f.add(where, "%s", problem)
		}
	}
}

// CheckMap checks the map call's output: the change kind, the sections'
// outlines (ID, title, kind, hunks, gist, scope, level and its reasons) and
// the planned tiles (ID, type, owner, what it shows and the hunks it
// cites). Every hunk of the pull request must be in a section, at most one
// section may be mechanical, a tile's owner must be "overview", "context" or
// one of the sections, and only one tile may be the overview's. It returns
// the findings to repair, empty when there are none.
func CheckMap(changeKind string, sections []Section, planned []Tile, refs *Refs) []string {
	f := &findings{}
	if !slices.Contains(ChangeKinds, changeKind) {
		f.add("change_kind", "%q is not one of %s", changeKind, strings.Join(ChangeKinds, ", "))
	}
	if len(sections) == 0 {
		f.add("sections", "there are none; group the hunks into sections")
	}
	ids := map[string]bool{}
	mechanical := 0
	inSection := map[string]bool{}
	for i, s := range sections {
		where := fmt.Sprintf("sections[%d]", i)
		if s.ID != "" {
			where += " (" + s.ID + ")"
		}
		switch {
		case !sectionID.MatchString(s.ID) || len(s.ID) > 128:
			f.add(where, "its id is not sec_ and a lower-case slug")
		case ids[s.ID]:
			f.add(where, "its id repeats another section's")
		}
		ids[s.ID] = true
		f.title(where+".title", s.Title, MaxTitle)
		switch s.Kind {
		case KindMechanical:
			mechanical++
		case KindLogical:
		default:
			f.add(where+".kind", "%q is not logical or mechanical", s.Kind)
		}
		if len(s.Hunks) == 0 {
			f.add(where+".hunks", "it has none; a section holds at least one hunk")
		}
		for _, h := range s.Hunks {
			if !refs.HasHunk(h) {
				f.add(where+".hunks", "%q is not a hunk of this pull request", h)
			}
			inSection[h] = true
		}
		f.text(where+".gist", s.Gist, textRule{limit: MaxGist, required: true, line: true})
		f.text(where+".scope", s.Scope, textRule{limit: MaxScope, sentences: 1, line: true})
		if s.Kind == KindLogical {
			switch {
			case s.Level == nil:
				f.add(where+".level", "it is missing; rate the section 1 to 5")
			case *s.Level < 1 || *s.Level > 5:
				f.add(where+".level", "%d is not 1 to 5", *s.Level)
			}
			if len(s.LevelReasons) == 0 {
				f.add(where+".level_reasons", "there are none; give the strongest reason for the level")
			}
		}
		if s.Kind == KindMechanical && s.Level != nil && *s.Level != 1 {
			f.add(where+".level", "a mechanical section is level 1")
		}
		if len(s.LevelReasons) > MaxLevelReasons {
			f.add(where+".level_reasons", "there are %d; give at most %d", len(s.LevelReasons), MaxLevelReasons)
		}
		for j, r := range s.LevelReasons {
			at := fmt.Sprintf("%s.level_reasons[%d]", where, j)
			f.text(at, r.Text, textRule{limit: MaxLevelReason, sentences: 1, required: true, line: true})
			f.cites(at, r.Cites, 0, refs)
		}
	}
	if mechanical > 1 {
		f.add("sections", "%d are mechanical; put every mechanical hunk in one mechanical section", mechanical)
	}
	var missing []string
	for _, h := range refs.HunkIDs() {
		if !inSection[h] {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		shown := missing[:min(len(missing), 12)]
		more := ""
		if len(missing) > len(shown) {
			more = fmt.Sprintf(" and %d more", len(missing)-len(shown))
		}
		f.add("sections", "hunks %s%s are in no section; put each in the section whose behaviour it serves", strings.Join(shown, ", "), more)
	}
	if len(planned) > MaxTiles {
		f.add("tiles", "there are %d; plan at most %d", len(planned), MaxTiles)
	}
	tileIDs := map[string]bool{}
	overview := 0
	for i, t := range planned {
		where := fmt.Sprintf("tiles[%d]", i)
		if t.ID != "" {
			where += " (" + t.ID + ")"
		}
		switch {
		case !tileID.MatchString(t.ID):
			f.add(where, "its id is not tl_ and a lower-case slug")
		case tileIDs[t.ID]:
			f.add(where, "its id repeats another tile's")
		}
		tileIDs[t.ID] = true
		f.tileType(where, t.Type, refs)
		switch {
		case t.Owner == OwnerOverview:
			overview++
		case t.Owner == OwnerContext:
		case !ids[t.Owner]:
			f.add(where+".owner", "%q is not overview, context or a section's id", t.Owner)
		}
		f.text(where+".shows", t.Shows, textRule{limit: MaxShows, sentences: 2, required: true, line: true})
		f.cites(where+".cites", t.Cites, 0, refs)
	}
	if overview > 1 {
		f.add("tiles", "%d are the overview's; plan at most one main diagram", overview)
	}
	return f.list
}

func (f *findings) tileType(where, typ string, refs *Refs) {
	switch {
	case !typeName.MatchString(typ):
		f.add(where+".type", "%q is not a tile type", typ)
	case slices.Contains(NativeTypes, typ):
	case refs != nil && refs.TileTypes != nil && !refs.TileTypes[typ]:
		f.add(where+".type", "%q is not a tile type this review may use", typ)
	}
}

// CheckSection checks what a section agent wrote of a section: its body and
// risks. A logical section has a body of one to MaxBlocks blocks, each
// within its limits (CheckBody: a text over its limit, a block without text
// or past the limits and a point's citation that does not resolve are
// reported as what the host would fix); a mechanical one may have a body
// of prose only. A section from before bodies, one without a body but with a
// summary and steps, is checked as it was: a summary and two to five steps,
// each citing what it rests on. A section whose details are pending or
// failed has nothing to check.
func CheckSection(s Section, refs *Refs) []string {
	f := &findings{}
	if s.Details != "" {
		return nil
	}
	where := "section"
	if s.ID != "" {
		where = "section " + s.ID
	}
	if len(s.Body) > 0 || (s.Summary == "" && len(s.Steps) == 0) {
		f.body(where+".body", s.Body, refs)
	} else {
		f.legacyText(where, s, refs)
	}
	risks := map[string]bool{}
	for i, r := range s.Risks {
		at := fmt.Sprintf("%s.risks[%d]", where, i)
		switch {
		case !riskID.MatchString(r.ID) || len(r.ID) > 128:
			f.add(at, "its id is not rsk_ and a lower-case slug")
		case risks[r.ID]:
			f.add(at, "its id repeats another risk's")
		}
		risks[r.ID] = true
		if !slices.Contains(RiskTiers, r.Tier) {
			f.add(at+".tier", "%q is not one of %s", r.Tier, strings.Join(RiskTiers, ", "))
		}
		if !category.MatchString(r.Category) || len(r.Category) > 128 {
			f.add(at+".category", "%q is not a lower-case category", r.Category)
		}
		f.text(at+".text", r.Text, textRule{limit: MaxRiskText, sentences: 2, required: true})
		f.cites(at+".cites", r.Cites, 1, refs)
	}
	return f.list
}

// body checks a section's body.
func (f *findings) body(where string, body []Block, refs *Refs) {
	fixed, report := CheckBody(body, refs)
	counts := report.Counts()
	if n := counts[view.FixCut]; n > 0 {
		f.add(where, "%d of its texts are over their limits (prose %d, notes %d, a point %d and its detail %d, titles %d characters)", n, MaxProse, MaxNote, MaxPoint, MaxPointDetail, MaxBlockTitle)
	}
	if n := counts[view.FixLimit]; n > 0 {
		f.add(where, "it has more than %d blocks or %d points in a block", MaxBlocks, MaxPoints)
	}
	if n := counts[view.FixNativeItem]; n > 0 {
		f.add(where, "%d of its blocks or points are empty or of a type the body does not have", n)
	}
	// A point's citations are checked as a step's were: each must resolve.
	for i, b := range body {
		for j, p := range b.Items {
			f.cites(fmt.Sprintf("%s[%d].items[%d].cites", where, i, j), p.Cites, 0, refs)
		}
	}
	if len(fixed) == 0 {
		f.add(where, "it has no blocks; write 1 to %d", MaxBlocks)
		return
	}
	for i, b := range fixed {
		at := fmt.Sprintf("%s[%d]", where, i)
		if n, limit := Sentences(b.Text), map[string]int{BlockProse: 3, BlockNote: 2}[b.Type]; limit > 0 && n > limit {
			f.add(at+".text", "it has %d sentences; write at most %d", n, limit)
		}
		f.plainIf(at+".title", b.Title)
		f.plainIf(at+".text", b.Text)
		for j, p := range b.Items {
			f.plainIf(fmt.Sprintf("%s.items[%d].text", at, j), p.Text)
			f.plainIf(fmt.Sprintf("%s.items[%d].more", at, j), p.More)
		}
	}
}

func (f *findings) plainIf(where, s string) {
	if s != "" {
		f.plain(where, s)
	}
}

// legacyText checks the summary and steps of a section from before bodies.
func (f *findings) legacyText(where string, s Section, refs *Refs) {
	f.text(where+".summary", s.Summary, textRule{limit: MaxSummary, sentences: MaxSummarySentences, required: true})
	switch {
	case s.Kind == KindMechanical && len(s.Steps) > MaxSteps:
		f.add(where+".steps", "there are %d; write at most %d", len(s.Steps), MaxSteps)
	case s.Kind != KindMechanical && (len(s.Steps) < MinSteps || len(s.Steps) > MaxSteps):
		f.add(where+".steps", "there are %d; write %d to %d", len(s.Steps), MinSteps, MaxSteps)
	}
	ids := map[string]bool{}
	for i, step := range s.Steps {
		at := fmt.Sprintf("%s.steps[%d]", where, i)
		switch {
		case !stepID.MatchString(step.ID) || len(step.ID) > 128:
			f.add(at, "its id is not st_ and a lower-case slug")
		case ids[step.ID]:
			f.add(at, "its id repeats another step's")
		}
		ids[step.ID] = true
		f.title(at+".title", step.Title, MaxStepTitle)
		f.text(at+".text", step.Text, textRule{limit: MaxStepText, sentences: 1, required: true, line: true})
		f.cites(at+".cites", step.Cites, 1, refs)
	}
}

// CheckContext checks what the context agent wrote: where the change fits,
// in four parts of at most two sentences each, the area required, each
// with the repository files it rests on.
func CheckContext(c Context, refs *Refs) []string {
	f := &findings{}
	for _, part := range []struct {
		name     string
		part     ContextPart
		required bool
	}{{"area", c.Area, true}, {"reached_by", c.ReachedBy, false}, {"contracts", c.Contracts, false}, {"related", c.Related, false}} {
		where := "context." + part.name
		f.text(where+".text", part.part.Text, textRule{limit: MaxContextPart, sentences: MaxContextSentences, required: part.required})
		if len(part.part.Files) > MaxContextFiles {
			f.add(where+".files", "there are %d; name at most %d", len(part.part.Files), MaxContextFiles)
		}
		for _, file := range part.part.Files {
			if file == "" || strings.HasPrefix(file, "/") || slices.Contains(strings.Split(file, "/"), "..") {
				f.add(where+".files", "%q is not a path in the repository", file)
			}
		}
	}
	return f.list
}

// CheckTile checks what a tile agent wrote of a tile: its title, caption,
// citations and data in its type's shape. A native tile's data is checked
// as tiles/view checks a diagram, every text within its limit; a code tile
// names a hunk of the pull request and, optionally, lines on its new side;
// any other type's data is an object (its own type checks it). A tile that
// is not ready has nothing to check.
func CheckTile(t Tile, refs *Refs) []string {
	f := &findings{}
	if t.State != "" && t.State != TileReady {
		return nil
	}
	where := "tile"
	if t.ID != "" {
		where = "tile " + t.ID
	}
	f.tileType(where, t.Type, refs)
	f.text(where+".title", t.Title, textRule{limit: MaxTileTitle, required: true, line: true})
	f.text(where+".caption", t.Caption, textRule{limit: MaxCaption, sentences: 2, line: true})
	f.cites(where+".cites", t.Cites, 0, refs)
	switch t.Type {
	case TypeFlow, TypeColumns, TypeSequence, TypeTable:
		f.nativeData(where+".data", t.Type, t.Data, refs)
	case TypeCode:
		f.codeData(where+".data", t.Data, refs)
	default:
		var object map[string]json.RawMessage
		if json.Unmarshal(t.Data, &object) != nil || object == nil {
			f.add(where+".data", "it is not an object in its type's shape")
		}
	}
	return f.list
}

func (f *findings) nativeData(where, typ string, data json.RawMessage, refs *Refs) {
	if len(data) == 0 {
		f.add(where, "it is missing")
		return
	}
	_, problem, report := view.CheckNative(typ, data, refs.HasHunk)
	if problem != "" {
		f.add(where, "%s", problem)
		return
	}
	counts := report.Counts()
	if n := counts[view.FixCut]; n > 0 {
		f.add(where, "%d of its texts are over their limits (titles of steps 60 and items 80, notes 80, labels and headings 24 to 40, sequence text 80, cells 120)", n)
	}
	if n := counts[view.FixNativeItem]; n > 0 {
		f.add(where, "%d of its rows are not as long as its columns, or past the limit of rows", n)
	}
	if n := counts[view.FixAnchor]; n > 0 {
		f.add(where, "%d of its source citations name no hunk of this pull request", n)
	}
	var strings []string
	collectStrings(data, &strings)
	for _, s := range strings {
		f.plain(where, s)
	}
}

// codeData checks a code tile's data: the hunk it shows and, optionally,
// the lines of it.
func (f *findings) codeData(where string, data json.RawMessage, refs *Refs) {
	var code CodeData
	if json.Unmarshal(data, &code) != nil || code.Hunk == "" {
		f.add(where, `it is not {"hunk": a hunk's id, "lines": [first, last]}`)
		return
	}
	if problem := refs.AnchorProblem(HunkAnchor(code.Hunk, code.Lines)); problem != "" {
		f.add(where, "%s", problem)
	}
	if code.Lines != nil && len(code.Lines) != 2 {
		f.add(where, "lines is not a first and last line")
	}
}

// collectStrings appends every string of a JSON value.
func collectStrings(raw json.RawMessage, out *[]string) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			*out = append(*out, v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
}

// CheckOverview checks what the overview call wrote: the summary's text
// (two or three sentences), the verdict (its decision, one line and at most
// three points, each naming a section of the document when it names one),
// why the risk is what it is, and the overview's screenshots (attachments
// of the pull request), main diagram (a tile of the document) and intent
// (asks of a linked session's prompts).
func CheckOverview(summary Summary, overview Overview, sections []Section, tiles []Tile, refs *Refs) []string {
	f := &findings{}
	f.text("summary.text", summary.Text, textRule{limit: MaxSummary, sentences: MaxSummarySentences, required: true})
	if v := summary.Verdict; v == nil {
		f.add("verdict", "it is missing; say approve, look_first or request_changes")
	} else {
		if !slices.Contains(Decisions, v.Decision) {
			f.add("verdict.decision", "%q is not one of %s", v.Decision, strings.Join(Decisions, ", "))
		}
		f.text("verdict.text", v.Text, textRule{limit: MaxVerdictText, sentences: 1, required: true, line: true})
		if len(v.Points) > MaxVerdictPoints {
			f.add("verdict.points", "there are %d; give at most %d, only what blocks the merge", len(v.Points), MaxVerdictPoints)
		}
		for i, p := range v.Points {
			at := fmt.Sprintf("verdict.points[%d]", i)
			f.text(at, p.Text, textRule{limit: MaxPointText, sentences: 2, required: true, line: true})
			if p.Section != "" && !slices.ContainsFunc(sections, func(s Section) bool { return s.ID == p.Section }) {
				f.add(at+".section", "%q is not a section of the review", p.Section)
			}
		}
	}
	f.text("risk_why", summary.RiskWhy, textRule{limit: MaxRiskWhy, sentences: MaxRiskWhySentences, required: true})
	if len(overview.Screenshots) > MaxScreenshots {
		f.add("overview.screenshots", "there are %d; choose at most %d", len(overview.Screenshots), MaxScreenshots)
	}
	seen := map[string]bool{}
	for _, id := range overview.Screenshots {
		switch {
		case !refs.Attachments[id]:
			f.add("overview.screenshots", "%q is not an attachment of the pull request", id)
		case seen[id]:
			f.add("overview.screenshots", "%q is chosen twice", id)
		}
		seen[id] = true
	}
	if d := overview.Diagram; d != nil && !slices.ContainsFunc(tiles, func(t Tile) bool { return t.ID == *d }) {
		f.add("overview.diagram", "%q is not a tile of the review", *d)
	}
	if len(overview.Intent) > MaxIntent {
		f.add("overview.intent", "there are %d; give at most %d", len(overview.Intent), MaxIntent)
	}
	for i, in := range overview.Intent {
		at := fmt.Sprintf("overview.intent[%d]", i)
		f.text(at+".asked", in.Asked, textRule{limit: MaxIntentText, required: true, line: true})
		f.text(at+".got", in.Got, textRule{limit: MaxIntentText, required: true, line: true})
		if problem := refs.AnchorProblem(promptAnchor(in.Session, in.Prompt)); problem != "" {
			f.add(at, "%s", problem)
		}
	}
	return f.list
}

func promptAnchor(session string, index int) Anchor {
	encoded, _ := json.Marshal(map[string]any{"type": "prompt", "session": session, "index": index})
	return encoded
}

// CheckDocument checks a whole document: its shape (schema review-2.json)
// and every level's checks (CheckMap, CheckSection, CheckContext,
// CheckTile, CheckOverview), with refs, nil for the document's own index
// (RefsOfIndex). Tile checks skip the tiles a person added. It returns the
// findings, empty when there are none.
func CheckDocument(d Document, refs *Refs) []string {
	if refs == nil {
		refs = RefsOfIndex(d.Index)
	}
	var out []string
	encoded, err := json.Marshal(d)
	if err != nil {
		return []string{"the document does not encode: " + err.Error()}
	}
	if err := schema.ValidateReview2(encoded); err != nil {
		out = append(out, "the document does not have the baum.review/2 shape: "+err.Error())
	}
	var planned []Tile
	for _, t := range d.Tiles {
		if !t.Added {
			planned = append(planned, t)
		}
	}
	out = append(out, CheckMap(d.Summary.ChangeKind, d.Sections, planned, refs)...)
	owner := map[string]string{}
	for _, t := range d.Tiles {
		owner[t.ID] = t.Owner
	}
	for _, s := range d.Sections {
		out = append(out, CheckSection(s, refs)...)
		for _, b := range s.Body {
			if b.Type == BlockTile && owner[b.Tile] != s.ID {
				out = append(out, fmt.Sprintf("section %s.body: tile %q is not a tile of this section", s.ID, b.Tile))
			}
		}
	}
	if d.Context != nil {
		out = append(out, CheckContext(*d.Context, refs)...)
	}
	for _, t := range planned {
		out = append(out, CheckTile(t, refs)...)
	}
	out = append(out, CheckOverview(d.Summary, d.Overview, d.Sections, d.Tiles, refs)...)
	return out
}

// Findings joins findings as repair text: one per line.
func Findings(list []string) string { return strings.Join(list, "\n") }

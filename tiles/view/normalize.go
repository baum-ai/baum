package view

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/baum-ai/baum/tiles"
)

// The fixes the checks make, by kind (Report.Counts), beside the
// tiles.FixKind the library and authored tiles' checks report.
const (
	// FixNative is a native tile dropped: an ID, type, count, actor or text
	// it may not have.
	FixNative tiles.FixKind = "native_invalid"
	// FixNativeItem is part of a native tile dropped: a table row.
	FixNativeItem tiles.FixKind = "native_item"
	// FixAnchor is an anchor dropped: it cites no hunk of the pull request.
	FixAnchor tiles.FixKind = "anchor"
	// FixCut is text cut to its limit.
	FixCut tiles.FixKind = "cut"
	// FixOverview is an overview part dropped: a verdict without points, a
	// point, a screenshot, a diagram or an intent naming nothing the review
	// has, or text that names a hunk.
	FixOverview tiles.FixKind = "overview"
	// FixPlacement is a placement dropped: a section the review does not
	// have, a tile it does not have, or one already placed.
	FixPlacement tiles.FixKind = "placement"
	// FixLimit is an item past its limit dropped.
	FixLimit tiles.FixKind = "limit"
	// FixPlaced is a tile the host placed by the hunks it covers (Place).
	FixPlaced tiles.FixKind = "placed"
	// FixUnusable is the library and authored tiles dropped: the review does
	// not convert to tiles.
	FixUnusable tiles.FixKind = tiles.FixUnusable
)

// Report counts what the checks fixed, by kind, and keeps the first few
// reasons, in the host's words: they never quote the composition.
type Report struct {
	counts   map[tiles.FixKind]int
	Problems []string
}

// maxProblems bounds the reasons a report keeps.
const maxProblems = 20

func (r *Report) fix(kind tiles.FixKind, problem string) {
	if r.counts == nil {
		r.counts = map[tiles.FixKind]int{}
	}
	r.counts[kind]++
	if problem != "" && len(r.Problems) < maxProblems {
		r.Problems = append(r.Problems, problem)
	}
}

// Fix counts one fix of a kind, with the reason in the host's words (or "").
func (r *Report) Fix(kind tiles.FixKind, problem string) { r.fix(kind, problem) }

func (r *Report) add(counts map[tiles.FixKind]int) {
	for kind, n := range counts {
		if r.counts == nil {
			r.counts = map[tiles.FixKind]int{}
		}
		r.counts[kind] += n
	}
}

// Counts returns the fixes by kind, nil for none.
func (r Report) Counts() map[tiles.FixKind]int {
	if len(r.counts) == 0 {
		return nil
	}
	return maps.Clone(r.counts)
}

// Total is how many fixes the report counts.
func (r Report) Total() int {
	n := 0
	for _, c := range r.counts {
		n += c
	}
	return n
}

// text cleans a text (clean) and cuts it to its limit, counting the cut, or
// says why it may not be shown: it names a hunk by its ID.
func (r *Report) text(s string, limit int) (string, string) {
	s = clean(s)
	if hunkMention.MatchString(s) {
		return "", "names a hunk by its ID"
	}
	if out := cut(s, limit); out != s {
		r.fix(FixCut, "")
		return out, ""
	}
	return s, ""
}

// The bounds on what Normalize reads: a generous multiple of what a
// composition holds.
const (
	maxInputDiagrams   = 40
	maxInputPlacements = 400
	maxInputPlaced     = 40
)

// Normalize checks a composition, as a caller of store.Put gave it,
// against its review r and the tile document converted from it (doc), and
// returns what may be stored and shown, with what it fixed. It never drops
// the whole composition, only the offending item. The result is a fixed
// point: normalising it again changes nothing.
//
//  1. Library and authored tiles: tiles.NormalizeExtras (a library tile's
//     definition, data and citations, an authored tile's ID, shape and
//     citations, at most 8 and 3). A review that does not convert keeps
//     neither.
//  2. Diagrams: a native tile whose ID is not dg_ and a slug, or one another
//     tile has, or whose type, counts, actors or text break the native
//     rules, is dropped; text is made one plain line (no markdown but code
//     spans) and cut to its limit; text that names a hunk by its ID drops
//     the tile; an anchor that cites no hunk of the pull request is dropped.
//     The first MaxDiagrams are kept.
//  3. The overview: the risk summary and the verdict's texts are cleaned and
//     cut; a verdict point's section the review does not have is cleared, a
//     verdict past three points loses the rest and one with none is dropped;
//     screenshots name the review's attachments, once each, at most three;
//     the diagram names a tile of the composition; an intent names a session
//     the review links (not a restricted one) and a prompt it has, at most
//     three.
//  4. Placements: a placement names a section of the review or context; a
//     tile it places must exist and be placed once (the overview's diagram
//     counts), else it is dropped; placements of one section merge, and
//     they are kept in review order, context first, without empty ones.
//
// The generator passes through: the host sets it (tiles.Generator.Checked).
func Normalize(r *Review, doc *tiles.Document, c *Composition) (Composition, Report) {
	var report Report
	out := Composition{Schema: CompositionName, Generator: c.Generator, Sections: []Placement{}}
	if c.Generator != nil {
		g := *c.Generator
		out.Generator = &g
	}
	ids := map[string]bool{}
	if len(c.Library) > 0 || len(c.Authored) > 0 {
		if doc == nil {
			report.fix(FixUnusable, "the review does not convert to tiles")
		} else {
			library, authored, fixes := tiles.NormalizeExtras(doc, c.Library, c.Authored)
			counts := fixes.Counts()
			if counts[tiles.FixUnusable] > 0 {
				report.fix(FixUnusable, "the review does not convert to tiles")
			} else {
				out.Library, out.Authored = library, authored
				for kind, count := range counts {
					if tileFix(kind) {
						report.add(map[tiles.FixKind]int{kind: count})
					}
				}
			}
		}
	}
	for _, id := range ExtraIDs(&out) {
		ids[id] = true
	}
	checkParts(r, c, &out, ids, &report)
	return out, report
}

// Check is Normalize without the library and authored tiles' own checks,
// for a reader of a stored composition whose tiles are checked where they
// are drawn (the served tile document): extraIDs are the IDs of those
// tiles, which placements and the overview may name. The composition's
// library and authored tiles are kept as they are.
func Check(r *Review, c *Composition, extraIDs []string) Composition {
	var report Report
	out := Composition{Schema: CompositionName, Generator: c.Generator, Library: c.Library, Authored: c.Authored, Sections: []Placement{}}
	ids := map[string]bool{}
	for _, id := range extraIDs {
		ids[id] = true
	}
	checkParts(r, c, &out, ids, &report)
	return out
}

// checkParts checks the diagrams, the overview and the placements of c into
// out; ids holds the IDs of the tiles out already has.
func checkParts(r *Review, c *Composition, out *Composition, ids map[string]bool, report *Report) {
	// Diagrams.
	for i, t := range c.Diagrams {
		if i == maxInputDiagrams {
			report.fix(FixLimit, "")
			break
		}
		check := &nativeCheck{review: r, report: report}
		checked, ok := check.check(t)
		switch {
		case !ok:
			report.fix(FixNative, "a diagram was dropped: "+check.problem)
		case ids[checked.ID]:
			report.fix(FixNative, "a diagram was dropped: its ID is another tile's")
		case len(out.Diagrams) == MaxDiagrams:
			report.fix(FixLimit, "")
		default:
			ids[checked.ID] = true
			out.Diagrams = append(out.Diagrams, checked)
		}
	}

	out.Overview = overview(r, c.Overview, ids, report)
	placed := map[string]bool{}
	if out.Overview.Diagram != nil {
		placed[*out.Overview.Diagram] = true
	}
	out.Sections = placements(r, c.Sections, ids, placed, report)
}

// tileFix reports whether a fix of baum.composition/1's normaliser is about
// the library and authored tiles themselves, not their layout.
func tileFix(kind tiles.FixKind) bool {
	s := string(kind)
	return strings.HasPrefix(s, "library_") || strings.HasPrefix(s, "authored_") ||
		kind == tiles.FixRenamed || kind == tiles.FixTitle || kind == tiles.FixControl || kind == tiles.FixInputLimit
}

// ExtraIDs are the IDs of a composition's library and authored tiles, in
// order: a library tile's is its type's name (tiles.LibraryTileID).
func ExtraIDs(c *Composition) []string {
	var out []string
	for _, lib := range c.Library {
		var def struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(lib.Definition, &def) == nil && def.Name != "" {
			out = append(out, tiles.LibraryTileID(def.Name))
		}
	}
	for _, a := range c.Authored {
		out = append(out, a.ID)
	}
	return out
}

func overview(r *Review, in Overview, ids map[string]bool, report *Report) Overview {
	out := Overview{Screenshots: []string{}, Intent: []Intent{}}
	if text, problem := report.text(in.RiskSummary, MaxRiskSummary); problem != "" {
		report.fix(FixOverview, "the risk summary "+problem)
	} else {
		out.RiskSummary = text
	}
	if v := in.Verdict; v != nil {
		out.Verdict = verdict(r, v, report)
	}
	attachments := map[string]bool{}
	for _, a := range r.Attachments {
		attachments[a.ID] = true
	}
	for _, id := range in.Screenshots {
		switch {
		case !attachments[id] || slices.Contains(out.Screenshots, id):
			report.fix(FixOverview, "a screenshot names no attachment of the review, or one twice")
		case len(out.Screenshots) == MaxScreenshots:
			report.fix(FixLimit, "")
		default:
			out.Screenshots = append(out.Screenshots, id)
		}
	}
	if d := in.Diagram; d != nil && *d != "" {
		if ids[*d] {
			diagram := *d
			out.Diagram = &diagram
		} else {
			report.fix(FixOverview, "the overview's diagram names no tile")
		}
	}
	for _, i := range in.Intent {
		s := r.session(i.Session)
		if s == nil || s.Restricted || !slices.ContainsFunc(s.Prompts, func(p Prompt) bool { return p.Index == i.Prompt }) {
			report.fix(FixOverview, "an intent names no session prompt of the review")
			continue
		}
		asked, p1 := report.text(i.Asked, MaxIntentText)
		got, p2 := report.text(i.Got, MaxIntentText)
		if p1 != "" || p2 != "" || asked == "" || got == "" {
			report.fix(FixOverview, "an intent's text is empty or names a hunk")
			continue
		}
		if len(out.Intent) == MaxIntent {
			report.fix(FixLimit, "")
			continue
		}
		out.Intent = append(out.Intent, Intent{Asked: asked, Got: got, Session: i.Session, Prompt: i.Prompt})
	}
	return out
}

func verdict(r *Review, v *Verdict, report *Report) *Verdict {
	text, problem := report.text(v.Text, MaxVerdictText)
	if problem != "" || text == "" {
		report.fix(FixOverview, "the verdict's line is empty or names a hunk")
		return nil
	}
	out := &Verdict{Text: text, Points: []Point{}}
	for _, p := range v.Points {
		point, problem := report.text(p.Text, MaxPointText)
		if problem != "" || point == "" {
			report.fix(FixOverview, "a verdict point is empty or names a hunk")
			continue
		}
		if len(out.Points) == MaxVerdictPoints {
			report.fix(FixLimit, "")
			continue
		}
		section := p.Section
		if section != "" && r.Section(section) == nil {
			report.fix(FixOverview, "a verdict point names a section the review does not have")
			section = ""
		}
		out.Points = append(out.Points, Point{Text: point, Section: section})
	}
	if len(out.Points) == 0 {
		report.fix(FixOverview, "the verdict has no point")
		return nil
	}
	return out
}

// placements checks the placements and returns them in review order,
// context first; placed holds the tiles already placed and gains the ones
// these place.
func placements(r *Review, in []Placement, ids, placed map[string]bool, report *Report) []Placement {
	by := map[string][]string{}
	for i, p := range in {
		if i == maxInputPlacements {
			report.fix(FixLimit, "")
			break
		}
		if p.ID != ContextSection && r.Section(p.ID) == nil {
			report.fix(FixPlacement, "a placement names a section the review does not have")
			continue
		}
		for j, id := range p.Tiles {
			if j == maxInputPlaced {
				report.fix(FixLimit, "")
				break
			}
			if !ids[id] || placed[id] {
				report.fix(FixPlacement, fmt.Sprintf("a placement on %s names a tile the composition does not have, or one already placed", plain(p.ID)))
				continue
			}
			placed[id] = true
			by[p.ID] = append(by[p.ID], id)
		}
	}
	out := []Placement{}
	for _, id := range sectionOrder(r) {
		if tiles := by[id]; len(tiles) > 0 {
			out = append(out, Placement{ID: id, Tiles: tiles})
		}
	}
	return out
}

// sectionOrder is context, then the review's sections in order.
func sectionOrder(r *Review) []string {
	out := []string{ContextSection}
	for _, s := range r.Sections {
		out = append(out, s.ID)
	}
	return out
}

// plain is an ID when it is a plain identifier, for a problem's words.
func plain(id string) string {
	if !plainID.MatchString(id) {
		return "a section"
	}
	return id
}

var plainID = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)

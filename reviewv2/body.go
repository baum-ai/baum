package reviewv2

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/tiles/view"
)

// EnsureBodies leaves every section of the document without a tile block that names a tile the document
// does not have or another section owns, or that repeats an earlier one,
// and with a tile block, at the end, for each tile the section owns that its
// body does not place. Never a section's text.
func EnsureBodies(d *Document) {
	byID := map[string]Tile{}
	for _, t := range d.Tiles {
		byID[t.ID] = t
	}
	for i := range d.Sections {
		s := &d.Sections[i]
		body := []Block{}
		placed := map[string]bool{}
		for _, b := range s.Body {
			if b.Type == BlockTile {
				t, ok := byID[b.Tile]
				if !ok || t.Owner != s.ID || placed[b.Tile] {
					continue
				}
				placed[b.Tile] = true
			}
			body = append(body, b)
		}
		for _, t := range d.Tiles {
			if t.Owner == s.ID && !placed[t.ID] {
				placed[t.ID] = true
				body = append(body, Block{Type: BlockTile, Tile: t.ID})
			}
		}
		s.Body = body
	}
}

// bodyWords are the words of a section's body that a reader reads: its
// blocks' titles and texts and its points' texts and details.
func bodyWords(body []Block) []string {
	var out []string
	for _, b := range body {
		out = append(out, b.Title, b.Text)
		for _, p := range b.Items {
			out = append(out, p.Text, p.More)
		}
	}
	return out
}

// CleanInline makes a body text one line of inline markdown: control and
// zero-width characters removed and whitespace folded. Bold and code stay.
func CleanInline(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r), r == 0x200b, r == 0x200c, r == 0x200d, r == 0x2060, r == 0xfeff:
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// NoteKind is kind when it is one of NoteKinds, else empty (a plain note).
func NoteKind(kind string) string {
	if slices.Contains(NoteKinds, kind) {
		return kind
	}
	return ""
}

// PointTone is tone when it is one of view.Tones but plain, else empty.
func PointTone(tone string) string {
	if tone != "plain" && slices.Contains(view.Tones, tone) {
		return tone
	}
	return ""
}

// CheckBody checks a body as the text limits ask: texts cleaned (CleanInline)
// and cut to their limits, blocks and points without text dropped (and
// blocks of a type the body does not have), blocks capped at MaxBlocks and
// points at MaxPoints, and a point's citations kept to those that resolve
// (refs; once each, at most MaxCites). It returns the body fixed and what it
// fixed (view.FixCut for each text cut, FixLimit for each block or point
// past its limit, FixNativeItem for each one dropped, FixAnchor for each
// citation dropped). A body that ends up empty is left empty. Tile blocks
// are kept as they are: whether they name a tile of the section is the
// document's (EnsureBodies).
func CheckBody(body []Block, refs *Refs) ([]Block, view.Report) {
	var report view.Report
	cut := func(s string, limit int) string {
		s = CleanInline(s)
		if utf8.RuneCountInString(s) > limit {
			report.Fix(view.FixCut, "")
			return view.Cut(s, limit)
		}
		return s
	}
	out := []Block{}
	for _, b := range body {
		fixed := Block{Type: b.Type}
		switch b.Type {
		case BlockProse, BlockNote:
			limit := MaxProse
			if b.Type == BlockNote {
				limit = MaxNote
			}
			if fixed.Text = cut(b.Text, limit); fixed.Text == "" {
				report.Fix(view.FixNativeItem, "a block has no text")
				continue
			}
			fixed.Title = cut(b.Title, MaxBlockTitle)
			if b.Type == BlockNote {
				fixed.Kind = NoteKind(b.Kind)
			}
		case BlockPoints:
			fixed.Title = cut(b.Title, MaxBlockTitle)
			for _, p := range b.Items {
				point := Point{Text: cut(p.Text, MaxPoint), More: cut(p.More, MaxPointDetail), Tone: PointTone(p.Tone), Cites: []Anchor{}}
				if point.Text == "" {
					report.Fix(view.FixNativeItem, "a point has no text")
					continue
				}
				if len(fixed.Items) == MaxPoints {
					report.Fix(view.FixLimit, "a block has more points than it may")
					continue
				}
				seen := map[string]bool{}
				for _, c := range p.Cites {
					if len(point.Cites) == MaxCites || seen[string(c)] {
						continue
					}
					seen[string(c)] = true
					if refs != nil && refs.AnchorProblem(c) != "" {
						report.Fix(view.FixAnchor, "a point cites nothing this pull request has")
						continue
					}
					point.Cites = append(point.Cites, c)
				}
				fixed.Items = append(fixed.Items, point)
			}
			if len(fixed.Items) == 0 {
				report.Fix(view.FixNativeItem, "a points block has no points")
				continue
			}
		case BlockTile:
			if fixed.Tile = strings.TrimSpace(b.Tile); fixed.Tile == "" {
				report.Fix(view.FixNativeItem, "a tile block names no tile")
				continue
			}
		default:
			report.Fix(view.FixNativeItem, fmt.Sprintf("%q is not a block type", b.Type))
			continue
		}
		if len(out) == MaxBlocks {
			report.Fix(view.FixLimit, "a body has more blocks than it may")
			continue
		}
		out = append(out, fixed)
	}
	return out, report
}

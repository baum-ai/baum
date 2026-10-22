package bundle

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"strings"
)

// redactPatches masks the patch text of diff in place and returns the
// number of spans masked.
//
// The hunks diff parsing took from one file are redacted as one text, the
// file's patch, so the engine can follow a key that runs on from one hunk
// into the next, past the hunk header between them. That text is the
// file's patch up to its last hunk's margin: the one parsing kept
// (filePatch.text), or else the hunks' kept texts joined by newlines in
// file order. Only the kept text of each hunk is copied out of the redacted
// text. A hunk diff parsing did not produce is redacted on its own.
//
// Base64 runs and headings are then masked too (base64Masks), and every
// line keeps its diff marker and every hunk header its line ranges.
//
// Redaction fails closed: a hunk whose patch no longer lies where parsing
// put it in its file's text, or every hunk of a file whose redacted text
// changed length, is masked whole, keeping only its diff syntax.
func redactPatches(ctx context.Context, redactor *Redactor, diff []Hunk) (int, error) {
	type member struct{ hunk, offset int }
	type group struct {
		text    string
		members []member
	}
	var groups []*group
	byFile := map[*filePatch]*group{}
	total := 0
	for i := range diff {
		hunk := &diff[i]
		file, offset := hunk.file, hunk.offset
		hunk.file, hunk.offset = nil, 0
		if hunk.Patch == "" {
			continue
		}
		if file != nil && file.text != "" && !strings.HasPrefix(file.text[min(offset, len(file.text)):], hunk.Patch) {
			hunk.Patch = maskPatch(hunk.Patch)
			total++
			continue
		}
		g := byFile[file]
		if g == nil {
			g = &group{}
			if file != nil {
				g.text, byFile[file] = file.text, g
			}
			groups = append(groups, g)
		}
		g.members = append(g.members, member{i, offset})
	}
	for _, g := range groups {
		// Hunks are read in file order, whatever their order in diff.
		slices.SortStableFunc(g.members, func(a, b member) int { return cmp.Compare(a.offset, b.offset) })
		text, offsets := g.text, make([]int, len(g.members))
		if text == "" {
			patches := make([]string, len(g.members))
			at := 0
			for k, m := range g.members {
				patches[k], offsets[k] = diff[m.hunk].Patch, at
				at += len(diff[m.hunk].Patch) + 1
			}
			text = strings.Join(patches, "\n")
		} else {
			for k, m := range g.members {
				offsets[k] = m.offset
			}
		}
		redacted, count, err := redactor.RedactString(ctx, text)
		if err != nil {
			return 0, err
		}
		if len(redacted) != len(text) {
			// The mask keeps the text's length, so this is not expected; no
			// hunk can be cut back out of the redacted text.
			for _, m := range g.members {
				diff[m.hunk].Patch = maskPatch(diff[m.hunk].Patch)
			}
			total += len(g.members)
			continue
		}
		masks, runs := base64Masks(text)
		total += count + changedMasks(text, redacted, masks, runs)
		for k, m := range g.members {
			diff[m.hunk].Patch = maskRange(text, redacted, masks, offsets[k], offsets[k]+len(diff[m.hunk].Patch))
		}
	}
	return total, nil
}

// maskSpan is a byte range of a patch base64Masks masks; spans of one run
// share its index.
type maskSpan struct{ start, end, run int }

const (
	// minBase64Line is the shortest line content base64Masks takes for
	// key material, and the shortest hunk heading it masks.
	minBase64Line = 40
	// minBase64Run is the fewest consecutive such lines it masks.
	minBase64Run = 3
)

// base64Masks returns the parts of patch masked as key material whatever
// the engine found, sorted by start, and how many runs they make up. It is
// a deliberate over-mask for the key bodies a patch shows without their
// armor: an edit inside a committed key gives hunks of its body lines alone,
// and git's section heading after a hunk header often repeats a key line.
//
//   - Every run of minBase64Run or more consecutive lines whose content,
//     after its diff marker, is pure base64 ([A-Za-z0-9+/=]) of at least
//     minBase64Line bytes, with a shorter pure-base64 line just before or
//     after the run (a key's last body line). Hunk headers neither break
//     nor join a run. Embedded base64 data in a patch is masked the same.
//   - Every hunk heading, the text after "@@ -a,b +c,d @@ ", that is pure
//     base64 of at least minBase64Line bytes.
//
// Diff markers and the header's line ranges are never part of a span.
func base64Masks(patch string) (spans []maskSpan, runs int) {
	const (
		other = iota
		short
		long
	)
	type line struct{ start, end, kind int }
	var run []line
	var before, prev line
	flush := func(after line) {
		if len(run) >= minBase64Run {
			if before.kind == short {
				spans = append(spans, maskSpan{before.start, before.end, runs})
			}
			for _, l := range run {
				spans = append(spans, maskSpan{l.start, l.end, runs})
			}
			if after.kind == short {
				spans = append(spans, maskSpan{after.start, after.end, runs})
			}
			runs++
		}
		run = run[:0]
	}
	for start := 0; start < len(patch); {
		end := strings.IndexByte(patch[start:], '\n')
		if end < 0 {
			end = len(patch)
		} else {
			end += start
		}
		text := patch[start:end]
		if n := hunkHeaderLen(text); n > 0 {
			if len(text)-n >= minBase64Line && isBase64(text[n:]) {
				spans = append(spans, maskSpan{start + n, end, runs})
				runs++
			}
			start = end + 1
			continue
		}
		cur := line{kind: other}
		if text != "" && strings.IndexByte(" +-", text[0]) >= 0 && isBase64(text[1:]) {
			cur = line{start + 1, end, short}
			if end-start-1 >= minBase64Line {
				cur.kind = long
			}
		}
		if cur.kind == long {
			if len(run) == 0 {
				before = prev
			}
			run = append(run, cur)
		} else {
			flush(cur)
		}
		prev = cur
		start = end + 1
	}
	flush(line{})
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans, runs
}

func isBase64(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '+', c == '/', c == '=':
		default:
			return false
		}
	}
	return true
}

// changedMasks counts the runs of masks that mask a byte the engine left as
// it was, so a key the engine already masked is not counted twice.
func changedMasks(text, redacted string, masks []maskSpan, runs int) int {
	changed := make([]bool, runs)
	count := 0
	for _, span := range masks {
		if changed[span.run] {
			continue
		}
		for j := span.start; j < span.end; j++ {
			if redacted[j] == text[j] && maskByte(text[j]) != text[j] {
				changed[span.run] = true
				count++
				break
			}
		}
	}
	return count
}

// maskRange returns redacted[from:to] with masks applied and its diff
// syntax kept (keepDiffSyntax), copying only that range. from is the start
// of a line.
func maskRange(text, redacted string, masks []maskSpan, from, to int) string {
	out := []byte(redacted[from:to])
	i := sort.Search(len(masks), func(i int) bool { return masks[i].end > from })
	for ; i < len(masks) && masks[i].start < to; i++ {
		for j := max(masks[i].start, from); j < min(masks[i].end, to); j++ {
			out[j-from] = maskByte(text[j])
		}
	}
	restoreDiffSyntax(out, text[from:to])
	return string(out)
}

// maskPatch masks every byte of patch but its line breaks and diff syntax.
func maskPatch(patch string) string {
	out := []byte(patch)
	for i, c := range out {
		if c != '\n' {
			out[i] = maskByte(c)
		}
	}
	restoreDiffSyntax(out, patch)
	return string(out)
}

// maskByte is the engine's format-preserving mask of one byte: a letter
// becomes "X" or "x" by case, a digit "9", the separators "-_.:/+=@" stay
// and any other byte becomes "*".
func maskByte(c byte) byte {
	switch {
	case 'A' <= c && c <= 'Z':
		return 'X'
	case 'a' <= c && c <= 'z':
		return 'x'
	case '0' <= c && c <= '9':
		return '9'
	case strings.IndexByte("-_.:/+=@", c) >= 0:
		return c
	}
	return '*'
}

// keepDiffSyntax puts back the diff syntax a mask spanning lines overwrites:
// the first byte of every patch line, its diff marker, and the line ranges
// of every hunk header. A key spanning lines is masked whole, and the mask
// would turn a context line's leading space into "*", or a hunk header
// inside the key, "@@ -36,7 +36,7 @@", into "@@*-99*9*+99*9*@@*", so neither
// would parse as diff syntax. A marker (space, "+", "-" or "\\") and a
// header's "@@ -a,b +c,d @@" and the space after it are diff syntax, never
// part of the secret. Any other first byte stays masked, as does the
// section heading git writes after a header: it copies a line of the file,
// which may be a line of the key.
func keepDiffSyntax(patch, redacted string) string {
	out := []byte(redacted)
	restoreDiffSyntax(out, patch)
	return string(out)
}

// restoreDiffSyntax is keepDiffSyntax in place: out is patch, masked.
func restoreDiffSyntax(out []byte, patch string) {
	for start := 0; start < len(patch); {
		end := strings.IndexByte(patch[start:], '\n')
		if end < 0 {
			end = len(patch)
		} else {
			end += start
		}
		line := patch[start:end]
		if n := hunkHeaderLen(line); n > 0 {
			copy(out[start:], line[:n])
		} else if line != "" && strings.IndexByte(" +-\\", line[0]) >= 0 {
			out[start] = line[0]
		}
		start = end + 1
	}
}

// hunkHeaderLen returns the length of the "@@ -a,b +c,d @@" a hunk header
// line starts with, with the space after it, or 0 for any other line. A
// count may be left out, as in "@@ -1 +1 @@".
func hunkHeaderLen(line string) int {
	i, ok := len("@@ -"), strings.HasPrefix(line, "@@ -")
	lineRange := func() bool {
		digits := func() bool {
			from := i
			for i < len(line) && '0' <= line[i] && line[i] <= '9' {
				i++
			}
			return i > from
		}
		if !digits() {
			return false
		}
		if i < len(line) && line[i] == ',' {
			i++
			return digits()
		}
		return true
	}
	if !ok || !lineRange() || !strings.HasPrefix(line[i:], " +") {
		return 0
	}
	i += len(" +")
	if !lineRange() || !strings.HasPrefix(line[i:], " @@") {
		return 0
	}
	i += len(" @@")
	if i < len(line) && line[i] == ' ' {
		i++
	}
	return i
}

package bundle

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// CollapsedReason is the TextlessReason of the one entry that stands for
// every hunk of a file fitDiffIndex collapsed.
const CollapsedReason = "collapsed"

// indexFile is one file of the diff index, as fitDiffIndex weighs it: the
// positions of its entries in the diff, in order, and what is left of it.
type indexFile struct {
	entries []int
	// bytes is what the file's entries add to the encoded index, a comma
	// each included.
	bytes     int
	collapsed *Hunk
	dropped   bool
}

// fitDiffIndex degrades the diff index until its encoded size is within
// limit, rather than fail the review, in this order:
//
//  1. every generated or vendored file (lockfiles among them) with more
//     than one entry is collapsed to one entry;
//  2. the other files are collapsed one at a time, the largest saving first;
//  3. whole files are left out, one at a time, in dropTier order, each tier
//     from the end of the diff.
//
// A collapsed entry has an ID hashed from the path and its hunks' IDs
// (collapsedHunkID), the file's path, change and role, its hunks' summed
// line counts and the new-side span from
// the first to the last of them, and no patch: CollapsedReason, or the
// checkout's omitted_ reason when any of its hunks had one, which the risk
// score reads. The order of entries is kept, a collapsed one at its first
// hunk's place. Each stage that cut anything adds one generator.truncated
// note with its count. A limit of zero or less is no limit.
func fitDiffIndex(bundle *Bundle, limit int) {
	if limit <= 0 || diffIndexBytes(bundle) <= limit {
		return
	}
	var files []*indexFile
	byPath := map[string]*indexFile{}
	for i, hunk := range bundle.Diff {
		file := byPath[hunk.Path]
		if file == nil {
			file = &indexFile{}
			byPath[hunk.Path] = file
			files = append(files, file)
		}
		file.entries = append(file.entries, i)
		file.bytes += indexEntryBytes(hunk)
	}
	// The encoded index is its brackets and every remaining entry with a
	// comma, less the comma after the last one: one byte plus the entries'
	// bytes, or two bytes ("[]") when no entry is left.
	size := 1
	for _, file := range files {
		size += file.bytes
	}
	fitsNow := func() bool { return max(size, 2) <= limit }
	lowValue := func(file *indexFile) bool {
		role := bundle.Diff[file.entries[0]].Role
		return role == RoleGenerated || role == RoleVendored
	}
	collapsedOf := func(file *indexFile) Hunk {
		hunks := make([]Hunk, len(file.entries))
		for i, entry := range file.entries {
			hunks[i] = bundle.Diff[entry]
		}
		return collapseHunks(hunks)
	}
	collapse := func(file *indexFile) {
		collapsed := collapsedOf(file)
		size += indexEntryBytes(collapsed) - file.bytes
		file.collapsed, file.bytes = &collapsed, indexEntryBytes(collapsed)
	}

	var notes []string
	collapsedLowValue := 0
	for _, file := range files {
		if len(file.entries) > 1 && lowValue(file) {
			collapse(file)
			collapsedLowValue++
		}
	}
	if collapsedLowValue > 0 {
		notes = append(notes, fmt.Sprintf("hunks of %d generated or vendored file(s) collapsed to one diff index entry per file, without patch text, to fit the diff index limit", collapsedLowValue))
	}

	if !fitsNow() {
		type saving struct {
			file  *indexFile
			bytes int
		}
		var savings []saving
		for _, file := range files {
			if len(file.entries) > 1 && file.collapsed == nil {
				savings = append(savings, saving{file, file.bytes - indexEntryBytes(collapsedOf(file))})
			}
		}
		// Stable, so equal savings keep diff order.
		slices.SortStableFunc(savings, func(a, b saving) int { return cmp.Compare(b.bytes, a.bytes) })
		collapsedOther := 0
		for _, candidate := range savings {
			if fitsNow() || candidate.bytes <= 0 {
				break
			}
			collapse(candidate.file)
			collapsedOther++
		}
		if collapsedOther > 0 {
			notes = append(notes, fmt.Sprintf("hunks of %d other file(s) collapsed to one diff index entry per file, without patch text, to fit the diff index limit, the largest first", collapsedOther))
		}
	}

	if !fitsNow() {
		dropped := 0
		for tier := range 4 {
			for i := len(files) - 1; i >= 0 && !fitsNow(); i-- {
				if file := files[i]; !file.dropped && dropTier(bundle.Diff[file.entries[0]].Role) == tier {
					file.dropped = true
					size -= file.bytes
					dropped++
				}
			}
		}
		if dropped > 0 {
			notes = append(notes, fmt.Sprintf("%d file(s) left out of the diff index to fit its limit: generated and vendored files first, then docs, then tests and config, code last", dropped))
		}
	}

	kept := make([]Hunk, 0, len(bundle.Diff))
	for i, hunk := range bundle.Diff {
		file := byPath[hunk.Path]
		switch {
		case file.dropped:
		case file.collapsed == nil:
			kept = append(kept, hunk)
		case file.entries[0] == i:
			kept = append(kept, *file.collapsed)
		}
	}
	bundle.Diff = kept
	bundle.Truncated = append(bundle.Truncated, notes...)
}

// dropTier orders the files fitDiffIndex leaves out: generated and vendored
// first, then docs, then tests and config, and code and migrations last.
func dropTier(role string) int {
	switch role {
	case RoleGenerated, RoleVendored:
		return 0
	case RoleDocs:
		return 1
	case RoleTest, RoleConfig:
		return 2
	default:
		return 3
	}
}

// collapseHunks is the one entry that stands for every hunk of a file, in
// diff order.
func collapseHunks(hunks []Hunk) Hunk {
	first := hunks[0]
	collapsed := Hunk{
		ID: collapsedHunkID(first.Path, hunks), Path: first.Path, OldPath: first.OldPath, Change: first.Change, Role: first.Role,
		Textless: true, TextlessReason: CollapsedReason,
	}
	start, end := 0, 0
	for _, hunk := range hunks {
		collapsed.Additions += hunk.Additions
		collapsed.Deletions += hunk.Deletions
		if hunk.LineCount > 0 {
			if start == 0 || hunk.StartLine < start {
				start = hunk.StartLine
			}
			end = max(end, hunk.StartLine+hunk.LineCount-1)
		}
		if collapsed.TextlessReason == CollapsedReason && strings.HasPrefix(hunk.TextlessReason, "omitted_") {
			collapsed.TextlessReason = hunk.TextlessReason
		}
	}
	if start > 0 {
		collapsed.StartLine, collapsed.LineCount = start, end-start+1
	} else {
		collapsed.StartLine = first.StartLine
	}
	collapsed.DiffURL = HunkDiffURL(collapsed.Path, collapsed.StartLine)
	return collapsed
}

// collapsedHunkID hashes the path and the IDs of the hunks a collapsed entry
// stands for, in order. Those IDs address the hunks' content, so the entry's
// ID changes whenever the file's change does: a later version whose section
// holds the same collapsed entry reuses its details only when the code is
// unchanged (review.ReusableSectionDetails compares hunk IDs).
func collapsedHunkID(path string, hunks []Hunk) string {
	digest := newHunkIDDigest(path)
	digest.Write([]byte("collapsed\x00"))
	for _, hunk := range hunks {
		digest.Write([]byte(hunk.ID + "\x00"))
	}
	return hunkIDFromDigest(digest)
}

// indexEntry is a hunk as the diff index counts it: without its patch text,
// and without a textless marker byte trimming set, since that counts as
// content.
func indexEntry(hunk Hunk) Hunk {
	hunk.Patch = ""
	if strings.HasPrefix(hunk.TextlessReason, "trimmed_") {
		hunk.Textless, hunk.TextlessReason = false, ""
	}
	return hunk
}

// indexEntryBytes is what one entry adds to the encoded diff index, its
// comma included.
func indexEntryBytes(hunk Hunk) int {
	encoded, _ := json.Marshal(indexEntry(hunk))
	return len(encoded) + 1
}

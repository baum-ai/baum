package review

import (
	"regexp"
	"strconv"
)

// diffHunk is the diff of an indexed hunk: its header and its lines.
type diffHunk struct {
	Header string     `json:"header"`
	Lines  []diffLine `json:"lines"`
}

// diffLine is one line of a hunk's diff.
type diffLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old_line,omitempty"`
	New  int    `json:"new_line,omitempty"`
}

var hunkRangePattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

// hunkRange parses a hunk header into its old and new start and count.
func hunkRange(header string) (old, oldCount, next, newCount int, ok bool) {
	match := hunkRangePattern.FindStringSubmatch(header)
	if match == nil {
		return
	}
	old, _ = strconv.Atoi(match[1])
	next, _ = strconv.Atoi(match[3])
	oldCount, newCount = 1, 1
	if match[2] != "" {
		oldCount, _ = strconv.Atoi(match[2])
	}
	if match[4] != "" {
		newCount, _ = strconv.Atoi(match[4])
	}
	return old, oldCount, next, newCount, true
}

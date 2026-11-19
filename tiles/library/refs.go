package library

import (
	"fmt"
	"path"
	"strings"
)

// maxExcerpt bounds the lines of one hunk excerpt, as the page does.
const maxExcerpt = 200

// excerptContext is how many lines a cited range's excerpt shows around it.
const excerptContext = 3

// Refs returns what a page would send a frame for the sample's data: the
// payload of each citation, keyed by its citation key ("Citation keys" in
// ARCHITECTURE.md), resolved from the sample's own hunks and items, and the
// places of the citations that do not resolve. A glance's payloads carry no
// excerpts. Only hunk, risk and evidence citations resolve.
func (s Sample) Refs(face string) (map[string]any, []string) {
	refs := map[string]any{}
	var unresolved []string
	for _, c := range Citations(s.Data) {
		key, payload := s.resolve(c, face)
		if payload == nil {
			unresolved = append(unresolved, c.At)
			continue
		}
		refs[key] = payload
	}
	return refs, unresolved
}

func (s Sample) resolve(c Citation, face string) (string, map[string]any) {
	switch c.Type {
	case "hunk":
		var lines any
		if c.Lines != nil {
			lines = c.Lines
		}
		key, err := encode([]any{"hunk", c.ID, lines})
		h, ok := s.Hunks[c.ID]
		if err != nil || !ok {
			return "", nil
		}
		if c.Lines != nil && (c.Lines[0] > c.Lines[1] || c.Lines[0] < h.Range[0] || c.Lines[1] > h.Range[1]) {
			return "", nil
		}
		return string(key), hunkPayload(h, c.Lines, face)
	case "risk", "evidence":
		item, ok := s.Items[c.ID]
		if !ok {
			return "", nil
		}
		key, err := encode([]any{c.Type, c.ID})
		if err != nil {
			return "", nil
		}
		payload := map[string]any{"status": "ok", "label": item.Label, "navigable": true}
		if c.Type == "risk" {
			payload["tier"] = item.Tier
			payload["state"] = "open"
			payload["text"] = item.Text
		} else {
			payload["evidence_kind"] = "test"
			payload["evidence_status"] = item.Tier
			payload["claim"] = item.Text
		}
		return string(key), payload
	}
	return "", nil
}

func hunkPayload(h Hunk, cited []int, face string) map[string]any {
	label := path.Base(h.Path)
	switch {
	case cited != nil && cited[0] == cited[1]:
		label += fmt.Sprintf(":%d", cited[0])
	case cited != nil:
		label += fmt.Sprintf(":%d–%d", cited[0], cited[1])
	default:
		label += fmt.Sprintf(":%d–%d", h.Range[0], h.Range[1])
	}
	change := h.Change
	if change == "" {
		change = "modified"
	}
	payload := map[string]any{
		"status": "ok", "label": label, "navigable": true, "path": h.Path, "change": change, "role": "code",
		"range": []int{h.Range[0], h.Range[1]}, "additions": h.Additions, "deletions": h.Deletions,
	}
	if face != "detail" {
		return payload
	}
	first, last := 0, len(h.Lines)
	if cited != nil {
		first, last = -1, -1
		for i, line := range h.Lines {
			if line.New == 0 {
				continue
			}
			if line.New >= cited[0]-excerptContext && first < 0 {
				first = i
			}
			if line.New <= cited[1]+excerptContext {
				last = i + 1
			}
		}
		if first < 0 || last < first {
			first, last = 0, len(h.Lines)
		}
	}
	if last-first > maxExcerpt {
		last = first + maxExcerpt
	}
	lines := make([]map[string]any, 0, last-first)
	for _, line := range h.Lines[first:last] {
		entry := map[string]any{"kind": line.Kind, "text": line.Text}
		if line.Old > 0 {
			entry["old"] = line.Old
		}
		if line.New > 0 {
			entry["new"] = line.New
		}
		lines = append(lines, entry)
	}
	header := h.Header
	if header == "" {
		header = fmt.Sprintf("@@ +%d,%d @@", h.Range[0], h.Range[1]-h.Range[0]+1)
	}
	payload["excerpt"] = map[string]any{
		"header": strings.TrimSpace(header), "lines": lines,
		"omitted_before": first, "omitted_after": len(h.Lines) - last,
	}
	return payload
}

// HunkFromPatch returns a sample hunk for a diff hunk: its path, change,
// new range and lines numbered from its patch, for drawing data a fill wrote
// for a real pull request.
func HunkFromPatch(path, change string, start, count, additions, deletions int, patch string) Hunk {
	h := Hunk{Path: path, Change: change, Range: [2]int{start, max(start, start+count-1)}, Additions: additions, Deletions: deletions}
	oldLine, newLine := 0, start
	for _, text := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		if strings.HasPrefix(text, "@@") {
			h.Header = text
			var a, b, c, d int
			if n, _ := fmt.Sscanf(text, "@@ -%d,%d +%d,%d", &a, &b, &c, &d); n >= 3 {
				oldLine, newLine = a, c
			} else if n, _ := fmt.Sscanf(text, "@@ -%d +%d", &a, &c); n == 2 {
				oldLine, newLine = a, c
			}
			continue
		}
		switch {
		case strings.HasPrefix(text, "+"):
			h.Lines = append(h.Lines, Line{Kind: "add", New: newLine, Text: text[1:]})
			newLine++
		case strings.HasPrefix(text, "-"):
			h.Lines = append(h.Lines, Line{Kind: "remove", Old: oldLine, Text: text[1:]})
			oldLine++
		case strings.HasPrefix(text, `\`):
		default:
			h.Lines = append(h.Lines, Line{Kind: "context", Old: oldLine, New: newLine, Text: strings.TrimPrefix(text, " ")})
			oldLine++
			newLine++
		}
	}
	return h
}

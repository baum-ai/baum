// Package tilecatalogue holds the review tile catalogue: the kinds generation
// can offer and the selection guidance the model receives for each. It has no
// dependencies so a host can check its tile allow-list against it at startup.
package tilecatalogue

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// JSON is the complete catalogue, one entry per kind in catalogue order.
//
//go:embed catalogue.json
var JSON string

// Entry is one catalogue kind. Raw is the entry exactly as the catalogue
// states it.
type Entry struct {
	Kind string
	Raw  json.RawMessage
}

var entries = mustParse(JSON)

func mustParse(catalogue string) []Entry {
	parsed, err := ParseEntries([]byte(catalogue))
	if err != nil {
		panic(err.Error())
	}
	return parsed
}

// ParseEntries reads catalogue entries in the shape of catalogue.json: a JSON
// array of objects, each with a kind.
func ParseEntries(data []byte) ([]Entry, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("tile catalogue: %w", err)
	}
	parsed := make([]Entry, 0, len(raw))
	for _, item := range raw {
		var entry struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item, &entry); err != nil || entry.Kind == "" {
			return nil, fmt.Errorf("tile catalogue entry without a kind: %s", item)
		}
		parsed = append(parsed, Entry{Kind: entry.Kind, Raw: item})
	}
	return parsed, nil
}

// WithCandidates returns the entries of the offered kinds in catalogue order,
// each replaced by the candidate's entry of that kind when there is one, so
// selection guidance can be tried without editing catalogue.json. A candidate
// of a kind the catalogue does not list, of a kind that is not offered, or
// listed twice is an error: it would silently change nothing.
func WithCandidates(kinds []string, candidates []Entry) ([]Entry, error) {
	if err := Validate(kinds); err != nil {
		return nil, err
	}
	offered := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		offered[kind] = true
	}
	replacement := make(map[string]Entry, len(candidates))
	for _, candidate := range candidates {
		if err := Validate([]string{candidate.Kind}); err != nil {
			return nil, fmt.Errorf("candidate catalogue: %w", err)
		}
		if !offered[candidate.Kind] {
			return nil, fmt.Errorf("candidate catalogue entry %q is not in the offered tile kinds", candidate.Kind)
		}
		if _, repeated := replacement[candidate.Kind]; repeated {
			return nil, fmt.Errorf("candidate catalogue lists %q twice", candidate.Kind)
		}
		replacement[candidate.Kind] = candidate
	}
	selected := Entries(kinds)
	for i, entry := range selected {
		if candidate, ok := replacement[entry.Kind]; ok {
			selected[i] = candidate
		}
	}
	return selected, nil
}

// Kinds lists every catalogue kind in catalogue order.
func Kinds() []string {
	kinds := make([]string, len(entries))
	for i, entry := range entries {
		kinds[i] = entry.Kind
	}
	return kinds
}

// Entries returns the catalogue entries whose kind is listed, in catalogue
// order.
func Entries(kinds []string) []Entry {
	allowed := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		allowed[kind] = true
	}
	var selected []Entry
	for _, entry := range entries {
		if allowed[entry.Kind] {
			selected = append(selected, entry)
		}
	}
	return selected
}

// Validate rejects a kind the catalogue does not list and a kind listed twice.
func Validate(kinds []string) error {
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.Kind] = true
	}
	seen := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if !known[kind] {
			return fmt.Errorf("unknown tile kind %q; the catalogue lists %s", kind, strings.Join(Kinds(), ", "))
		}
		if seen[kind] {
			return fmt.Errorf("tile kind %q is listed twice", kind)
		}
		seen[kind] = true
	}
	return nil
}

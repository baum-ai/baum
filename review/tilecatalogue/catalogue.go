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
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(catalogue), &raw); err != nil {
		panic(fmt.Sprintf("tile catalogue: %v", err))
	}
	parsed := make([]Entry, 0, len(raw))
	for _, item := range raw {
		var entry struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(item, &entry); err != nil || entry.Kind == "" {
			panic(fmt.Sprintf("tile catalogue entry without a kind: %s", item))
		}
		parsed = append(parsed, Entry{Kind: entry.Kind, Raw: item})
	}
	return parsed
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

package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

// Tile payloads remain raw JSON in stored documents. These projections only
// cover facts which JSON Schema cannot validate across fields or against code.
type reviewTile struct {
	ID   string         `json:"id"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
	Code []tileCode     `json:"code"`
}

// tileCode is one code source of a tile: a file at a revision with the
// excerpt it shows.
type tileCode struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Version  int    `json:"review_version"`
	Diff     *struct {
		Hunks []tileHunk `json:"hunks"`
	} `json:"diff,omitempty"`
}

// tileHunk is one excerpt hunk of a tile code source.
type tileHunk struct {
	Header string     `json:"header"`
	Lines  []tileLine `json:"lines"`
}

// tileLine is one line of an excerpt hunk.
type tileLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old_line,omitempty"`
	New  int    `json:"new_line,omitempty"`
}

func tileObjects(value any) []map[string]any {
	values, _ := value.([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, value.(map[string]any))
	}
	return result
}
func tileStrings(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.(string))
	}
	return result
}
func tileIDs(objects []map[string]any, key string) (map[string]bool, bool) {
	ids := map[string]bool{}
	for _, object := range objects {
		id := object[key].(string)
		if ids[id] {
			return ids, false
		}
		ids[id] = true
	}
	return ids, true
}
func validTileReferences(tile reviewTile) bool {
	d := tile.Data
	switch tile.Kind {
	case "access-model":
		actors, unique := tileIDs(tileObjects(d["actors"]), "id")
		capabilities := tileObjects(d["capabilities"])
		_, distinct := tileIDs(capabilities, "id")
		if !unique || !distinct {
			return false
		}
		for _, capability := range capabilities {
			allowed := map[string]bool{}
			for _, id := range tileStrings(capability["allowed"]) {
				allowed[id] = true
			}
			for _, key := range []string{"allowed", "granted", "revoked"} {
				seen := map[string]bool{}
				for _, id := range tileStrings(capability[key]) {
					if seen[id] || !actors[id] || key == "granted" && !allowed[id] || key == "revoked" && allowed[id] {
						return false
					}
					seen[id] = true
				}
			}
		}
	case "architecture":
		containers, unique := tileIDs(tileObjects(d["containers"]), "id")
		if !unique {
			return false
		}
		for _, edge := range tileObjects(d["edges"]) {
			if !containers[edge["from"].(string)] || !containers[edge["to"].(string)] {
				return false
			}
		}
		for _, sequence := range tileObjects(d["sequences"]) {
			for _, step := range tileObjects(sequence["steps"]) {
				if !containers[step["from"].(string)] || !containers[step["to"].(string)] {
					return false
				}
			}
		}
	case "database-schema":
		tables := tileObjects(d["tables"])
		if _, unique := tileIDs(tables, "name"); !unique {
			return false
		}
		columns := map[string]bool{}
		for _, table := range tables {
			fields := tileObjects(table["columns"])
			if _, unique := tileIDs(fields, "name"); !unique {
				return false
			}
			for _, field := range fields {
				columns[table["name"].(string)+"."+field["name"].(string)] = true
			}
		}
		for _, relationship := range tileObjects(d["relationships"]) {
			if !columns[relationship["from"].(string)] || !columns[relationship["to"].(string)] {
				return false
			}
		}
	}
	return true
}

func tilesForSection(section authoredSection) []json.RawMessage {
	owners := []json.RawMessage{section.Tiles}
	for _, risk := range section.Risks {
		owners = append(owners, risk.Tiles)
	}
	return owners
}

// validateTileReferences reports every tile whose identifiers or internal
// references are invalid, so one repair can correct all of them.
func validateTileReferences(sections []authoredSection) []string {
	var findings []string
	for _, section := range sections {
		for _, owner := range tilesForSection(section) {
			if len(owner) == 0 {
				continue
			}
			var tiles []reviewTile
			if err := json.Unmarshal(owner, &tiles); err != nil {
				return append(findings, err.Error())
			}
			ids := map[string]bool{}
			for _, tile := range tiles {
				if ids[tile.ID] {
					findings = append(findings, fmt.Sprintf("section %s: duplicate tile ID within owner: %s", section.ID, tile.ID))
				}
				ids[tile.ID] = true
				if !validTileReferences(tile) {
					findings = append(findings, fmt.Sprintf("section %s: invalid references within tile: %s", section.ID, tile.ID))
				}
			}
		}
	}
	return findings
}

// validateTileSources reports every unverifiable code source rather than the
// first, naming its section, tile and path.
func validateTileSources(sections []authoredSection, bundle *bundlepkg.Bundle, directory string) []string {
	version := 1
	if bundle.Previous != nil {
		version += bundle.Previous.Version
	}
	inherited, err := inheritedTileSources(bundle)
	if err != nil {
		return []string{err.Error()}
	}
	var findings []string
	for _, section := range sections {
		for _, owner := range tilesForSection(section) {
			var tiles []reviewTile
			if len(owner) > 0 {
				if err := json.Unmarshal(owner, &tiles); err != nil {
					return append(findings, err.Error())
				}
			}
			for _, tile := range tiles {
				for i, source := range tile.Code {
					if problem := validateTileSource(source, bundle, directory, version, inherited); problem != "" {
						findings = append(findings, fmt.Sprintf("section %s tile %s source %d (%s): %s", section.ID, tile.ID, i+1, source.Path, problem))
					}
				}
			}
		}
	}
	return findings
}

// inheritedTileSources lists the code sources of the previous review's
// sections, which a new review may carry forward unchanged.
func inheritedTileSources(bundle *bundlepkg.Bundle) ([]tileCode, error) {
	if bundle.Previous == nil {
		return nil, nil
	}
	var inherited []tileCode
	for _, previous := range bundle.Previous.Sections {
		var section authoredSection
		if err := json.Unmarshal(previous.Content, &section); err != nil {
			return nil, err
		}
		for _, owner := range tilesForSection(section) {
			var tiles []reviewTile
			if len(owner) > 0 {
				if err := json.Unmarshal(owner, &tiles); err != nil {
					return nil, err
				}
			}
			for _, tile := range tiles {
				inherited = append(inherited, tile.Code...)
			}
		}
	}
	return inherited, nil
}

func validateTileSource(source tileCode, bundle *bundlepkg.Bundle, directory string, version int, inherited []tileCode) string {
	if !filepath.IsLocal(source.Path) || strings.Contains(source.Path, "\\") || strings.IndexFunc(source.Path, func(r rune) bool { return r < 32 }) >= 0 {
		return "invalid repository path"
	}
	for _, part := range strings.Split(source.Path, "/") {
		if part == "" || part == "." || part == ".." {
			return "invalid repository path"
		}
	}
	if source.Version > version {
		return "source is from a future review"
	}
	if source.Version < version {
		// The previous document is host-authored input. Carry its already
		// checked sources forward without rebinding them to the current checkout.
		for _, prior := range inherited {
			if reflect.DeepEqual(source, prior) {
				return ""
			}
		}
		return "historical source is not present in the supplied previous review; use a verifiable source"
	}
	root := "head"
	if source.Revision == bundle.PullRequest.MergeBaseSHA {
		root = "merge-base"
	} else if source.Revision != bundle.PullRequest.HeadSHA {
		return "revision does not match this review's head or merge base"
	}
	info, err := os.Stat(filepath.Join(directory, root, source.Path))
	if err != nil || !info.Mode().IsRegular() {
		return "file is unavailable in the pinned checkout"
	}
	if source.Diff == nil {
		return ""
	}
	// Bundle patches are the exact head/merge-base comparison, not model text.
	lines := map[tileLine]bool{}
	for _, line := range comparisonLines(bundle, source.Path, root) {
		lines[line] = true
	}
	previousOld, previousNew := 0, 0
	for _, hunk := range source.Diff.Hunks {
		old, oldCount, next, newCount, ok := tileHunkRange(hunk.Header)
		if !ok || old < previousOld || next < previousNew {
			return "invalid excerpt hunk ranges"
		}
		oldEnd, newEnd := old+oldCount, next+newCount
		for j, line := range hunk.Lines {
			if !lines[line] {
				return fmt.Sprintf("hunk %q line %d (%s %d/%d) does not match the supplied comparison text or numbering", hunk.Header, j+1, line.Kind, line.Old, line.New)
			}
			if line.Kind != "add" {
				if line.Old != old {
					return fmt.Sprintf("hunk %q line %d has old line %d, expected %d", hunk.Header, j+1, line.Old, old)
				}
				old++
			}
			if line.Kind != "remove" {
				if line.New != next {
					return fmt.Sprintf("hunk %q line %d has new line %d, expected %d", hunk.Header, j+1, line.New, next)
				}
				next++
			}
		}
		if old != oldEnd || next != newEnd {
			return fmt.Sprintf("hunk %q header counts do not match its %d lines", hunk.Header, len(hunk.Lines))
		}
		previousOld, previousNew = old, next
	}
	return ""
}

// comparisonLines lists the numbered lines the bundle's patches show for one
// path, in patch order.
func comparisonLines(bundle *bundlepkg.Bundle, path, root string) []tileLine {
	var lines []tileLine
	for _, hunk := range comparisonHunks(bundle, path, root) {
		lines = append(lines, hunk...)
	}
	return lines
}

// comparisonHunks lists the same lines grouped by the patch hunk that shows
// them, so an excerpt can be confined to one hunk.
func comparisonHunks(bundle *bundlepkg.Bundle, path, root string) [][]tileLine {
	var hunks [][]tileLine
	for _, hunk := range bundle.Diff {
		if hunk.Path != path && (root != "merge-base" || hunk.OldPath != path) {
			continue
		}
		oldLine, newLine := 0, 0
		var current []tileLine
		flush := func() {
			if len(current) > 0 {
				hunks = append(hunks, current)
				current = nil
			}
		}
		for _, line := range strings.Split(hunk.Patch, "\n") {
			if old, _, next, _, ok := tileHunkRange(line); ok {
				flush()
				oldLine, newLine = old, next
				continue
			}
			if len(line) == 0 || oldLine == 0 && newLine == 0 {
				continue
			}
			item := tileLine{Text: line[1:]}
			switch line[0] {
			case ' ':
				item.Kind, item.Old, item.New = "context", oldLine, newLine
				oldLine++
				newLine++
			case '+':
				item.Kind, item.New = "add", newLine
				newLine++
			case '-':
				item.Kind, item.Old = "remove", oldLine
				oldLine++
			default:
				continue
			}
			current = append(current, item)
		}
		flush()
	}
	return hunks
}

var tileRangePattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

// tileHunkRange parses a hunk header into its old and new start and count.
func tileHunkRange(header string) (old, oldCount, next, newCount int, ok bool) {
	match := tileRangePattern.FindStringSubmatch(header)
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

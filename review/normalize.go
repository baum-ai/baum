package review

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

// authoredNormalizer maps a document the lenient contract accepts onto the
// strict one. Every step is deterministic and needs nothing from the model:
// nulls and unknown keys go, canonical empties and constants are filled,
// identifiers take their prefix and spelling, hunk lists lose unknown hunks
// and repeats within a section, empty anchors other than a risk's or a
// behavior step's default to the enclosing section's hunks, attention references are resolved, evidence
// whose anchors cannot carry its status becomes asserted, and code
// sources get their SHA, review version and excerpt lines from the bundle.
// What it cannot decide stays for validation to report.
type authoredNormalizer struct {
	defs     map[string]any
	bundle   *bundlepkg.Bundle
	version  int
	hunks    map[string]bundlepkg.Hunk
	findings []string
}

func normalizeAuthored(value any, strict map[string]any, bundle *bundlepkg.Bundle) (any, []string) {
	root, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	defs, _ := strict["$defs"].(map[string]any)
	n := &authoredNormalizer{defs: defs, bundle: bundle, version: 1, hunks: map[string]bundlepkg.Hunk{}}
	if bundle.Previous != nil {
		n.version += bundle.Previous.Version
	}
	for _, h := range bundle.Diff {
		n.hunks[h.ID] = h
	}
	n.walk(root, strict)
	n.hunkAnchorLines(root)
	n.sections(root)
	return root, n.findings
}

// hunkAnchorLines treats model-selected line ranges as optional precision. A
// usable overlap is made canonical; a range that cannot select the hunk's
// right side is removed so consumers fall back to showing the full hunk.
func (n *authoredNormalizer) hunkAnchorLines(root map[string]any) {
	walkJSON(root, func(candidate map[string]any) {
		if candidate["type"] != "hunk" {
			return
		}
		id, _ := candidate["id"].(string)
		hunk, known := n.hunks[id]
		lines, present := candidate["lines"].([]any)
		if !known || !present {
			return
		}
		if len(lines) != 2 {
			delete(candidate, "lines")
			return
		}
		start, startOK := jsonIntegerNumber(lines[0])
		end, endOK := jsonIntegerNumber(lines[1])
		if !startOK || !endOK {
			delete(candidate, "lines")
			return
		}
		if start > end {
			start, end = end, start
		}
		hunkStart, hunkEnd := float64(hunk.StartLine), float64(hunk.StartLine+hunk.LineCount-1)
		if hunk.LineCount <= 0 || end < hunkStart || start > hunkEnd {
			delete(candidate, "lines")
			return
		}
		candidate["lines"] = []any{int(max(start, hunkStart)), int(min(end, hunkEnd))}
	})
}

func jsonIntegerNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	if !ok || math.IsInf(number, 0) || math.IsNaN(number) || math.Trunc(number) != number {
		return 0, false
	}
	return number, true
}

// walk applies the schema-driven steps: branch selection, unknown-key and
// null removal, defaults for missing required fields, identifier spelling and
// duplicate identifiers within one array.
func (n *authoredNormalizer) walk(value any, schema map[string]any) any {
	schema = ResolveSchema(schema, n.defs)
	if branch := n.branch(value, schema); branch != nil {
		schema = branch
	}
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			return v
		}
		closed := schema["additionalProperties"] == false
		for key, child := range v {
			prop, known := props[key].(map[string]any)
			if !known {
				if closed {
					delete(v, key)
				}
				continue
			}
			if child == nil {
				if !acceptsNull(ResolveSchema(prop, n.defs)) {
					delete(v, key)
				}
				continue
			}
			v[key] = n.walk(child, prop)
		}
		for _, name := range stringSlice(schema["required"]) {
			if _, present := v[name]; present {
				continue
			}
			if prop, ok := props[name].(map[string]any); ok {
				if fallback, ok := defaultValue(prop, n.defs); ok {
					v[name] = fallback
				}
			}
		}
		return v
	case []any:
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return v
		}
		for i := range v {
			v[i] = n.walk(v[i], items)
		}
		n.dedupe(v, items)
		return v
	case string:
		if pattern, ok := schema["pattern"].(string); ok {
			if rule := identifierPattern(pattern); rule != nil {
				return rule.normalize(v)
			}
		}
	}
	return value
}

// branch picks the union member an object belongs to: the only object branch,
// or the one whose type or kind constant matches the value's.
func (n *authoredNormalizer) branch(value any, schema map[string]any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var branches []any
	for _, key := range []string{"oneOf", "anyOf"} {
		if list, ok := schema[key].([]any); ok {
			branches = append(branches, list...)
		}
	}
	var objects []map[string]any
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		branch = ResolveSchema(branch, n.defs)
		if branch["type"] == "object" || branch["properties"] != nil {
			objects = append(objects, branch)
		}
	}
	if len(objects) == 1 {
		return objects[0]
	}
	for _, branch := range objects {
		props, _ := branch["properties"].(map[string]any)
		for _, key := range []string{"type", "kind"} {
			field, _ := props[key].(map[string]any)
			if field == nil {
				continue
			}
			if constant, ok := field["const"]; ok && constant == object[key] {
				return branch
			}
			if enum, ok := field["enum"].([]any); ok && slices.Contains(enum, object[key]) {
				return branch
			}
		}
	}
	return nil
}

// dedupe renames repeated prefixed identifiers within one array; references
// keep pointing at the first item that carried the identifier. Sections are
// left alone: a repeated section ID is a repeated section, which the model
// must merge, and the previous-order rules depend on the IDs as written.
func (n *authoredNormalizer) dedupe(list []any, items map[string]any) {
	seen := map[string]bool{}
	for _, raw := range list {
		object, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		schema := ResolveSchema(items, n.defs)
		if branch := n.branch(object, schema); branch != nil {
			schema = branch
		}
		props, _ := schema["properties"].(map[string]any)
		idSchema, _ := props["id"].(map[string]any)
		pattern, _ := idSchema["pattern"].(string)
		rule := identifierPattern(pattern)
		id, isString := object["id"].(string)
		if rule == nil || rule.prefix == "" || rule.prefix == "sec" || !isString {
			continue
		}
		candidate := id
		for suffix := 2; seen[candidate]; suffix++ {
			candidate = id + "_" + strconv.Itoa(suffix)
		}
		seen[candidate] = true
		object["id"] = candidate
	}
}

func (n *authoredNormalizer) sections(root map[string]any) {
	sections, _ := root["sections"].([]any)
	for _, raw := range sections {
		section, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		location := "section " + fmt.Sprint(section["id"])
		// A title of only spaces is left empty, for validation to reject.
		if title, ok := section["title"].(string); ok {
			section["title"] = strings.TrimSpace(title)
		}
		// A hunk may appear in several sections, but only once in each.
		hunks := []any{}
		seen := map[string]bool{}
		for _, id := range stringSlice(section["hunks"]) {
			if _, ok := n.hunks[id]; ok && !seen[id] {
				seen[id] = true
				hunks = append(hunks, id)
			}
		}
		section["hunks"] = hunks
		anchors := hunkAnchors(hunks)
		if summary, ok := section["summary"].(map[string]any); ok {
			fillAnchors(summary, "anchors", anchors)
		}
		// The host rates a mechanical section itself, and a section with
		// no current hunks takes no part in the score, so an assessment the
		// model gave either is dropped rather than repaired.
		if section["kind"] == "mechanical" || len(hunks) == 0 {
			delete(section, "assessment")
		} else if assessment, ok := section["assessment"].(map[string]any); ok {
			for _, reason := range Objects(assessment["reasons"]) {
				fillAnchors(reason, "anchors", anchors)
			}
			if factors, ok := assessment["factors"].([]any); ok {
				unique := []any{}
				for _, factor := range factors {
					if !slices.Contains(unique, factor) {
						unique = append(unique, factor)
					}
				}
				assessment["factors"] = unique
			}
		}
		items := map[string]bool{}
		// A risk keeps the anchors the model gave it: they decide which lines
		// the risk rates in the stored statistics, so they are never guessed.
		for _, risk := range Objects(section["risks"]) {
			items[fmt.Sprint(risk["id"])] = true
			riskAnchors, _ := risk["anchors"].([]any)
			n.tiles(location+" risk "+fmt.Sprint(risk["id"]), risk["tiles"], riskAnchors)
		}
		for _, evidence := range Objects(section["evidence"]) {
			items[fmt.Sprint(evidence["id"])] = true
			n.supportEvidenceStatus(evidence)
		}
		for _, block := range Objects(section["blocks"]) {
			switch block["kind"] {
			case "behavior":
				// Behavior anchors drive the inline code evidence. Keep their
				// selection model-authored instead of widening a missing choice
				// to every hunk in the section.
			case "decisions":
				for _, decision := range Objects(block["items"]) {
					items[fmt.Sprint(decision["id"])] = true
					fillAnchors(decision, "anchors", anchors)
				}
			case "entities", "groups", "dependency":
				for _, item := range Objects(block["items"]) {
					if _, present := item["hunks"]; present {
						item["hunks"] = n.knownHunks(item["hunks"])
					}
				}
			case "diagram", "code", "media", "images", "repro", "table":
				fillAnchors(block, "source", anchors)
			}
		}
		n.tiles(location, section["tiles"], anchors)
		// References are resolved with or without their prefix; one that names
		// no item stays, so validation asks for the repair instead of the
		// attention text being lost.
		if attention, ok := section["attention"].(map[string]any); ok {
			if refs, present := attention["refs"]; present {
				resolved := make([]any, 0)
				for _, ref := range stringSlice(refs) {
					if candidate, ok := resolveReference(ref, items); ok {
						ref = candidate
					}
					resolved = append(resolved, ref)
				}
				attention["refs"] = resolved
			}
		}
	}
}

// supportEvidenceStatus keeps a claim whose anchors cannot carry its status
// as a model assertion, as an unresolved anchor does. The conditional status
// rules are dropped from Codex's strict output schema, so the model cannot be
// relied on to meet them. Verified test evidence needs a test hunk or a
// recognized report, as semantic validation requires; a cited check that did
// not succeed stays for validation to reject, and an unknown one is asserted
// when anchors are resolved. A verified item without granularity verifies its
// job.
func (n *authoredNormalizer) supportEvidenceStatus(evidence map[string]any) {
	anchors := Objects(evidence["anchors"])
	cites := func(accept func(kind, id string) bool) bool {
		return slices.ContainsFunc(anchors, func(anchor map[string]any) bool {
			id, _ := anchor["id"].(string)
			return accept(fmt.Sprint(anchor["type"]), id)
		})
	}
	supported := true
	switch evidence["status"] {
	case "verified":
		supported = cites(func(kind, _ string) bool { return kind == "check" })
		if evidence["kind"] == "test" {
			supported = supported && cites(func(kind, id string) bool {
				return kind == "hunk" && n.hunks[id].Role == "test" || kind == "bot_report" && slices.ContainsFunc(n.bundle.Conversation, func(c bundlepkg.ConversationItem) bool { return c.ID == id && c.BotReport })
			})
		}
		if _, present := evidence["granularity"]; supported && !present {
			evidence["granularity"] = "job"
		}
	case "observed":
		supported = cites(func(kind, _ string) bool { return kind == "session_event" })
	case "reported":
		supported = len(anchors) > 0 && slices.Contains([]string{"author_human", "author_agent", "agent_review", "human_review"}, fmt.Sprint(evidence["source"]))
	}
	if !supported {
		evidence["status"] = "asserted"
		delete(evidence, "granularity")
	}
}

func (n *authoredNormalizer) knownHunks(value any) []any {
	kept := []any{}
	for _, id := range stringSlice(value) {
		if _, known := n.hunks[id]; known {
			kept = append(kept, id)
		}
	}
	return kept
}

// resolveReference accepts a section item's identifier with or without its
// prefix and reports whether the section holds that item.
func resolveReference(ref string, items map[string]bool) (string, bool) {
	if items[ref] {
		return ref, true
	}
	for _, prefix := range []string{"ev", "rsk", "dec"} {
		candidate := prefix + "_" + strings.TrimPrefix(ref, prefix+"_")
		if items[candidate] {
			return candidate, true
		}
	}
	return "", false
}

func hunkAnchors(hunks []any) []any {
	anchors := make([]any, 0, len(hunks))
	for _, id := range hunks {
		anchors = append(anchors, map[string]any{"type": "hunk", "id": id})
	}
	return anchors
}

// fillAnchors gives an owner with no anchors of its own the section's hunks.
func fillAnchors(owner map[string]any, key string, fallback []any) {
	if existing, _ := owner[key].([]any); len(existing) > 0 || len(fallback) == 0 {
		return
	}
	copied := make([]any, 0, len(fallback))
	for _, anchor := range fallback {
		source := anchor.(map[string]any)
		clone := make(map[string]any, len(source))
		maps.Copy(clone, source)
		copied = append(copied, clone)
	}
	owner[key] = copied
}

// Objects returns the JSON objects in a decoded list, skipping other items.
//
// It is exported for hosts that run output redaction or the generate-and-repair loop themselves.
func Objects(value any) []map[string]any {
	list, _ := value.([]any)
	result := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

// tiles resolves each code source: the revision named by role becomes its
// SHA, a missing review_version becomes this review's, and an excerpt named
// by hunk headers gets its lines from the bundle's comparison. Inherited
// sources from earlier reviews are carried unchanged.
func (n *authoredNormalizer) tiles(location string, value any, fallbackAnchors []any) {
	for _, tile := range Objects(value) {
		fillAnchors(tile, "anchors", fallbackAnchors)
		for i, source := range Objects(tile["code"]) {
			where := fmt.Sprintf("%s tile %v source %d (%v)", location, tile["id"], i+1, source["path"])
			switch source["revision"] {
			case tileRevisionHead:
				source["revision"] = n.bundle.PullRequest.HeadSHA
			case tileRevisionMergeBase:
				source["revision"] = n.bundle.PullRequest.MergeBaseSHA
			}
			version, _ := source["review_version"].(float64)
			if version <= 0 {
				source["review_version"] = n.version
				version = float64(n.version)
			}
			if int(version) != n.version {
				continue
			}
			path, _ := source["path"].(string)
			root := ""
			switch source["revision"] {
			case n.bundle.PullRequest.HeadSHA:
				root = "head"
			case n.bundle.PullRequest.MergeBaseSHA:
				root = "merge-base"
			}
			if root != "" {
				n.deriveExcerpt(where, source, path, root)
			}
		}
	}
}

func (n *authoredNormalizer) deriveExcerpt(where string, source map[string]any, path, root string) {
	diff, _ := source["diff"].(map[string]any)
	hunks := Objects(diff["hunks"])
	if len(hunks) == 0 {
		return
	}
	available := comparisonHunks(n.bundle, path, root)
	// A hunk that cannot be derived is dropped with its finding, so the
	// strict contract does not report the same excerpt a second time.
	kept := make([]any, 0, len(hunks))
	for i, hunk := range hunks {
		header, _ := hunk["header"].(string)
		old, oldCount, next, newCount, ok := TileHunkRange(header)
		if !ok {
			n.findings = append(n.findings, fmt.Sprintf("%s: hunk %d header %q is not of the form @@ -old,count +new,count @@", where, i+1, header))
			continue
		}
		// An excerpt is a run of adjacent lines from one patch hunk. Removals
		// are placed by old numbers and additions by new ones, so a selection
		// touching two hunks, or leaving a hole inside one, would splice lines
		// the comparison never showed together.
		var selected []TileLine
		sources := 0
		adjacent := true
		for _, patchHunk := range available {
			var picked []TileLine
			first, last := -1, -1
			for j, line := range patchHunk {
				inOld := line.Kind != "add" && line.Old >= old && line.Old < old+oldCount
				inNew := line.Kind != "remove" && line.New >= next && line.New < next+newCount
				if line.Kind == "context" && inOld && inNew || line.Kind == "remove" && inOld || line.Kind == "add" && inNew {
					picked = append(picked, line)
					if first < 0 {
						first = j
					}
					last = j
				}
			}
			if len(picked) > 0 {
				sources++
				selected = picked
				adjacent = last-first+1 == len(picked)
			}
		}
		switch {
		case sources == 0:
			n.findings = append(n.findings, fmt.Sprintf("%s: hunk %d header %q selects no line of the supplied comparison for this path; use a header inside one of the bundle's patch hunks", where, i+1, header))
			continue
		case sources > 1:
			n.findings = append(n.findings, fmt.Sprintf("%s: hunk %d header %q selects lines from more than one patch hunk; use one header per patch hunk", where, i+1, header))
			continue
		case !adjacent:
			n.findings = append(n.findings, fmt.Sprintf("%s: hunk %d header %q selects lines that are not adjacent in the comparison; use a header covering one run of lines", where, i+1, header))
			continue
		}
		hunk["header"], hunk["lines"] = excerptFromLines(old, next, selected)
		kept = append(kept, hunk)
	}
	if len(kept) == 0 {
		delete(source, "diff")
		return
	}
	diff["hunks"] = kept
}

// excerptFromLines writes the header the selected lines actually cover and the
// lines in the contract's shape.
func excerptFromLines(oldStart, newStart int, lines []TileLine) (string, []any) {
	oldCount, newCount := 0, 0
	encoded := make([]any, 0, len(lines))
	for _, line := range lines {
		item := map[string]any{"kind": line.Kind, "text": line.Text}
		if line.Kind != "add" {
			if oldCount == 0 {
				oldStart = line.Old
			}
			oldCount++
			item["old_line"] = line.Old
		}
		if line.Kind != "remove" {
			if newCount == 0 {
				newStart = line.New
			}
			newCount++
			item["new_line"] = line.New
		}
		encoded = append(encoded, item)
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount), encoded
}

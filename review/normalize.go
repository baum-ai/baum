package review

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// authoredNormalizer maps a document the lenient contract accepts onto the
// strict one. Every step is deterministic and needs nothing from the model:
// nulls and unknown keys go, canonical empties and constants are filled,
// identifiers take their prefix and spelling, hunk lists lose unknown hunks
// and repeats within a section, empty anchors other than a risk's or a
// behavior step's default to the enclosing section's hunks, attention references are resolved, evidence
// whose anchors cannot carry its status becomes asserted, and code
// sources get their SHA, review version and excerpt lines from the bundle,
// and the summary context loses empty parts and references to files the
// checkout does not hold. What it cannot decide stays for validation to report.
type authoredNormalizer struct {
	defs     map[string]any
	bundle   *bundlepkg.Bundle
	version  int
	hunks    map[string]bundlepkg.Hunk
	findings []string
	// probes counts the taken suffixes dedupe tried, which tests read to
	// bound its work.
	probes int
	// identifier, when set, sees each value walk respells as an identifier,
	// as it was before the respelling, with the respelling and the schema
	// node that closes it.
	identifier func(value, normalized string, schema map[string]any)
}

func normalizeAuthored(value any, strict map[string]any, bundle *bundlepkg.Bundle, directory string) (any, []string) {
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
	reviewMinutes(root)
	summaryContext(root, directory)
	n.hunkAnchorLines(root)
	n.sections(root)
	return root, n.findings
}

// authoredIdentifier is a value normalizeAuthored respells as an identifier:
// its text as the model wrote it, the respelling, and the maxLength the
// strict schema bounds the respelling to, or 0 without one.
type authoredIdentifier struct {
	text, normalized string
	limit            int
}

// authoredIdentifiers lists, sorted and once each, the values that
// normalizeAuthored respells as identifiers (see identifierRule.normalize), as
// the model wrote them. It takes them from the same walk, so it names exactly
// the positions normalization rewrites. The walk changes value as
// normalization does, so callers pass a copy they discard.
func authoredIdentifiers(value any, strict map[string]any) []authoredIdentifier {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	defs, _ := strict["$defs"].(map[string]any)
	seen := map[authoredIdentifier]bool{}
	n := &authoredNormalizer{defs: defs, identifier: func(value, normalized string, schema map[string]any) {
		limit, _ := jsonInt(schema["maxLength"])
		seen[authoredIdentifier{text: value, normalized: normalized, limit: max(limit, 0)}] = true
	}}
	n.walk(root, strict)
	return slices.SortedFunc(maps.Keys(seen), func(a, b authoredIdentifier) int {
		return cmp.Or(strings.Compare(a.text, b.text), strings.Compare(a.normalized, b.normalized), cmp.Compare(a.limit, b.limit))
	})
}

// reviewMinutes keeps the model's review time estimate within the schema's
// range: it is a rough figure, not worth a repair call. A fraction rounds to
// whole minutes and a value that is not a number is dropped.
func reviewMinutes(root map[string]any) {
	summary, _ := root["summary"].(map[string]any)
	value, present := summary["review_minutes"]
	if !present || value == nil {
		return
	}
	number, ok := value.(float64)
	if !ok || math.IsInf(number, 0) || math.IsNaN(number) {
		delete(summary, "review_minutes")
		return
	}
	summary["review_minutes"] = min(max(math.Round(number), minReviewMinutes), maxReviewMinutes)
}

const minReviewMinutes, maxReviewMinutes = 1, 240

// summaryContext tidies the model's summary context so that references never
// cost a repair: an optional part without text is dropped, references that
// are not a list become an empty one, and each part's references are cleaned
// (cleanContextRefs). A missing context or area stays for validation to
// report.
func summaryContext(root map[string]any, directory string) {
	summary, _ := root["summary"].(map[string]any)
	context, ok := summary["context"].(map[string]any)
	if !ok {
		return
	}
	trees := openCheckoutTrees(directory)
	defer func() {
		for _, tree := range trees {
			tree.Close()
		}
	}()
	for _, name := range contextParts {
		part, ok := context[name].(map[string]any)
		if !ok {
			continue
		}
		text, isText := part["text"].(string)
		if isText {
			text = strings.TrimSpace(text)
			part["text"] = text
		}
		if name != "area" && text == "" {
			delete(context, name)
			continue
		}
		refs, _ := part["refs"].([]any)
		part["refs"] = cleanContextRefs(refs, trees)
	}
}

var contextParts = []string{"area", "reached_by", "contracts", "related"}

const maxContextRefs = 8

// cleanContextRefs makes each reference's path repository-relative and drops
// a reference that is malformed, leaves the repository or repeats an earlier
// one. With checkout trees it also drops a path no tree holds, so a reference
// never names an invented file. It keeps at most maxContextRefs.
func cleanContextRefs(refs []any, trees []*os.Root) []any {
	kept := []any{}
	type key struct {
		path   string
		symbol any
	}
	seen := map[key]bool{}
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		written, _ := ref["path"].(string)
		path, ok := contextRefPath(written, trees)
		if !ok {
			continue
		}
		var symbol any
		if name, ok := ref["symbol"].(string); ok && strings.TrimSpace(name) != "" {
			symbol = strings.TrimSpace(name)
		}
		if seen[key{path, symbol}] {
			continue
		}
		seen[key{path, symbol}] = true
		kept = append(kept, map[string]any{"path": path, "symbol": symbol})
		if len(kept) == maxContextRefs {
			break
		}
	}
	return kept
}

// contextRefPath tidies a reference path: trimmed, without a leading "./" or
// "/", cleaned, inside the repository and not the repository itself. Without
// checkout trees the tidied path is kept as written. With them it is kept
// when a tree holds it; otherwise the model may have written it from the run
// directory (head/api/handler.go), so a leading tree name is stripped and the
// rest kept when a tree holds that.
func contextRefPath(path string, trees []*os.Root) (string, bool) {
	path = strings.TrimSpace(path)
	for {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(path, "./"), "/")
		if trimmed == path {
			break
		}
		path = trimmed
	}
	if len(trees) == 0 {
		return localRefPath(path)
	}
	if local, ok := localRefPath(path); ok && treesHold(trees, local) {
		return local, true
	}
	for _, tree := range checkoutTrees {
		if rest, ok := strings.CutPrefix(path, tree+"/"); ok {
			if local, ok := localRefPath(rest); ok && treesHold(trees, local) {
				return local, true
			}
		}
	}
	return "", false
}

// localRefPath cleans a repository-relative path, refusing one that is empty,
// leaves the repository, uses a backslash or names the repository itself.
func localRefPath(path string) (string, bool) {
	if path == "" || !filepath.IsLocal(path) || strings.Contains(path, "\\") {
		return "", false
	}
	path = filepath.ToSlash(filepath.Clean(path))
	return path, path != "."
}

// checkoutTrees are the run directory's checkout trees, as agent.sourceContext
// reads them.
var checkoutTrees = []string{"head", "merge-base"}

// openCheckoutTrees opens the run directory's checkout trees that exist. A
// stack review's directory, for one, holds none.
func openCheckoutTrees(directory string) []*os.Root {
	if directory == "" {
		return nil
	}
	var trees []*os.Root
	for _, tree := range checkoutTrees {
		if root, err := os.OpenRoot(filepath.Join(directory, tree)); err == nil {
			trees = append(trees, root)
		}
	}
	return trees
}

// treesHold reports whether a checkout tree has a file or directory at the
// repository-relative path. The trees are roots, so a symbolic link that
// leads out of its tree holds nothing.
func treesHold(trees []*os.Root, path string) bool {
	for _, tree := range trees {
		if info, err := tree.Stat(filepath.FromSlash(path)); err == nil && (info.Mode().IsRegular() || info.IsDir()) {
			return true
		}
	}
	return false
}

// hunkAnchorLines treats model-selected line ranges as optional precision. A
// usable overlap is made canonical; a range that cannot select the hunk's
// right side is removed so consumers fall back to showing the full hunk.
func (n *authoredNormalizer) hunkAnchorLines(root map[string]any) {
	clampHunkAnchorLines(root, n.hunks)
}

// clampHunkAnchorLines applies hunkAnchorLines' rule to every hunk anchor in
// value against hunks. A hunk ID names the hunk's content, so it survives a
// rebase that moves the hunk; an anchor carried from an earlier version can
// then name the right hunk at lines it no longer occupies.
func clampHunkAnchorLines(value any, hunks map[string]bundlepkg.Hunk) {
	walkJSON(value, func(candidate map[string]any) {
		if candidate["type"] != "hunk" {
			return
		}
		id, _ := candidate["id"].(string)
		hunk, known := hunks[id]
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
	schema = resolveSchema(schema, n.defs)
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
				if !acceptsNull(resolveSchema(prop, n.defs)) {
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
				normalized := rule.normalize(v)
				if n.identifier != nil {
					n.identifier(v, normalized, schema)
				}
				return normalized
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
		branch = resolveSchema(branch, n.defs)
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
	next := map[string]int{}
	for _, raw := range list {
		object, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		schema := resolveSchema(items, n.defs)
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
		// The first free suffix only grows, so each repeat resumes from the
		// last one taken instead of trying 2, 3, ... again.
		candidate := id
		if seen[candidate] {
			suffix := max(next[id], 2)
			for candidate = id + "_" + strconv.Itoa(suffix); seen[candidate]; candidate = id + "_" + strconv.Itoa(suffix) {
				suffix++
				n.probes++
			}
			next[id] = suffix + 1
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
			for _, reason := range jsonObjects(assessment["reasons"]) {
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
		for _, risk := range jsonObjects(section["risks"]) {
			items[fmt.Sprint(risk["id"])] = true
			riskAnchors, _ := risk["anchors"].([]any)
			n.tiles(location+" risk "+fmt.Sprint(risk["id"]), risk["tiles"], riskAnchors)
		}
		for _, evidence := range jsonObjects(section["evidence"]) {
			items[fmt.Sprint(evidence["id"])] = true
			n.supportEvidenceStatus(evidence)
		}
		dropEmptyItemBlocks(section)
		for _, block := range jsonObjects(section["blocks"]) {
			switch block["kind"] {
			case "behavior":
				// Behavior anchors drive the inline code evidence. Keep their
				// selection model-authored instead of widening a missing choice
				// to every hunk in the section.
			case "decisions":
				for _, decision := range jsonObjects(block["items"]) {
					items[fmt.Sprint(decision["id"])] = true
					fillAnchors(decision, "anchors", anchors)
					// false says what an absent flag says. A human decision
					// marked approved contradicts itself, so it stays for
					// validation to ask which the model meant.
					if decision["approved_by_human"] == false {
						delete(decision, "approved_by_human")
					}
					// An empty string says what an absent field says: no
					// rationale, trigger or evidence was extracted.
					for _, key := range []string{"rationale", "trigger", "evidence"} {
						if text, ok := decision[key].(string); ok && strings.TrimSpace(text) == "" {
							delete(decision, key)
						}
					}
				}
			case "entities", "groups", "dependency":
				for _, item := range jsonObjects(block["items"]) {
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

// dropEmptyItemBlocks removes item-list blocks with no items. Such a block
// carries nothing, and the strict contract requires at least one item, so
// rejecting the document would only cost repair calls. Blocks with other
// required content are left for validation.
func dropEmptyItemBlocks(section map[string]any) {
	blocks, ok := section["blocks"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok {
			switch block["kind"] {
			case "entities", "groups", "decisions", "dependency", "included_prs":
				if items, ok := block["items"].([]any); !ok && block["items"] == nil || ok && len(items) == 0 {
					continue
				}
			}
		}
		kept = append(kept, raw)
	}
	section["blocks"] = kept
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
	anchors := jsonObjects(evidence["anchors"])
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

// jsonObjects returns the JSON objects in a decoded list, skipping other items.
func jsonObjects(value any) []map[string]any {
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
	for _, tile := range jsonObjects(value) {
		fillAnchors(tile, "anchors", fallbackAnchors)
		for i, source := range jsonObjects(tile["code"]) {
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
	hunks := jsonObjects(diff["hunks"])
	if len(hunks) == 0 {
		return
	}
	available := comparisonHunks(n.bundle, path, root)
	// A hunk that cannot be derived is dropped with its finding, so the
	// strict contract does not report the same excerpt a second time.
	kept := make([]any, 0, len(hunks))
	for i, hunk := range hunks {
		header, _ := hunk["header"].(string)
		old, oldCount, next, newCount, ok := tileHunkRange(header)
		if !ok {
			n.findings = append(n.findings, fmt.Sprintf("%s: hunk %d header %q is not of the form @@ -old,count +new,count @@", where, i+1, header))
			continue
		}
		// An excerpt is a run of adjacent lines from one patch hunk. Removals
		// are placed by old numbers and additions by new ones, so a selection
		// touching two hunks, or leaving a hole inside one, would splice lines
		// the comparison never showed together.
		var selected []tileLine
		sources := 0
		adjacent := true
		for _, patchHunk := range available {
			var picked []tileLine
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
func excerptFromLines(oldStart, newStart int, lines []tileLine) (string, []any) {
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

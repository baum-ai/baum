package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func resolvedSchema(t *testing.T, encoded []byte) *jsonschema.Resolved {
	t.Helper()
	var schema jsonschema.Schema
	checkNoError(t, json.Unmarshal(encoded, &schema))
	resolved, err := schema.Resolve(nil)
	checkNoError(t, err)
	return resolved
}

// The model-facing contract must be strictly looser than the stored one for
// every union, optional and identifier, and must still validate against a
// schema library.
func TestLenientContractRelaxesUnionsOptionalsAndIdentifiers(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	var lenient map[string]any
	checkNoError(t, json.Unmarshal(v.Schema, &lenient))
	defs := lenient["$defs"].(map[string]any)
	for _, name := range []string{"anchor", "block", "tile", "tileCodeLine"} {
		def := defs[name].(map[string]any)
		if _, still := def["oneOf"]; still {
			t.Fatalf("%s keeps oneOf", name)
		}
		if len(def["allOf"].([]any)) < 3 || !strings.Contains(def["description"].(string), "Required fields per value") {
			t.Fatalf("%s is not a discriminated union: %v", name, def)
		}
	}
	anchor := defs["anchor"].(map[string]any)["properties"].(map[string]any)
	if !acceptsNull(anchor["sha"].(map[string]any)) || !acceptsNull(anchor["lines"].(map[string]any)) {
		t.Fatalf("anchor fields of other types must accept null: %v", anchor)
	}
	if description, _ := anchor["lines"].(map[string]any)["description"].(string); !strings.Contains(description, "right/new-side") || !strings.Contains(description, "full hunk") {
		t.Fatalf("hunk line range lacks its coordinate contract: %q", description)
	}
	var bestEffort any
	checkNoError(t, json.Unmarshal(lenientFixture(), &bestEffort))
	riskAnchor := bestEffort.(map[string]any)["sections"].([]any)[0].(map[string]any)["risks"].([]any)[0].(map[string]any)["anchors"].([]any)[0].(map[string]any)
	riskAnchor["lines"] = []any{0, 2}
	checkNoError(t, resolvedSchema(t, v.Schema).Validate(bestEffort))
	riskAnchor["lines"] = []any{2}
	checkNoError(t, resolvedSchema(t, v.Schema).Validate(bestEffort))
	section := defs["section"].(map[string]any)
	for _, required := range stringSlice(section["required"]) {
		if required == "risks" || required == "evidence" || required == "blocks" || required == "attention" || required == "hunks" {
			t.Fatalf("section still requires %s, which has a canonical empty value", required)
		}
	}
	if len(stringSlice(lenient["required"])) != 2 || containsString(stringSlice(lenient["required"]), "schema") {
		t.Fatalf("root requires %v", lenient["required"])
	}
	risk := section["properties"].(map[string]any)["risks"].(map[string]any)["items"].(map[string]any)
	if _, pattern := risk["properties"].(map[string]any)["id"].(map[string]any)["pattern"]; pattern {
		t.Fatal("risk id keeps its prefix pattern")
	}
	if !containsString(stringSlice(risk["required"]), "anchors") || acceptsNull(risk["properties"].(map[string]any)["anchors"].(map[string]any)) {
		t.Fatal("risk anchors must stay the model's, since they decide which lines a risk rates")
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"risk with only a file anchor", func(document map[string]any) {
			risk := document["sections"].([]any)[0].(map[string]any)["risks"].([]any)[0].(map[string]any)
			risk["anchors"] = []any{map[string]any{"type": "file", "path": "main.go"}}
		}},
		{"behavior without anchors", func(document map[string]any) {
			step := document["sections"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
			delete(step, "anchors")
		}},
		{"behavior with only a file anchor", func(document map[string]any) {
			step := document["sections"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
			step["anchors"] = []any{map[string]any{"type": "file", "path": "main.go"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			checkNoError(t, json.Unmarshal(lenientFixture(), &document))
			tc.mutate(document)
			if err := resolvedSchema(t, v.Schema).Validate(document); err == nil {
				t.Fatal("model-facing contract accepted evidence without a hunk anchor")
			}
		})
	}
	summaryAnchors := section["properties"].(map[string]any)["summary"].(map[string]any)
	if containsString(stringSlice(summaryAnchors["required"]), "anchors") {
		t.Fatal("summary anchors stay required although the platform defaults them")
	}
	file := defs["tileCodeFile"].(map[string]any)
	if containsString(stringSlice(file["required"]), "review_version") {
		t.Fatal("review_version stays required")
	}
	hunk := file["properties"].(map[string]any)["diff"].(map[string]any)["properties"].(map[string]any)["hunks"].(map[string]any)["items"].(map[string]any)
	if containsString(stringSlice(hunk["required"]), "lines") {
		t.Fatal("excerpt lines stay required")
	}
	summaryText := section["properties"].(map[string]any)["summary"].(map[string]any)["properties"].(map[string]any)["text"].(map[string]any)
	if _, capped := summaryText["maxLength"]; capped {
		t.Fatal("summary text keeps its length cap")
	}
	if strings.Contains(string(v.Schema), `"observed_sessions"`) || strings.Contains(string(v.Schema), `"derived":`) || strings.Contains(string(v.Schema), `"prompt_version"`) {
		t.Fatal("lenient contract exposes derived fields")
	}
	// A strict document is still a lenient one.
	var strictDocument any
	checkNoError(t, json.Unmarshal(historyCandidate(t, []any{historySection("a", testHunk, "rsk_one")}), &strictDocument))
	checkNoError(t, resolvedSchema(t, v.Schema).Validate(strictDocument))
}

func lenientFixture() []byte {
	// Everything the lenient contract tolerates and the strict one rejects:
	// no schema constant, nulls for optionals, missing empty collections,
	// unprefixed and oddly spelled identifiers, a repeated identifier, a
	// duplicated and an unknown hunk, unanchored items, an unprefixed
	// attention reference, a revision named by role, no review_version and an
	// excerpt named by header only.
	return []byte(`{
	  "summary": {"text": "Changes parser behavior.", "review_exchange": {"summary": "No reviews."}, "change_kind": "feature", "release_note": null, "attention": null, "authorship": {"declared": "unknown", "declared_tools": null}},
	  "sections": [{
	    "id": "Parser Core", "title": "Parser", "kind": "logical",
	    "hunks": ["h_0123456789ab", "h_0123456789ab", "h_ffffffffffff"],
	    "summary": {"text": "Adds the parser."},
	    "attention": {"text": "Check the risk.", "refs": ["Bad Input"]},
	    "risks": [
	      {"id": "Bad Input", "tier": "medium", "category": "Input Validation", "source": "model", "text": "Malformed input aborts.", "finding": null, "anchors": [{"type": "hunk", "id": "h_0123456789ab", "lines": null}]},
	      {"id": "bad-input", "tier": "low", "category": "robustness", "source": "model", "text": "Second risk.", "anchors": [{"type": "hunk", "id": "h_0123456789ab"}, {"type": "file", "path": "main.go", "lines": null}]}
	    ],
	    "blocks": [
	      {"kind": "behavior", "steps": [{"id": "Step One", "text": "Parses input.", "anchors": [{"type": "hunk", "id": "h_0123456789ab"}], "evidence": ["Unit Test"]}]},
	      {"kind": "code", "language": "go", "content": "code", "caption": null}
	    ],
	    "tiles": [{"id": "Bug Fix", "kind": "bug-fix", "data": {"bug": "Invalid input aborts parsing.", "fix": "Reject invalid input before parsing."},
	      "code": [{"path": "main.go", "revision": "head", "why": "Parser implementation", "diff": {"hunks": [{"header": "@@ -1,2 +1,2 @@"}]}}]}],
	    "evidence": [{"id": "Unit Test", "kind": "test", "claim": "Covered by tests.", "status": "asserted", "source": "model", "anchors": [{"type": "hunk", "id": "h_0123456789ab"}], "detail": null}]
	  }]
	}`)
}

func TestNormalizationMapsLenientDocumentOntoStrictContract(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: head, MergeBaseSHA: base}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Patch: "@@ -1,2 +1,3 @@\n before\n-old\n+new\n+added\n"}}}
	directory := t.TempDir()
	for _, root := range []string{"head", "merge-base"} {
		checkNoError(t, os.Mkdir(filepath.Join(directory, root), 0700))
		checkNoError(t, os.WriteFile(filepath.Join(directory, root, "main.go"), []byte("source"), 0600))
	}
	var lenientDocument any
	checkNoError(t, json.Unmarshal(lenientFixture(), &lenientDocument))
	if err := resolvedSchema(t, v.Schema).Validate(lenientDocument); err != nil {
		t.Fatalf("lenient contract rejects the fixture: %v", err)
	}
	if err := v.resolved.Validate(lenientDocument); err == nil {
		t.Fatal("strict contract accepted the un-normalized fixture")
	}

	prepared, findings := v.Prepare(lenientFixture(), bundle, directory)
	checkEqual(t, "", findings)
	var doc map[string]any
	checkNoError(t, json.Unmarshal(prepared, &doc))
	checkEqual(t, "baum.review/1", doc["schema"])
	summary := doc["summary"].(map[string]any)
	checkEqual(t, 0, len(summary["implements"].([]any)))
	if summary["breaking_change"] != nil {
		t.Fatalf("breaking_change = %v", summary["breaking_change"])
	}
	// A null in place of a required array becomes the canonical empty array.
	assertSameDocumentJSON(t, []byte(`[]`), tileJSON(t, summary["authorship"].(map[string]any)["declared_tools"]))
	section := doc["sections"].([]any)[0].(map[string]any)
	checkEqual(t, "sec_parser_core", section["id"])
	assertSameDocumentJSON(t, []byte(`["h_0123456789ab"]`), tileJSON(t, section["hunks"]))
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, section["summary"].(map[string]any)["anchors"]))
	risks := section["risks"].([]any)
	first, second := risks[0].(map[string]any), risks[1].(map[string]any)
	checkEqual(t, "rsk_bad_input", first["id"])
	checkEqual(t, "rsk_bad_input_2", second["id"])
	checkEqual(t, "input_validation", first["category"])
	if finding, present := first["finding"]; present && finding != nil {
		t.Fatalf("finding = %v", finding)
	}
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, first["anchors"]))
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"},{"type":"file","path":"main.go"}]`), tileJSON(t, second["anchors"]))
	assertSameDocumentJSON(t, []byte(`{"text":"Check the risk.","refs":["rsk_bad_input"]}`), tileJSON(t, section["attention"]))
	blocks := section["blocks"].([]any)
	step := blocks[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	checkEqual(t, "beh_step_one", step["id"])
	assertSameDocumentJSON(t, []byte(`["ev_unit_test"]`), tileJSON(t, step["evidence"]))
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, step["anchors"]))
	code := blocks[1].(map[string]any)
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, code["source"]))
	if _, present := code["caption"]; present {
		t.Fatal("null caption was kept")
	}
	checkEqual(t, "ev_unit_test", section["evidence"].([]any)[0].(map[string]any)["id"])
	tile := section["tiles"].([]any)[0].(map[string]any)
	checkEqual(t, "tile_bug_fix", tile["id"])
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, tile["anchors"]))
	source := tile["code"].([]any)[0].(map[string]any)
	checkEqual(t, head, source["revision"])
	checkEqual(t, float64(1), source["review_version"])
	// The header asked for new lines 1-2, so the third added line stays out.
	assertSameDocumentJSON(t, []byte(`{"hunks":[{"header":"@@ -1,2 +1,2 @@","lines":[{"kind":"context","text":"before","old_line":1,"new_line":1},{"kind":"remove","text":"old","old_line":2},{"kind":"add","text":"new","new_line":2}]}]}`), tileJSON(t, source["diff"]))

	var strictDocument any
	checkNoError(t, json.Unmarshal(prepared, &strictDocument))
	checkNoError(t, v.resolved.Validate(strictDocument))
	again, findings := v.Prepare(prepared, bundle, directory)
	checkEqual(t, "", findings)
	assertSameDocumentJSON(t, prepared, again)
}

func TestNormalizationRepairsHunkAnchorLineRanges(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", StartLine: 10, LineCount: 3}}}
	documentWithLines := func(lines []any) map[string]any {
		var document map[string]any
		checkNoError(t, json.Unmarshal(authoredFixture(), &document))
		section := document["sections"].([]any)[0].(map[string]any)
		section["risks"] = []any{map[string]any{
			"id": "rsk_range", "tier": "medium", "category": "correctness", "source": "model", "text": "Range risk.",
			"anchors": []any{map[string]any{"type": "hunk", "id": testHunk, "lines": lines}},
		}}
		return document
	}

	for _, tc := range []struct {
		name  string
		lines []any
		want  string
	}{
		{"reversed", []any{12, 10}, `[10,12]`},
		{"partially before", []any{0, 11}, `[10,11]`},
		{"partially after", []any{11, 20}, `[11,12]`},
		{"outside", []any{1, 2}, ``},
		{"wrong length", []any{10}, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := documentWithLines(tc.lines)
			prepared, findings := v.Prepare(tileJSON(t, document), bundle, t.TempDir())
			checkEqual(t, "", findings)
			var normalized map[string]any
			checkNoError(t, json.Unmarshal(prepared, &normalized))
			got := normalized["sections"].([]any)[0].(map[string]any)["risks"].([]any)[0].(map[string]any)["anchors"].([]any)[0].(map[string]any)
			if tc.want == "" {
				if _, present := got["lines"]; present {
					t.Fatalf("unusable lines were retained: %v", got["lines"])
				}
				return
			}
			assertSameDocumentJSON(t, []byte(tc.want), tileJSON(t, got["lines"]))
		})
	}

	t.Run("deletion only", func(t *testing.T) {
		deletion := *bundle
		deletion.Diff = []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", StartLine: 10}}
		document := documentWithLines([]any{10, 10})
		prepared, findings := v.Prepare(tileJSON(t, document), &deletion, t.TempDir())
		checkEqual(t, "", findings)
		var normalized map[string]any
		checkNoError(t, json.Unmarshal(prepared, &normalized))
		anchor := normalized["sections"].([]any)[0].(map[string]any)["risks"].([]any)[0].(map[string]any)["anchors"].([]any)[0].(map[string]any)
		if _, present := anchor["lines"]; present {
			t.Fatalf("deletion-only range was retained: %v", anchor["lines"])
		}
	})
}

func TestExcerptDerivationNarrowsHeadersAndReportsUnusableOnes(t *testing.T) {
	head := strings.Repeat("a", 40)
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: head, MergeBaseSHA: strings.Repeat("b", 40)}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Patch: "@@ -1,2 +1,2 @@\n a\n-b\n+B\n@@ -10,2 +10,2 @@\n c\n-d\n+D\n"}}}
	directory := t.TempDir()
	checkNoError(t, os.Mkdir(filepath.Join(directory, "head"), 0700))
	checkNoError(t, os.WriteFile(filepath.Join(directory, "head", "main.go"), []byte("source"), 0600))
	for _, tc := range []struct {
		name, header, want, finding string
	}{
		{"exact", "@@ -10,2 +10,2 @@", "@@ -10,2 +10,2 @@", ""},
		{"wider than the patch hunk", "@@ -8,6 +8,6 @@", "@@ -10,2 +10,2 @@", ""},
		{"single line", "@@ -2 +2 @@", "@@ -2,1 +2,1 @@", ""},
		{"outside", "@@ -30,2 +30,2 @@", "", "selects no line"},
		{"across two patch hunks", "@@ -1,11 +1,11 @@", "", "more than one patch hunk"},
		// Removals placed by old numbers in one hunk and additions by new numbers in another.
		{"spliced from two patch hunks", "@@ -2,1 +11,1 @@", "", "more than one patch hunk"},
		{"malformed", "lines 1-2", "", "is not of the form"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &authoredNormalizer{bundle: bundle, version: 1}
			source := map[string]any{"path": "main.go", "diff": map[string]any{"hunks": []any{map[string]any{"header": tc.header}}}}
			n.deriveExcerpt("tile", source, "main.go", "head")
			if tc.finding != "" {
				if len(n.findings) != 1 || !strings.Contains(n.findings[0], tc.finding) || source["diff"] != nil {
					t.Fatalf("findings=%v diff=%v", n.findings, source["diff"])
				}
				return
			}
			hunk := source["diff"].(map[string]any)["hunks"].([]any)[0].(map[string]any)
			if len(n.findings) != 0 || hunk["header"] != tc.want {
				t.Fatalf("findings=%v header=%v", n.findings, hunk["header"])
			}
			var derived TileCode
			checkNoError(t, json.Unmarshal(tileJSON(t, map[string]any{"path": "main.go", "revision": head, "review_version": 1, "diff": source["diff"]}), &derived))
			checkEqual(t, "", validateTileSource(derived, bundle, directory, 1, nil))
		})
	}
}

func TestExcerptDerivationRejectsHolesInsideOnePatchHunk(t *testing.T) {
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: strings.Repeat("a", 40)}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Patch: "@@ -1,4 +1,4 @@\n a\n-b\n c\n-d\n+D\n"}}}
	n := &authoredNormalizer{bundle: bundle, version: 1}
	// Old line 2 (the removal of b) and new line 3 (the addition of D) are
	// separated by the context line c and the removal of d.
	source := map[string]any{"path": "main.go", "diff": map[string]any{"hunks": []any{map[string]any{"header": "@@ -2,1 +3,1 @@"}}}}
	n.deriveExcerpt("tile", source, "main.go", "head")
	if len(n.findings) != 1 || !strings.Contains(n.findings[0], "not adjacent") || source["diff"] != nil {
		t.Fatalf("findings=%v diff=%v", n.findings, source["diff"])
	}
}

// What normalization must leave for the model: a risk without anchors, a
// reference naming no item and a new section with no hunks at all.
func TestNormalizationLeavesRejectionsForRiskAnchorsReferencesAndEmptySections(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	for _, tc := range []struct {
		name, finding string
		mutate        func(doc map[string]any)
	}{
		{"risk without anchors", `missing properties: ["anchors"]`, func(doc map[string]any) {
			section := doc["sections"].([]any)[0].(map[string]any)
			section["risks"] = []any{map[string]any{"id": "rsk_one", "tier": "high", "category": "safety", "source": "model", "text": "Unanchored."}}
		}},
		{"attention naming no item", `section 1 (sec_parser) attention ref "ev_missing": does not resolve to evidence, risk, or decision in this section`, func(doc map[string]any) {
			section := doc["sections"].([]any)[0].(map[string]any)
			section["attention"] = map[string]any{"text": "Must be kept.", "refs": []any{"ev_missing"}}
		}},
		{"empty section title", "minLength: \"\" contains 0 Unicode code points, fewer than 1 (at /$defs/section/properties/title)", func(doc map[string]any) {
			doc["sections"].([]any)[0].(map[string]any)["title"] = ""
		}},
		{"whitespace section title", "minLength: \"\" contains 0 Unicode code points, fewer than 1 (at /$defs/section/properties/title)", func(doc map[string]any) {
			doc["sections"].([]any)[0].(map[string]any)["title"] = " \t\u00a0"
		}},
		{"new section without hunks", "section sec_empty has no hunks", func(doc map[string]any) {
			first := doc["sections"].([]any)[0].(map[string]any)
			second, err := decodeObject(tileJSON(t, first), "section")
			checkNoError(t, err)
			second["id"], second["hunks"] = "sec_empty", []any{}
			doc["sections"] = []any{first, second}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := decodeObject(authoredFixture(), "candidate")
			checkNoError(t, err)
			tc.mutate(doc)
			prepared, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
			if !strings.Contains(findings, tc.finding) {
				t.Fatalf("missing %q in findings:\n%s", tc.finding, findings)
			}
			if tc.name == "attention naming no item" && !strings.Contains(string(prepared), "Must be kept.") {
				t.Fatalf("attention text was dropped: %s", prepared)
			}
		})
	}
}

// The conditional status rules are not in Codex's strict output schema, so a
// status the anchors cannot carry is mapped to asserted rather than rejected.
func TestNormalizationAssertsEvidenceItsAnchorsCannotCarry(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	session := "ses_1"
	bundle := &bundlepkg.Bundle{
		Diff:     []bundlepkg.Hunk{{ID: testHunk, Path: "main_test.go", Role: "test"}, {ID: "h_bbbbbbbbbbbb", Path: "main.go", Role: "source"}},
		Checks:   []bundlepkg.Check{{ID: "chk_1", Kind: "check_run", Name: "test", Status: "completed", Conclusion: "success"}},
		Sessions: []bundlepkg.Session{{RootSessionID: session, CommandEvents: []bundlepkg.CommandEvent{{ID: "toolu_1"}}}},
	}
	hunk := map[string]any{"type": "hunk", "id": testHunk}
	check := map[string]any{"type": "check", "id": "chk_1"}
	source := map[string]any{"type": "hunk", "id": "h_bbbbbbbbbbbb"}
	file := map[string]any{"type": "file", "path": "main_test.go"}
	event := map[string]any{"type": "session_event", "session": session, "event": "toolu_1"}
	for _, tc := range []struct {
		name, status, source, kind string
		anchors                    []any
		wantStatus                 string
		wantGranularity            any
	}{
		{"observed from the diff alone", "observed", "model", "run", []any{hunk}, "asserted", nil},
		{"observed with a session event", "observed", "session", "run", []any{event}, "observed", nil},
		{"verified without a check", "verified", "ci", "ci", []any{hunk}, "asserted", nil},
		{"verified check without granularity", "verified", "ci", "ci", []any{check}, "verified", "job"},
		{"verified test without a test anchor", "verified", "ci", "test", []any{check}, "asserted", nil},
		{"verified test with a test hunk", "verified", "ci", "test", []any{check, hunk}, "verified", "job"},
		{"verified test with a source hunk", "verified", "ci", "test", []any{check, source}, "asserted", nil},
		{"verified test with a file", "verified", "ci", "test", []any{check, file}, "asserted", nil},
		{"reported by the model", "reported", "model", "review", []any{hunk}, "asserted", nil},
		{"reported by a human reviewer", "reported", "human_review", "review", []any{hunk}, "reported", nil},
		{"asserted stays asserted", "asserted", "model", "review", []any{hunk}, "asserted", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := decodeObject(authoredFixture(), "candidate")
			checkNoError(t, err)
			section := doc["sections"].([]any)[0].(map[string]any)
			section["evidence"] = []any{map[string]any{"id": "ev_one", "kind": tc.kind, "claim": "Claimed.", "status": tc.status, "source": tc.source, "anchors": tc.anchors}}
			prepared, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
			checkEqual(t, "", findings)
			var out map[string]any
			checkNoError(t, json.Unmarshal(prepared, &out))
			evidence := out["sections"].([]any)[0].(map[string]any)["evidence"].([]any)[0].(map[string]any)
			checkEqual(t, tc.wantStatus, evidence["status"])
			checkEqual(t, tc.wantGranularity, evidence["granularity"])
		})
	}
}

// A repair is only as good as its findings: each names the failing item and,
// for a conditional status rule, what the status needs.
func TestSchemaFindingsNameTheFailingItemAndRule(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	hunk := map[string]any{"type": "hunk", "id": testHunk}
	for _, tc := range []struct {
		name, finding string
		mutate        func(section map[string]any)
	}{
		{"missing evidence without expects", "section 1 (sec_parser) evidence ev_gap: missing evidence needs expects", func(section map[string]any) {
			section["evidence"] = []any{map[string]any{"id": "ev_gap", "kind": "test", "claim": "No test covers it.", "status": "missing", "source": "model", "anchors": []any{hunk}}}
		}},
		{"unknown block kind", `section 1 (sec_parser) block 1: kind "bogus" is not a schema-defined block kind; use one of behavior,`, func(section map[string]any) {
			section["blocks"] = []any{map[string]any{"kind": "bogus"}}
		}},
		{"block missing a property", `section 1 (sec_parser) block 1 (behavior): required: missing properties: ["steps"]`, func(section map[string]any) {
			section["blocks"] = []any{map[string]any{"kind": "behavior"}}
		}},
		{"section-level problem beside an item", "section 1 (sec_parser): enum: bogus does not equal any of", func(section map[string]any) {
			section["kind"] = "bogus"
			section["blocks"] = []any{map[string]any{"kind": "behavior"}}
		}},
		{"attention naming a hunk", `section 1 (sec_parser) attention ref 1 ("h_0123456789ab"): hunk IDs cannot be attention references; use an evidence (ev_*), risk (rsk_*), or decision (dec_*) ID from this section, or set attention to null`, func(section map[string]any) {
			section["attention"] = map[string]any{"text": "Inspect this hunk.", "refs": []any{testHunk}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := decodeObject(authoredFixture(), "candidate")
			checkNoError(t, err)
			tc.mutate(doc["sections"].([]any)[0].(map[string]any))
			prepared, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
			if !strings.Contains(findings, tc.finding) {
				t.Fatalf("missing %q in findings:\n%s", tc.finding, findings)
			}
			if tc.name == "attention naming a hunk" && !strings.Contains(string(prepared), "Inspect this hunk.") {
				t.Fatalf("malformed attention was dropped: %s", prepared)
			}
			if strings.Contains(findings, "validating root") {
				t.Fatalf("findings keep the schema location chain:\n%s", findings)
			}
		})
	}
}

func TestNormalizationLeavesInheritedSourcesAndUnknownShapesAlone(t *testing.T) {
	head := strings.Repeat("c", 40)
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: head, MergeBaseSHA: strings.Repeat("b", 40)}, Previous: &bundlepkg.PreviousVersion{Version: 1}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Patch: "@@ -1 +1 @@\n-x\n+y\n"}}}
	inherited := map[string]any{"path": "main.go", "revision": strings.Repeat("a", 40), "review_version": float64(1), "why": "Old", "diff": map[string]any{"hunks": []any{map[string]any{"header": "@@ -1 +1 @@", "lines": []any{map[string]any{"kind": "add", "new_line": float64(1), "text": "kept"}}}}}}
	tile := map[string]any{"id": "tile_old", "kind": "bug-fix", "anchors": []any{}, "code": []any{inherited}, "data": map[string]any{}}
	n := &authoredNormalizer{bundle: bundle, version: 2, hunks: map[string]bundlepkg.Hunk{testHunk: bundle.Diff[0]}}
	n.tiles("section", []any{tile}, hunkAnchors([]any{testHunk}))
	assertSameDocumentJSON(t, []byte(`{"path":"main.go","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","review_version":1,"why":"Old","diff":{"hunks":[{"header":"@@ -1 +1 @@","lines":[{"kind":"add","new_line":1,"text":"kept"}]}]}}`), tileJSON(t, inherited))
	if len(n.findings) != 0 {
		t.Fatalf("findings for an inherited source: %v", n.findings)
	}
	v, err := sharedValidator()
	checkNoError(t, err)
	for _, document := range []string{`[]`, `{"sections": "none"}`, `{"sections": [{"hunks": "h"}]}`} {
		if _, findings := v.Prepare([]byte(document), bundle, t.TempDir()); findings == "" {
			t.Fatalf("%s was accepted", document)
		}
	}
}

// A hunk may belong to several sections or to none. Neither is a finding:
// completion keeps the shared hunk in each section, lists the uncovered one
// in unassigned_hunks, and counts every diff hunk's lines once in the stats.
func TestHunksMayBeSharedOrLeftUnassigned(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	doc, err := decodeObject(authoredFixture(), "candidate")
	checkNoError(t, err)
	first := doc["sections"].([]any)[0].(map[string]any)
	second, err := decodeObject(tileJSON(t, first), "section")
	checkNoError(t, err)
	second["id"] = "sec_second"
	doc["sections"] = []any{first, second}
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: "code", Change: "modified", Additions: 3, Deletions: 1},
			{ID: "h_bbbbbbbbbbbb", Path: "notes.md", Role: "docs", Change: "modified", Additions: 2}},
		Sources: bundlepkg.Sources{"diff": true}}
	completed, findings := v.Complete(tileJSON(t, doc), bundle, "", "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if findings != "" {
		t.Fatal(findings)
	}
	var stored struct {
		Sections []struct {
			Hunks []string `json:"hunks"`
		} `json:"sections"`
		Unassigned []string `json:"unassigned_hunks"`
		Stats      struct {
			RiskLOC map[string]int `json:"risk_loc"`
			RoleLOC map[string]int `json:"role_loc"`
		} `json:"stats"`
	}
	checkNoError(t, json.Unmarshal(completed, &stored))
	for i, section := range stored.Sections {
		if !slices.Equal(section.Hunks, []string{testHunk}) {
			t.Fatalf("section %d hunks = %v, want the shared hunk", i, section.Hunks)
		}
	}
	if !slices.Equal(stored.Unassigned, []string{"h_bbbbbbbbbbbb"}) {
		t.Fatalf("unassigned_hunks = %v", stored.Unassigned)
	}
	if stored.Stats.RoleLOC["code"] != 4 || stored.Stats.RoleLOC["docs"] != 2 {
		t.Fatalf("role_loc = %v, want each hunk counted once", stored.Stats.RoleLOC)
	}
	total := 0
	for _, lines := range stored.Stats.RiskLOC {
		total += lines
	}
	if total != 6 || stored.Stats.RiskLOC["unrated"] < 2 {
		t.Fatalf("risk_loc = %v, want 6 lines with the unassigned hunk unrated", stored.Stats.RiskLOC)
	}
}

// Filling anchors is normalisation: an owner with none takes its section's
// hunks, anything the model anchored keeps its own, and a section with no
// hunks gives nothing to fill with.
func TestNormalizationFillsOnlyMissingAnchorsFromTheSectionsHunks(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	doc, err := decodeObject(authoredFixture(), "candidate")
	checkNoError(t, err)
	first := doc["sections"].([]any)[0].(map[string]any)
	first["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{
		map[string]any{"id": "dec_unanchored", "text": "Chose a parser.", "anchors": []any{}},
		map[string]any{"id": "dec_anchored", "text": "Kept the file.", "anchors": []any{map[string]any{"type": "file", "path": "main.go"}}},
	}}}
	empty, err := decodeObject(tileJSON(t, first), "section")
	checkNoError(t, err)
	empty["id"], empty["hunks"], empty["blocks"] = "sec_empty", []any{}, []any{}
	empty["summary"] = map[string]any{"text": "Nothing left.", "anchors": []any{}}
	doc["sections"] = []any{first, empty}
	prepared, _ := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
	var result map[string]any
	checkNoError(t, json.Unmarshal(prepared, &result))
	sections := result["sections"].([]any)
	items := sections[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["items"].([]any)
	assertSameDocumentJSON(t, []byte(`[{"type":"hunk","id":"h_0123456789ab"}]`), tileJSON(t, items[0].(map[string]any)["anchors"]))
	assertSameDocumentJSON(t, []byte(`[{"type":"file","path":"main.go"}]`), tileJSON(t, items[1].(map[string]any)["anchors"]))
	assertSameDocumentJSON(t, []byte(`[]`), tileJSON(t, sections[1].(map[string]any)["summary"].(map[string]any)["anchors"]))
}

func TestNormalizationDropsItemBlocksWithoutItems(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	for name, empty := range map[string]map[string]any{
		"missing": {"kind": "dependency"},
		"null":    {"kind": "dependency", "items": nil},
		"empty":   {"kind": "decisions", "items": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeObject(authoredFixture(), "candidate")
			checkNoError(t, err)
			section := doc["sections"].([]any)[0].(map[string]any)
			kept := map[string]any{"kind": "dependency", "items": []any{map[string]any{"name": "lib", "ecosystem": "go", "from": "1.0.0", "to": "1.1.0", "hunks": []any{testHunk}}}}
			section["blocks"] = []any{empty, kept}
			prepared, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
			checkEqual(t, "", findings)
			var result map[string]any
			checkNoError(t, json.Unmarshal(prepared, &result))
			blocks := result["sections"].([]any)[0].(map[string]any)["blocks"].([]any)
			checkEqual(t, 1, len(blocks))
			checkEqual(t, "dependency", blocks[0].(map[string]any)["kind"])
			checkEqual(t, 1, len(blocks[0].(map[string]any)["items"].([]any)))
			// A section left with no blocks is still valid.
			section["blocks"] = []any{empty}
			_, findings = v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
			checkEqual(t, "", findings)
		})
	}
}

// approved_by_human marks an agent decision a human explicitly accepted; a
// human decision cannot carry it.
func TestDecisionApprovalIsOnlyForAgentDecisions(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	for _, tc := range []struct {
		by       string
		approved any
		valid    bool
	}{
		{"agent", true, true},
		{"agent", nil, true},
		{"human", nil, true},
		{"human", true, false},
		{"agent", false, false},
		// Codex strict output sends null for an absent optional field;
		// normalisation removes it.
		{"agent", "null", true},
		{"human", "null", true},
	} {
		doc, err := decodeObject(authoredFixture(), "candidate")
		checkNoError(t, err)
		item := map[string]any{"id": "dec_parser", "by": tc.by, "text": "Chose a parser.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}
		switch tc.approved {
		case nil:
		case "null":
			item["approved_by_human"] = nil
		default:
			item["approved_by_human"] = tc.approved
		}
		doc["sections"].([]any)[0].(map[string]any)["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{item}}}
		_, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
		if (findings == "") != tc.valid {
			t.Errorf("by %s, approved_by_human %v: findings %q, want valid %v", tc.by, tc.approved, findings, tc.valid)
		}
		// The lenient schema the model is given must take the null too.
		if tc.approved == "null" {
			var lenient any
			checkNoError(t, json.Unmarshal(tileJSON(t, doc), &lenient))
			if err := resolvedSchema(t, v.Schema).Validate(lenient); err != nil {
				t.Errorf("by %s with a null approved_by_human: lenient schema rejects it: %v", tc.by, err)
			}
		}
	}
}

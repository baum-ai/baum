package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review/tilecatalogue"
)

const stagedTestHunk = "h_abcdefabcdef"

func stagedBundle() *bundlepkg.Bundle {
	return &bundlepkg.Bundle{
		Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources: bundlepkg.Sources{"diff": true},
		Diff: []bundlepkg.Hunk{
			{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added", StartLine: 1, LineCount: 10, Additions: 10},
			{ID: stagedTestHunk, Path: "main_test.go", Role: "test", Change: "added", StartLine: 1, LineCount: 5, Additions: 5},
		},
	}
}

func stagedAssessment(hunk string) map[string]any {
	return map[string]any{"level": 2, "factors": []any{}, "reasons": []any{map[string]any{"text": "A local change.", "anchors": []any{map[string]any{"type": "hunk", "id": hunk}}}}}
}

// stagedOutlineReply is an outline as the outline stage writes it.
func stagedOutlineReply(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	outline := map[string]any{
		"schema": "baum.review/1",
		"summary": map[string]any{
			"text": "Adds a parser and its tests.", "review_exchange": map[string]any{"summary": "No reviews yet."}, "change_kind": "feature",
			"implements": []any{}, "release_note": nil, "breaking_change": nil, "attention": nil,
			"authorship": map[string]any{"declared": "none", "declared_tools": []any{}, "anchors": []any{}},
		},
		"relations": []any{},
		"sections": []any{
			map[string]any{"id": "sec_parser", "title": "Parser", "kind": "logical", "hunks": []any{testHunk}, "assessment": stagedAssessment(testHunk)},
			map[string]any{"id": "sec_tests", "title": "Tests", "kind": "logical", "hunks": []any{stagedTestHunk}, "assessment": stagedAssessment(stagedTestHunk)},
		},
	}
	if change != nil {
		change(outline)
	}
	encoded, err := json.Marshal(outline)
	checkNoError(t, err)
	return encoded
}

func stagedDetails(id, hunk string) map[string]any {
	return map[string]any{"id": id, "summary": map[string]any{"text": "Adds the code.", "anchors": []any{map[string]any{"type": "hunk", "id": hunk}}}, "attention": nil, "risks": []any{}, "evidence": []any{}, "blocks": []any{}}
}

func preparedOutline(t *testing.T, v *DocumentValidator, b *bundlepkg.Bundle) []byte {
	t.Helper()
	outline, findings := v.PrepareOutline(stagedOutlineReply(t, nil), b, t.TempDir())
	checkEqual(t, "", findings)
	return outline
}

func schemaTree(t *testing.T, schema []byte) map[string]any {
	t.Helper()
	var tree map[string]any
	checkNoError(t, json.Unmarshal(schema, &tree))
	var resolved jsonschema.Schema
	checkNoError(t, json.Unmarshal(schema, &resolved))
	if _, err := resolved.Resolve(nil); err != nil {
		t.Fatalf("the schema does not resolve: %v", err)
	}
	return tree
}

func properties(node any) map[string]any {
	return node.(map[string]any)["properties"].(map[string]any)
}

// Each stage replies against a projection of the model schema: the fields it
// writes, defined exactly as the model schema defines them, and nothing else.
func TestStagedSchemasAreProjectionsOfTheModelSchema(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	modelSchema, err := v.ModelSchema(tilecatalogue.Kinds())
	checkNoError(t, err)
	model := schemaTree(t, modelSchema)
	modelSection := properties(model["$defs"].(map[string]any)["section"])
	modelSummary := properties(properties(model)["summary"])

	outlineSchema, err := v.OutlineSchema()
	checkNoError(t, err)
	outline := schemaTree(t, outlineSchema)
	checkEqual(t, []string{"relations", "schema", "sections", "summary"}, sortedKeys(keySet(properties(outline))))
	summary := properties(properties(outline)["summary"])
	checkEqual(t, []string{"attention", "authorship", "breaking_change", "change_kind", "implements", "release_note", "review_exchange", "text"}, sortedKeys(keySet(summary)))
	section := properties(outline["$defs"].(map[string]any)["section"])
	checkEqual(t, []string{"assessment", "hunks", "id", "kind", "title"}, sortedKeys(keySet(section)))
	checkEqual(t, modelSummary["text"], summary["text"])
	checkEqual(t, modelSection["assessment"], section["assessment"])

	detailsSchema, err := v.DetailsSchema(tilecatalogue.Kinds())
	checkNoError(t, err)
	details := schemaTree(t, detailsSchema)
	section = properties(details["$defs"].(map[string]any)["section"])
	checkEqual(t, []string{"attention", "blocks", "evidence", "id", "risks", "summary", "tiles"}, sortedKeys(keySet(section)))
	checkEqual(t, modelSection["risks"], section["risks"])
	checkEqual(t, []any{"id", "summary"}, details["$defs"].(map[string]any)["section"].(map[string]any)["required"])
	withoutTiles, err := v.DetailsSchema(nil)
	checkNoError(t, err)
	if strings.Contains(string(withoutTiles), `"tiles"`) {
		t.Fatal("a details schema without tile kinds offers tiles")
	}

	contextSchema, err := v.ContextSchema()
	checkNoError(t, err)
	context := schemaTree(t, contextSchema)
	checkEqual(t, modelSummary["context"], properties(context)["context"])

	for name, schema := range map[string][]byte{"outline": outlineSchema, "details": detailsSchema, "context": contextSchema} {
		if strings.Contains(string(schema), `"details"`) || strings.Contains(string(schema), `"review_minutes"`) {
			t.Errorf("the %s schema offers a host-owned field", name)
		}
	}
}

func keySet(object map[string]any) map[string]bool {
	keys := map[string]bool{}
	for key := range object {
		keys[key] = true
	}
	return keys
}

// The outline keeps only its fields, leaves every section's details pending,
// and is held to the whole document's section rules plus an assessment for
// every logical section with hunks.
func TestPrepareOutline(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	reply := stagedOutlineReply(t, func(outline map[string]any) {
		section := outline["sections"].([]any)[0].(map[string]any)
		section["risks"] = []any{map[string]any{"id": "rsk_early"}}
		outline["summary"].(map[string]any)["review_minutes"] = 9
	})
	outline, findings := v.PrepareOutline(reply, b, t.TempDir())
	checkEqual(t, "", findings)
	doc := mustDecode(t, outline)
	for _, section := range jsonObjects(doc["sections"]) {
		checkEqual(t, sectionDetailsPending, section["details"])
		checkEqual(t, []any{}, section["risks"])
	}
	if _, present := doc["summary"].(map[string]any)["review_minutes"]; present {
		t.Fatal("the outline kept a reading time")
	}
	if strings.Contains(string(OutlineOf(outline)), `"details"`) {
		t.Fatalf("the outline shape carries host fields: %s", OutlineOf(outline))
	}

	for name, test := range map[string]struct {
		change func(map[string]any)
		want   string
	}{
		"unassessed": {func(outline map[string]any) {
			delete(outline["sections"].([]any)[1].(map[string]any), "assessment")
		}, "section 2 (sec_tests): a logical section with hunks needs an assessment"},
		"unknown hunk": {func(outline map[string]any) {
			outline["sections"].([]any)[1].(map[string]any)["hunks"] = []any{stagedTestHunk, "h_ffffffffffff"}
		}, ""},
		"two mechanical": {func(outline map[string]any) {
			for _, section := range jsonObjects(outline["sections"]) {
				section["kind"] = "mechanical"
			}
		}, "more than one mechanical section"},
		"no summary": {func(outline map[string]any) { delete(outline, "summary") }, "summary"},
	} {
		_, findings := v.PrepareOutline(stagedOutlineReply(t, test.change), b, t.TempDir())
		if test.want == "" {
			// Normalization drops an unknown hunk.
			checkEqual(t, "", findings)
		} else if !strings.Contains(findings, test.want) {
			t.Errorf("%s: findings %q lack %q", name, findings, test.want)
		}
	}

	// The previous sections come first, in their order, as completion would
	// put them.
	previous := &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_tests", Content: encodeJSON(t, historySection("tests", stagedTestHunk))}}}
	b.Previous = previous
	outline, findings = v.PrepareOutline(stagedOutlineReply(t, nil), b, t.TempDir())
	checkEqual(t, "", findings)
	sections := jsonObjects(mustDecode(t, outline)["sections"])
	checkEqual(t, "sec_tests", sections[0]["id"])
	checkEqual(t, "sec_parser", sections[1]["id"])
}

// A section's details are validated merged into the outline, so their
// findings are the section's own.
func TestPrepareSectionDetails(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	outline := preparedOutline(t, v, b)
	details := stagedDetails("Sec-Parser", testHunk)
	section, findings := v.PrepareSectionDetails(outline, encodeJSON(t, details), b, t.TempDir())
	checkEqual(t, "", findings)
	merged := mustDecode(t, section)
	checkEqual(t, "sec_parser", merged["id"])
	checkEqual(t, "Parser", merged["title"])
	if merged["details"] != nil || merged["assessment"] == nil || merged["summary"] == nil {
		t.Fatalf("merged section: %s", section)
	}
	if got := mustDecode(t, DetailsOf(section)); got["title"] != nil || got["summary"] == nil {
		t.Fatalf("details shape: %s", DetailsOf(section))
	}

	details["attention"] = map[string]any{"text": "Look here.", "refs": []any{"rsk_missing"}}
	_, findings = v.PrepareSectionDetails(outline, encodeJSON(t, details), b, t.TempDir())
	if !strings.Contains(findings, `section 1 (sec_parser) attention ref "rsk_missing"`) {
		t.Fatalf("findings %q", findings)
	}
	if _, findings := v.PrepareSectionDetails(outline, encodeJSON(t, stagedDetails("sec_other", testHunk)), b, t.TempDir()); !strings.Contains(findings, "no section") {
		t.Fatalf("findings %q", findings)
	}
}

func TestPrepareContext(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	outline := preparedOutline(t, v, b)
	context, findings := v.PrepareContext(outline, []byte(`{"context":{"area":{"text":"The parser.","refs":[]},"related":{"text":"","refs":[]}}}`), b, t.TempDir())
	checkEqual(t, "", findings)
	checkEqual(t, map[string]any{"area": map[string]any{"text": "The parser.", "refs": []any{}}}, mustDecode(t, context))
	for _, reply := range []string{`{"context":null}`, `{}`, `{"context":{"related":{"text":"Other code.","refs":[]}}}`} {
		if _, findings := v.PrepareContext(outline, []byte(reply), b, t.TempDir()); findings == "" {
			t.Errorf("%s: accepted", reply)
		}
	}
}

// The assembled document completes as a document written in one pass does: a
// failed section keeps its outline and the summary may lack its context; a
// draft holds pending sections and a final document none.
func TestAssembleStagedCompletes(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	directory := t.TempDir()
	outline := preparedOutline(t, v, b)
	section, findings := v.PrepareSectionDetails(outline, encodeJSON(t, stagedDetails("sec_parser", testHunk)), b, directory)
	checkEqual(t, "", findings)
	parts := StagedParts{Outline: outline, Sections: map[string][]byte{"sec_parser": section}}

	draft, err := AssembleStaged(parts, false)
	checkNoError(t, err)
	sections := mustDecode(t, draft)["sections"].([]any)
	checkEqual(t, nil, sections[0].(map[string]any)["details"])
	checkEqual(t, sectionDetailsPending, sections[1].(map[string]any)["details"])
	if _, findings := v.PrepareStaged(draft, b, directory); findings != "" {
		t.Fatalf("draft rejected: %s", findings)
	}
	if _, findings := v.CompleteStaged(draft, b, directory, "model", "staged-v1", time.Now()); findings != "" {
		t.Fatalf("a draft does not complete: %s", findings)
	}

	final, err := AssembleStaged(parts, true)
	checkNoError(t, err)
	if strings.Contains(string(final), `"pending"`) {
		t.Fatalf("a final document holds pending details: %s", final)
	}
	doc := mustDecode(t, final)
	checkEqual(t, sectionDetailsFailed, doc["sections"].([]any)[1].(map[string]any)["details"])
	checkEqual(t, float64(1), doc["summary"].(map[string]any)["review_minutes"])
	if _, present := doc["summary"].(map[string]any)["context"]; present {
		t.Fatal("a context nobody wrote")
	}
	prepared, findings := v.PrepareStaged(final, b, directory)
	checkEqual(t, "", findings)
	completed, findings := v.CompleteStaged(prepared, b, directory, "model", "staged-v1", time.Now())
	checkEqual(t, "", findings)
	if stored := v.ValidateStored(completed, b); stored != nil {
		t.Fatalf("the completed document is not a valid stored one: %q", stored)
	}
	// The one-pass contract still rejects a document without its details:
	// it drops the state and then misses the summary.
	if _, findings := v.Prepare(final, b, directory); !strings.Contains(findings, `missing properties: ["summary"]`) {
		t.Fatalf("the authored contract accepted a staged document: %q", findings)
	}

	parts.Context = []byte(`{"area":{"text":"The parser.","refs":[]}}`)
	parts.Failed = map[string]bool{"sec_tests": true}
	draft, err = AssembleStaged(parts, false)
	checkNoError(t, err)
	doc = mustDecode(t, draft)
	checkEqual(t, sectionDetailsFailed, doc["sections"].([]any)[1].(map[string]any)["details"])
	checkEqual(t, "The parser.", doc["summary"].(map[string]any)["context"].(map[string]any)["area"].(map[string]any)["text"])
}

// Only a section whose hunks are unchanged and whose previous details were
// complete is reused, with its active risks and without their lifecycle.
func TestReusableSectionDetails(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	parser := historySection("parser", testHunk, "rsk_live", "rsk_fixed")
	risks := parser["risks"].([]any)
	risks[0].(map[string]any)["state"], risks[0].(map[string]any)["introduced_version"], risks[0].(map[string]any)["mitigated_version"] = "active", 1, nil
	risks[1].(map[string]any)["state"], risks[1].(map[string]any)["introduced_version"], risks[1].(map[string]any)["mitigated_version"] = "mitigated", 1, 2
	failed := historySection("tests", stagedTestHunk)
	withoutDetails(failed, sectionDetailsFailed, true)
	b.Previous = &bundlepkg.PreviousVersion{Version: 2, Sections: []bundlepkg.PreviousSection{
		{ID: "sec_parser", HunkContentHashes: []string{testHunk}, Content: encodeJSON(t, parser)},
		{ID: "sec_tests", HunkContentHashes: []string{stagedTestHunk}, Content: encodeJSON(t, failed)},
	}}
	outline := preparedOutline(t, v, b)
	reusable := ReusableSectionDetails(b.Previous, outline)
	if len(reusable) != 1 || reusable["sec_parser"] == nil {
		t.Fatalf("reusable: %v", reusable)
	}
	details := mustDecode(t, reusable["sec_parser"])
	kept := jsonObjects(details["risks"])
	if len(kept) != 1 || kept[0]["id"] != "rsk_live" || kept[0]["state"] != nil {
		t.Fatalf("reused risks: %v", kept)
	}
	if _, findings := v.PrepareSectionDetails(outline, reusable["sec_parser"], b, t.TempDir()); findings != "" {
		t.Fatalf("reused details rejected: %s", findings)
	}

	b.Previous.Sections[0].HunkContentHashes = []string{testHunk, stagedTestHunk}
	if reusable := ReusableSectionDetails(b.Previous, outline); len(reusable) != 0 {
		t.Fatalf("a section whose hunks changed was reused: %v", reusable)
	}
	if ReusableSectionDetails(nil, outline) != nil {
		t.Fatal("reuse without a previous version")
	}
}

func TestPreserveSectionRiskOmissions(t *testing.T) {
	previous := &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_parser", Content: encodeJSON(t, historySection("parser", testHunk, "rsk_old"))}}}
	assessed := stagedDetails("sec_parser", testHunk)
	repaired := stagedDetails("sec_parser", testHunk)
	repaired["risks"] = historySection("parser", testHunk, "rsk_old", "rsk_new")["risks"]
	kept := mustDecode(t, PreserveSectionRiskOmissions(previous, encodeJSON(t, assessed), encodeJSON(t, repaired)))
	risks := jsonObjects(kept["risks"])
	if len(risks) != 1 || risks[0]["id"] != "rsk_new" {
		t.Fatalf("risks: %v", risks)
	}
	if kept["hunks"] != nil {
		t.Fatalf("details gained hunks: %v", kept)
	}
	unchanged := encodeJSON(t, repaired)
	checkEqual(t, string(unchanged), string(PreserveSectionRiskOmissions(nil, encodeJSON(t, assessed), unchanged)))
}

// The reading time counts prose at 400 words a minute, without adding time
// for code excerpts, rounded up, within 1 to 240.
func TestReviewMinutes(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
	document := map[string]any{
		"summary": map[string]any{
			"text": words(100), "attention": map[string]any{"text": words(20)}, "review_exchange": map[string]any{"summary": words(30)},
			"release_note": words(10), "breaking_change": nil,
			"context": map[string]any{"area": map[string]any{"text": words(40)}, "related": map[string]any{"text": words(10)}},
		},
		"sections": []any{map[string]any{
			"title": words(5), "summary": map[string]any{"text": words(50)}, "attention": map[string]any{"text": words(5)},
			"risks":    []any{map[string]any{"id": "rsk_a", "text": words(30)}},
			"evidence": []any{map[string]any{"id": "ev_a", "claim": words(10), "detail": words(10), "expects": words(5)}},
			"blocks": []any{
				map[string]any{"kind": "behavior", "steps": []any{map[string]any{"title": words(5), "text": words(20), "before": words(10), "after": words(10), "question": words(10),
					"anchors": []any{map[string]any{"type": "file", "path": "x"}, map[string]any{"type": "hunk", "id": testHunk, "lines": []any{3, 8}}}}}},
				map[string]any{"kind": "decisions", "items": []any{map[string]any{"id": "dec_a", "title": words(5), "text": words(10), "rationale": words(5)}}},
				map[string]any{"kind": "code", "content": "a\nb\nc\n"},
				map[string]any{"kind": "intent", "text": words(500)},
			},
			"tiles": []any{map[string]any{"kind": "bug-fix", "data": map[string]any{"code": map[string]any{"diff": map[string]any{"hunks": []any{map[string]any{"header": "@@ -1 +1 @@", "lines": []any{1, 2, 3}}}}}}}},
		}},
	}
	// 400 words is 1 minute; the anchored, block and tile code adds no time.
	checkEqual(t, 1, ReviewMinutes(encodeJSON(t, document)))
	document["summary"].(map[string]any)["breaking_change"] = "one"
	checkEqual(t, 2, ReviewMinutes(encodeJSON(t, document)))
	checkEqual(t, 1, ReviewMinutes([]byte(`{"summary":{"text":""},"sections":[]}`)))
	checkEqual(t, 1, ReviewMinutes([]byte(`not json`)))
	checkEqual(t, 240, ReviewMinutes(encodeJSON(t, map[string]any{"summary": map[string]any{"text": words(100000)}})))
}

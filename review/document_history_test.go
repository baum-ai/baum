package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bundlepkg "github.com/baumai/baum/bundle"
)

// Lifecycle fields and repeated risk IDs are decided by the platform: an
// authored lifecycle field is dropped and a repeated risk renamed, while a
// repeated section stays a rejection because it is a repeated section.
func TestHistoryDropsAuthoredLifecycleRenamesRisksAndRejectsDuplicateSections(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk}}}
	section := historySection("a", testHunk, "rsk_one")
	section["risks"].([]any)[0].(map[string]any)["state"] = "mitigated"
	prepared, findings := v.Prepare(historyCandidate(t, []any{section}), bundle, t.TempDir())
	checkEqual(t, "", findings)
	if strings.Contains(string(prepared), `"state"`) {
		t.Fatalf("model-authored lifecycle survived normalization: %s", prepared)
	}
	section = historySection("a", testHunk, "rsk_one", "rsk_one")
	prepared, findings = v.Prepare(historyCandidate(t, []any{section}), bundle, t.TempDir())
	checkEqual(t, "", findings)
	if !strings.Contains(string(prepared), `"id":"rsk_one_2"`) {
		t.Fatalf("repeated risk was not renamed: %s", prepared)
	}
	_, findings = v.Prepare(historyCandidate(t, []any{section, section}), bundle, t.TempDir())
	if !strings.Contains(findings, "duplicate generated section") {
		t.Fatal(findings)
	}
}

func historySection(id, hunk string, risks ...string) map[string]any {
	values := []any{}
	for _, risk := range risks {
		values = append(values, map[string]any{"id": risk, "tier": "high", "category": "correctness", "source": "model", "text": "A concrete risk.", "anchors": []any{map[string]any{"type": "hunk", "id": hunk}}})
	}
	return map[string]any{"id": "sec_" + id, "title": id, "kind": "logical", "hunks": []any{hunk}, "summary": map[string]any{"text": "Changes behavior.", "anchors": []any{map[string]any{"type": "hunk", "id": hunk}}}, "attention": nil, "risks": values, "evidence": []any{}, "blocks": []any{}}
}

func historyCandidate(t *testing.T, sections []any) []byte {
	t.Helper()
	doc, err := decodeObject(authoredFixture(), "fixture")
	checkNoError(t, err)
	doc["sections"] = sections
	encoded, err := json.Marshal(doc)
	checkNoError(t, err)
	return encoded
}

func checkNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func checkEqual(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v", want, got)
	}
}

func TestHistoryRepairDoesNotPreserveMalformedAssessmentOmissions(t *testing.T) {
	section := historySection("a", testHunk, "rsk_one")
	content, err := json.Marshal(section)
	checkNoError(t, err)
	previous := &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_a", Content: content}}}
	repaired := historyCandidate(t, []any{section})
	for _, assessed := range []string{`{"sections":null}`, `{"sections":[{"id":"sec_a","hunks":[],"risks":null}]}`} {
		checkEqual(t, string(repaired), string(PreserveRiskOmissionsForRepair(previous, []byte(assessed), repaired)))
	}
}

// A section without current hunks keeps each risk's recorded lifecycle: an
// active risk stays active and an earlier mitigation keeps its version.
func TestStaleSectionRisksKeepTheirRecordedState(t *testing.T) {
	prior := historySection("a", testHunk, "rsk_active", "rsk_gone")
	risks := prior["risks"].([]any)
	active, gone := risks[0].(map[string]any), risks[1].(map[string]any)
	active["state"], active["introduced_version"], active["mitigated_version"] = "active", float64(2), nil
	gone["state"], gone["introduced_version"], gone["mitigated_version"] = "mitigated", float64(1), float64(2)
	current := cloneObject(prior)
	current["hunks"] = []any{}
	current["risks"] = []any{cloneObject(active), cloneObject(gone)}
	checkNoError(t, reconcileSectionRisks(prior, current, 3, 4, true))
	got := current["risks"].([]any)
	checkEqual(t, 2, len(got))
	first, second := got[0].(map[string]any), got[1].(map[string]any)
	checkEqual(t, "active", first["state"])
	checkEqual(t, 2, first["introduced_version"])
	checkEqual(t, nil, first["mitigated_version"])
	checkEqual(t, "mitigated", second["state"])
	checkEqual(t, 1, second["introduced_version"])
	checkEqual(t, 2, second["mitigated_version"])

	// The same holds when the stale section's risk was left out entirely.
	current = cloneObject(prior)
	current["hunks"] = []any{}
	current["risks"] = []any{}
	checkNoError(t, reconcileSectionRisks(prior, current, 3, 4, true))
	checkEqual(t, "active", current["risks"].([]any)[0].(map[string]any)["state"])
	checkEqual(t, "mitigated", current["risks"].([]any)[1].(map[string]any)["state"])

	// A legacy risk without lifecycle fields is active, and a risk first
	// authored in a stale section is active from this version.
	legacy := historySection("a", testHunk, "rsk_legacy")
	current = cloneObject(legacy)
	current["hunks"] = []any{}
	current["risks"] = append(current["risks"].([]any), historySection("b", testHunk, "rsk_new")["risks"].([]any)[0])
	checkNoError(t, reconcileSectionRisks(legacy, current, 3, 4, true))
	got = current["risks"].([]any)
	first, second = got[0].(map[string]any), got[1].(map[string]any)
	checkEqual(t, "active", first["state"])
	checkEqual(t, 3, first["introduced_version"])
	checkEqual(t, nil, first["mitigated_version"])
	checkEqual(t, "rsk_new", second["id"])
	checkEqual(t, "active", second["state"])
	checkEqual(t, 4, second["introduced_version"])
	checkEqual(t, nil, second["mitigated_version"])
}

// Restoring an omitted previous section early leaves completion's history
// reconciliation unchanged: the restored section is stale, so its risks keep
// their recorded state, while a risk omitted from a section the model wrote
// still reads as mitigated rather than reactivated.
func TestRestoreOmittedPreviousSectionsKeepsRiskHistory(t *testing.T) {
	gone := historySection("gone", testHunk, "rsk_gone_active", "rsk_gone_mitigated")
	goneRisks := gone["risks"].([]any)
	goneRisks[0].(map[string]any)["state"], goneRisks[0].(map[string]any)["introduced_version"] = "active", float64(1)
	goneRisks[1].(map[string]any)["state"], goneRisks[1].(map[string]any)["introduced_version"], goneRisks[1].(map[string]any)["mitigated_version"] = "mitigated", float64(1), float64(1)
	kept := historySection("kept", testHunk, "rsk_kept_omitted")
	kept["risks"].([]any)[0].(map[string]any)["state"], kept["risks"].([]any)[0].(map[string]any)["introduced_version"] = "active", float64(1)
	previous := &bundlepkg.PreviousVersion{Version: 1}
	for _, section := range []map[string]any{gone, kept} {
		content, err := json.Marshal(section)
		checkNoError(t, err)
		previous.Sections = append(previous.Sections, bundlepkg.PreviousSection{ID: section["id"].(string), Content: content})
	}
	// The model omits sec_gone and writes sec_kept without its risk.
	written := historySection("kept", testHunk)
	candidate := historyCandidate(t, []any{written, historySection("new", testHunk)})

	restored, err := RestoreOmittedPreviousSections(candidate, previous, nil)
	checkNoError(t, err)
	var sections struct {
		Sections []struct {
			ID    string           `json:"id"`
			Hunks []any            `json:"hunks"`
			Risks []map[string]any `json:"risks"`
		} `json:"sections"`
	}
	checkNoError(t, json.Unmarshal(restored, &sections))
	checkEqual(t, 3, len(sections.Sections))
	checkEqual(t, "sec_gone", sections.Sections[0].ID)
	checkEqual(t, 0, len(sections.Sections[0].Hunks))
	checkEqual(t, nil, sections.Sections[0].Risks[0]["state"])
	checkEqual(t, 0, len(sections.Sections[1].Risks))

	direct, err := reconcileAuthoredHistory(candidate, previous, nil, nil, true)
	checkNoError(t, err)
	early, err := reconcileAuthoredHistory(restored, previous, nil, nil, true)
	checkNoError(t, err)
	var want, got any
	checkNoError(t, json.Unmarshal(direct, &want))
	checkNoError(t, json.Unmarshal(early, &got))
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("completion after early restoration differs:\n%s\nwant\n%s", early, direct)
	}

	unchanged := historyCandidate(t, []any{historySection("gone", testHunk), historySection("kept", testHunk)})
	if same, err := RestoreOmittedPreviousSections(unchanged, previous, nil); err != nil || string(same) != string(unchanged) {
		t.Fatalf("a document omitting nothing changed: %v", err)
	}
}

// A hunk ID names the hunk's content, so a rebase that moves the hunk keeps
// its ID. Content carried over from the previous version (a section the
// model left out, a risk it dropped) is fitted to the hunk's current lines,
// as an authored anchor is: a range that no longer overlaps the hunk is
// removed and one that still does is clamped. The model cannot repair content
// it did not write, so a stale range would otherwise fail every repair.
func TestCarriedHunkAnchorsFitTheirMovedHunk(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	// The hunk sat at lines 10-14 and now sits at 110-114.
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "a.go", StartLine: 110, LineCount: 5}}}
	anchor := func(lines ...int) []any {
		return []any{map[string]any{"type": "hunk", "id": testHunk, "lines": []any{float64(lines[0]), float64(lines[1])}}}
	}
	gone := historySection("gone", testHunk, "rsk_gone")
	gone["summary"].(map[string]any)["anchors"] = anchor(10, 12)
	gone["risks"].([]any)[0].(map[string]any)["anchors"] = anchor(108, 111)
	kept := historySection("kept", testHunk, "rsk_dropped")
	kept["risks"].([]any)[0].(map[string]any)["anchors"] = anchor(11, 11)
	previous := &bundlepkg.PreviousVersion{Version: 1}
	for _, section := range []map[string]any{gone, kept} {
		content, err := json.Marshal(section)
		checkNoError(t, err)
		previous.Sections = append(previous.Sections, bundlepkg.PreviousSection{ID: section["id"].(string), Content: content})
	}
	bundle.Previous = previous
	// The model omits sec_gone and writes sec_kept without its risk.
	candidate := historyCandidate(t, []any{historySection("kept", testHunk)})

	_, findings := v.Prepare(candidate, bundle, t.TempDir())
	checkEqual(t, "", findings)

	reconciled, err := reconcileAuthoredHistory(candidate, previous, nil, bundle.Diff, true)
	checkNoError(t, err)
	var doc struct {
		Sections []struct {
			ID      string `json:"id"`
			Summary struct {
				Anchors []map[string]any `json:"anchors"`
			} `json:"summary"`
			Risks []struct {
				ID      string           `json:"id"`
				Anchors []map[string]any `json:"anchors"`
			} `json:"risks"`
		} `json:"sections"`
	}
	checkNoError(t, json.Unmarshal(reconciled, &doc))
	checkEqual(t, "sec_gone", doc.Sections[0].ID)
	if lines, ok := doc.Sections[0].Summary.Anchors[0]["lines"]; ok {
		t.Fatalf("a carried range outside the moved hunk was kept: %v", lines)
	}
	checkEqual(t, []any{float64(110), float64(111)}, doc.Sections[0].Risks[0].Anchors[0]["lines"])
	checkEqual(t, "sec_kept", doc.Sections[1].ID)
	checkEqual(t, "rsk_dropped", doc.Sections[1].Risks[0].ID)
	if lines, ok := doc.Sections[1].Risks[0].Anchors[0]["lines"]; ok {
		t.Fatalf("a dropped risk's carried range outside the moved hunk was kept: %v", lines)
	}
}

// The previous version's hunk anchors move with their hunks: shifted by as
// many lines as the hunk moved, then fitted to it. A hunk whose line count
// changed kept its ID with different context, so no shift is known and the
// range is removed. An anchor whose hunk's earlier position is unknown is only
// fitted, and anchors to hunks no longer in the diff are left as they were.
func TestFitPreviousHunkAnchorsFollowsMovedHunks(t *testing.T) {
	const other, gone, reshaped = "h_aaaaaaaaaaaa", "h_bbbbbbbbbbbb", "h_cccccccccccc"
	anchor := func(id string, lines ...int) map[string]any {
		return map[string]any{"type": "hunk", "id": id, "lines": []any{float64(lines[0]), float64(lines[1])}}
	}
	section := map[string]any{"id": "sec_a", "summary": map[string]any{"text": "Changes.", "anchors": []any{
		anchor(testHunk, 12, 13), anchor(other, 5, 9), anchor(gone, 1, 2), anchor(reshaped, 31, 31),
	}}}
	content, err := json.Marshal(section)
	checkNoError(t, err)
	b := &bundlepkg.Bundle{
		Diff: []bundlepkg.Hunk{
			{ID: testHunk, StartLine: 110, LineCount: 5}, {ID: other, StartLine: 7, LineCount: 2},
			// A context line was added above the change: same ID, one line more.
			{ID: reshaped, StartLine: 90, LineCount: 6},
		},
		Previous: &bundlepkg.PreviousVersion{Version: 1, HunkRanges: map[string][2]int{testHunk: {10, 5}, gone: {1, 2}, reshaped: {30, 5}},
			Sections: []bundlepkg.PreviousSection{{ID: "sec_a", Content: content}}},
	}
	checkNoError(t, FitPreviousHunkAnchors(b))
	var fitted struct {
		Summary struct {
			Anchors []map[string]any `json:"anchors"`
		} `json:"summary"`
	}
	checkNoError(t, json.Unmarshal(b.Previous.Sections[0].Content, &fitted))
	checkEqual(t, []any{float64(112), float64(113)}, fitted.Summary.Anchors[0]["lines"])
	checkEqual(t, []any{float64(7), float64(8)}, fitted.Summary.Anchors[1]["lines"])
	checkEqual(t, []any{float64(1), float64(2)}, fitted.Summary.Anchors[2]["lines"])
	if lines, ok := fitted.Summary.Anchors[3]["lines"]; ok {
		t.Fatalf("an anchor on a hunk whose context changed was shifted to %v", lines)
	}
	if strings.Contains(string(mustMarshal(t, b.Previous)), "112") == false {
		t.Fatal("the fitted lines are not in the bundle's previous version")
	}
	if strings.Contains(string(mustMarshal(t, b.Previous)), "HunkRanges") {
		t.Fatal("the recorded start lines were serialized into the bundle")
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	checkNoError(t, err)
	return encoded
}

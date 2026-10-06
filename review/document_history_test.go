package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
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

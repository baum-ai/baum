package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review/tilecatalogue"
)

// withoutDetails strips a stored section to its outline, as a live draft or
// a section whose details failed holds it.
func withoutDetails(section map[string]any, details string, summary bool) {
	section["details"] = details
	if summary {
		section["summary"] = nil
	} else {
		delete(section, "summary")
	}
	section["attention"] = nil
	section["risks"], section["evidence"], section["blocks"] = []any{}, []any{}, []any{}
	delete(section, "tiles")
}

// A stored section may lack its details while they are being written or
// after they failed: it keeps its outline, and its summary may be null or
// absent. Without a details state the summary is still required.
func TestValidateStoredAcceptsSectionsWithoutDetails(t *testing.T) {
	for _, details := range []string{sectionDetailsPending, sectionDetailsFailed} {
		for _, null := range []bool{true, false} {
			doc := storedExample(t)
			withoutDetails(firstSection(doc), details, null)
			if findings := validateStored(t, doc, nil); findings != nil {
				t.Errorf("%s, null summary %v: rejected: %q", details, null, findings)
			}
		}
	}
	for name, change := range map[string]func(map[string]any){
		"null summary":    func(section map[string]any) { section["summary"] = nil },
		"absent summary":  func(section map[string]any) { delete(section, "summary") },
		"unknown details": func(section map[string]any) { section["details"] = "partial" },
	} {
		doc := storedExample(t)
		change(firstSection(doc))
		if findings := validateStored(t, doc, nil); findings == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Refresh reads and stores a document whose sections lack their details, so
// a published version keeps a failed section's outline.
func TestRefreshKeepsASectionWithoutDetails(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	doc, err := decodeObject(published, "published")
	checkNoError(t, err)
	withoutDetails(doc["sections"].([]any)[0].(map[string]any), sectionDetailsFailed, true)
	stored, err := json.Marshal(doc)
	checkNoError(t, err)
	refreshed, err := validator.Refresh(stored, bundle, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	checkNoError(t, err)
	section := mustDecode(t, refreshed)["sections"].([]any)[0].(map[string]any)
	checkEqual(t, sectionDetailsFailed, section["details"])
	checkEqual(t, nil, section["summary"])
	if findings := validator.ValidateStored(refreshed, nil); findings != nil {
		t.Fatalf("the refreshed document is rejected: %q", findings)
	}
	// A complete section is stored without the field.
	if strings.Contains(string(published), `"details"`) {
		t.Fatalf("a complete document carries details: %s", published)
	}
}

// The details state is the host's: the model never writes it, so neither the
// authored contract nor any schema a model is given offers it, and an
// authored section still needs its summary.
func TestModelSchemasDoNotOfferSectionDetails(t *testing.T) {
	validator, err := sharedValidator()
	checkNoError(t, err)
	contract, err := json.Marshal(validator.Contract())
	checkNoError(t, err)
	none, err := validator.ModelSchema(nil)
	checkNoError(t, err)
	all, err := validator.ModelSchema(tilecatalogue.Kinds())
	checkNoError(t, err)
	for name, schema := range map[string][]byte{"contract": contract, "model": validator.Schema, "model without tiles": none, "model with tiles": all} {
		if strings.Contains(string(schema), `"details"`) {
			t.Errorf("%s schema offers section details", name)
		}
	}
	section := validator.Contract()["$defs"].(map[string]any)["section"].(map[string]any)
	checkEqual(t, "object", section["properties"].(map[string]any)["summary"].(map[string]any)["type"])
	if !containsString(stringSlice(section["required"]), "summary") {
		t.Fatalf("an authored section does not require its summary: %v", section["required"])
	}

	doc := mustDecode(t, authoredFixture())
	doc["sections"].([]any)[0].(map[string]any)["details"] = sectionDetailsFailed
	authored, err := json.Marshal(doc)
	checkNoError(t, err)
	if findings := validator.Validate(authored, nil); !strings.Contains(findings, "details") {
		t.Fatalf("an authored section with details was accepted: %q", findings)
	}
}

// A section the next version drops is Obsolete: nothing will write details
// it was still waiting for, so a pending one is carried as failed, and a
// failed one stays failed.
func TestAnObsoleteSectionNeverWaitsForDetails(t *testing.T) {
	for _, prior := range []string{sectionDetailsPending, sectionDetailsFailed} {
		dropped := historySection("gone", testHunk)
		withoutDetails(dropped, prior, true)
		content, err := json.Marshal(dropped)
		checkNoError(t, err)
		previous := &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_gone", Content: content}}}
		candidate := historyCandidate(t, []any{historySection("new", testHunk)})
		for name, carry := range map[string]func() ([]byte, error){
			"completion": func() ([]byte, error) { return reconcileAuthoredHistory(candidate, previous, nil, nil, true) },
			"restore":    func() ([]byte, error) { return RestoreOmittedPreviousSections(candidate, previous, nil) },
		} {
			document, err := carry()
			checkNoError(t, err)
			section := mustDecode(t, document)["sections"].([]any)[0].(map[string]any)
			checkEqual(t, "sec_gone", section["id"])
			if section["details"] != sectionDetailsFailed {
				t.Errorf("%s of a %s section: details = %v, want failed", name, prior, section["details"])
			}
		}
	}
}

func TestCompleteCarriesAnObsoleteSectionWithFailedDetails(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	previous := mustDecode(t, published)["sections"].([]any)[0].(map[string]any)
	withoutDetails(previous, sectionDetailsFailed, true)
	content, err := json.Marshal(previous)
	checkNoError(t, err)
	bundle.Previous = &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: previous["id"].(string), Content: content}}}
	candidate := historyCandidate(t, []any{historySection("new", testHunk)})
	completed, findings := validator.CompleteWithPrompt(candidate, bundle, t.TempDir(), "model", "prompt", time.Now())
	checkEqual(t, "", findings)
	sections := mustDecode(t, completed)["sections"].([]any)
	checkEqual(t, previous["id"], sections[0].(map[string]any)["id"])
	checkEqual(t, sectionDetailsFailed, sections[0].(map[string]any)["details"])
	checkEqual(t, nil, sections[0].(map[string]any)["summary"])
	if findings := validator.ValidateStored(completed, bundle); len(findings) > 0 {
		t.Fatalf("completed document rejected: %v", findings)
	}
}

// Only a live draft holds pending details: before a version is published or
// saved every pending section becomes failed, and nothing else changes.
func TestFailPendingDetails(t *testing.T) {
	doc := storedExample(t)
	sections := doc["sections"].([]any)
	withoutDetails(sections[0].(map[string]any), sectionDetailsPending, true)
	withoutDetails(sections[1].(map[string]any), sectionDetailsFailed, false)
	draft, err := json.Marshal(doc)
	checkNoError(t, err)
	got := mustDecode(t, FailPendingDetails(draft))
	sections[0].(map[string]any)["details"] = sectionDetailsFailed
	checkEqual(t, doc, got)
	if findings := validateStored(t, got, nil); findings != nil {
		t.Fatalf("published draft rejected: %q", findings)
	}
	// A document without pending details is returned as it is.
	for _, unchanged := range []string{`{"sections":[{"id":"sec_a","details":"failed"}]}`, "not json"} {
		checkEqual(t, unchanged, string(FailPendingDetails([]byte(unchanged))))
	}
}

package review

import (
	"encoding/json"
	"testing"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func assessedSection(id string, level int, reasons ...string) documentSection {
	s := documentSection{sectionContent: sectionContent{ID: id, Kind: "logical"}, Derived: documentDerived{State: "new"}}
	s.Assessment = &sectionAssessment{Level: level, Factors: []string{}}
	for _, text := range reasons {
		s.Assessment.Reasons = append(s.Assessment.Reasons, sectionAssessmentReason{Text: text})
	}
	return s
}

func withRisk(s documentSection, id, tier, state string) documentSection {
	s.Risks = append(s.Risks, documentRisk{authoredRisk: authoredRisk{ID: id, Tier: tier, Text: id + " text"}, State: state})
	return s
}

func scoreOf(t *testing.T, result *documentRiskScore) int {
	t.Helper()
	if result.Score == nil {
		t.Fatalf("no score: %+v", result)
	}
	return *result.Score
}

func TestRiskScoreRatesASensitiveChangeWithoutAFinding(t *testing.T) {
	result := riskScore([]documentSection{assessedSection("sec_auth", 4, "Changes who may approve a review.")}, nil)
	checkEqual(t, "assessed", result.Status)
	checkEqual(t, RiskRubricVersion, result.Rubric)
	checkEqual(t, 4, scoreOf(t, result))
	checkEqual(t, []documentRiskScoreReason{{Section: "sec_auth", Text: "Changes who may approve a review."}}, result.Reasons)
	checkEqual(t, []documentRiskLimitation{}, result.Limitations)
}

func TestRiskScoreIsNotDilutedByHarmlessSections(t *testing.T) {
	sensitive := assessedSection("sec_migration", 4, "Drops a column.")
	alone := scoreOf(t, riskScore([]documentSection{sensitive}, nil))
	mechanical := documentSection{sectionContent: sectionContent{ID: "sec_fmt", Kind: "mechanical"}, Derived: documentDerived{State: "new"}}
	var many []documentSection
	for range 20 {
		many = append(many, assessedSection("sec_docs", 1, "Rewords a comment."))
	}
	many = append(many, mechanical, sensitive, assessedSection("sec_ui", 2, "Moves a button."))
	checkEqual(t, alone, scoreOf(t, riskScore(many, nil)))
}

func TestRiskScoreFloorsFollowActiveFindings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tier  string
		state string
		want  int
	}{
		{"high", "high", "active", 4},
		{"critical", "critical", "active", 5},
		{"medium sets no floor", "medium", "active", 2},
		{"mitigated", "critical", "mitigated", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section := withRisk(assessedSection("sec_a", 2, "Localized."), "rsk_a", tc.tier, tc.state)
			checkEqual(t, tc.want, scoreOf(t, riskScore([]documentSection{section}, nil)))
		})
	}
	// A floor never lowers a higher assessment.
	section := withRisk(assessedSection("sec_a", 5, "Critical path."), "rsk_a", "high", "active")
	checkEqual(t, 5, scoreOf(t, riskScore([]documentSection{section}, nil)))
}

func TestRiskScoreIgnoresObsoleteSections(t *testing.T) {
	stale := withRisk(assessedSection("sec_old", 5, "Old concern."), "rsk_old", "critical", "active")
	stale.Derived.State = "stale"
	result := riskScore([]documentSection{stale, assessedSection("sec_new", 2, "Localized.")}, nil)
	checkEqual(t, 2, scoreOf(t, result))
	checkEqual(t, "sec_new", result.Reasons[0].Section)
	// Only obsolete sections left: nothing current was assessed.
	result = riskScore([]documentSection{stale}, nil)
	checkEqual(t, "not_assessed", result.Status)
	checkEqual(t, (*int)(nil), result.Score)
}

func TestRiskScoreIsALowerBoundWhenInputIsIncomplete(t *testing.T) {
	unassessed := documentSection{sectionContent: sectionContent{ID: "sec_unknown", Kind: "logical"}, Derived: documentDerived{State: "new"}}
	result := riskScore([]documentSection{assessedSection("sec_a", 3, "Shared code."), unassessed}, nil)
	checkEqual(t, "lower_bound", result.Status)
	checkEqual(t, 3, scoreOf(t, result))
	checkEqual(t, []documentRiskLimitation{{Kind: "unassessed_sections", Count: 1}}, result.Limitations)

	diff := []bundlepkg.Hunk{
		{ID: "h_1", Path: "big.go", TextlessReason: "omitted_size"},
		{ID: "h_2", Path: "big.go", TextlessReason: "omitted_size"},
		{ID: "h_3", Path: "vendor", TextlessReason: "omitted_submodule"},
		// Cut for size, but the file is still in the checkout.
		{ID: "h_4", Path: "long.go", TextlessReason: "trimmed_oversized"},
		{ID: "h_5", Path: "logo.png", TextlessReason: "binary"},
		// A lockfile or vendored copy carries no behavior of its own.
		{ID: "h_6", Path: "pnpm-lock.yaml", Role: bundlepkg.RoleGenerated, TextlessReason: "omitted_size"},
		{ID: "h_7", Path: "vendor/lib.go", Role: bundlepkg.RoleVendored, TextlessReason: "omitted_size"},
	}
	result = riskScore([]documentSection{assessedSection("sec_a", 2, "Localized.")}, diff)
	checkEqual(t, "lower_bound", result.Status)
	checkEqual(t, []documentRiskLimitation{{Kind: "omitted_source", Count: 2}}, result.Limitations)

	result = riskScore([]documentSection{assessedSection("sec_a", 2, "Localized.")}, diff[3:])
	checkEqual(t, "assessed", result.Status)
}

func TestRiskScoreWithoutAssessmentsIsNotAssessedUnlessAFindingSetsAFloor(t *testing.T) {
	legacy := documentSection{sectionContent: sectionContent{ID: "sec_a", Kind: "logical"}, Derived: documentDerived{State: "new"}}
	result := riskScore([]documentSection{legacy}, nil)
	checkEqual(t, "not_assessed", result.Status)
	checkEqual(t, (*int)(nil), result.Score)
	checkEqual(t, []documentRiskScoreReason{}, result.Reasons)
	checkEqual(t, []documentRiskLimitation{{Kind: "unassessed_sections", Count: 1}}, result.Limitations)

	result = riskScore([]documentSection{withRisk(legacy, "rsk_a", "critical", "active")}, nil)
	checkEqual(t, "lower_bound", result.Status)
	checkEqual(t, 5, scoreOf(t, result))

	mechanical := documentSection{sectionContent: sectionContent{ID: "sec_fmt", Kind: "mechanical"}, Derived: documentDerived{State: "new"}}
	result = riskScore([]documentSection{mechanical}, nil)
	checkEqual(t, "assessed", result.Status)
	checkEqual(t, 1, scoreOf(t, result))
	checkEqual(t, []documentRiskScoreReason{}, result.Reasons)
}

func TestRiskScoreReasonsLeadWithFindingsThenAlternateSections(t *testing.T) {
	a := assessedSection("sec_a", 4, "A1", "A2", "A3")
	b := withRisk(assessedSection("sec_b", 4, "B1"), "rsk_b", "high", "active")
	low := assessedSection("sec_low", 2, "L1")
	result := riskScore([]documentSection{a, low, b}, nil)
	risk := "rsk_b"
	checkEqual(t, []documentRiskScoreReason{
		{Section: "sec_b", Risk: &risk, Text: "rsk_b text"},
		{Section: "sec_a", Text: "A1"},
		{Section: "sec_b", Text: "B1"},
	}, result.Reasons)
}

func TestRiskScoreReasonsComeOnlyFromWhatSetsTheScore(t *testing.T) {
	// The section's own high risk lifts it above its assessment, so the
	// assessment's reason no longer explains the score.
	section := withRisk(assessedSection("sec_a", 3, "Shared parser."), "rsk_a", "high", "active")
	result := riskScore([]documentSection{section}, nil)
	risk := "rsk_a"
	checkEqual(t, 4, scoreOf(t, result))
	checkEqual(t, []documentRiskScoreReason{{Section: "sec_a", Risk: &risk, Text: "rsk_a text"}}, result.Reasons)
}

func TestSectionReviewScoreFollowsTheRiskScoreRules(t *testing.T) {
	unassessed := documentSection{sectionContent: sectionContent{ID: "sec_a", Kind: "logical"}, Derived: documentDerived{State: "new"}}
	mechanical := documentSection{sectionContent: sectionContent{ID: "sec_fmt", Kind: "mechanical"}, Derived: documentDerived{State: "new"}}
	stale := withRisk(assessedSection("sec_old", 5, "Old."), "rsk_old", "critical", "active")
	stale.Derived.State = "stale"
	risk := func(id string) *string { return &id }
	for _, tc := range []struct {
		name    string
		section documentSection
		score   int
		reason  *documentReviewReason
	}{
		{"assessed", assessedSection("sec_a", 3, "Shared parser.", "Second."), 3, &documentReviewReason{Text: "Shared parser."}},
		{"assessed without reasons", assessedSection("sec_a", 2), 2, nil},
		{"mechanical", mechanical, 1, nil},
		{"a medium risk sets no floor", withRisk(assessedSection("sec_a", 2, "Localized."), "rsk_a", "medium", "active"), 2, &documentReviewReason{Text: "Localized."}},
		{"a high risk lifts it", withRisk(assessedSection("sec_a", 2, "Localized."), "rsk_a", "high", "active"), 4, &documentReviewReason{Risk: risk("rsk_a"), Text: "rsk_a text"}},
		{"a risk at the level explains it first", withRisk(assessedSection("sec_a", 4, "Permissions."), "rsk_a", "high", "active"), 4, &documentReviewReason{Risk: risk("rsk_a"), Text: "rsk_a text"}},
		{"a floor never lowers it", withRisk(assessedSection("sec_a", 5, "Critical path."), "rsk_a", "high", "active"), 5, &documentReviewReason{Text: "Critical path."}},
		{"a mitigated risk takes no part", withRisk(assessedSection("sec_a", 2, "Localized."), "rsk_a", "critical", "mitigated"), 2, &documentReviewReason{Text: "Localized."}},
		{"unassessed", unassessed, 0, nil},
		{"unassessed with a critical risk", withRisk(unassessed, "rsk_a", "critical", "active"), 5, &documentReviewReason{Risk: risk("rsk_a"), Text: "rsk_a text"}},
		{"stale", stale, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			score, reason := sectionReviewScore(&tc.section)
			checkEqual(t, tc.score, score)
			checkEqual(t, tc.reason, reason)
		})
	}
}

func TestTheFirstSectionInReviewOrderCarriesThePullRequestScore(t *testing.T) {
	unassessed := documentSection{sectionContent: sectionContent{ID: "sec_u", Kind: "logical"}, Derived: documentDerived{State: "new"}}
	mechanical := documentSection{sectionContent: sectionContent{ID: "sec_fmt", Kind: "mechanical"}, Derived: documentDerived{State: "new"}}
	stale := withRisk(assessedSection("sec_old", 5, "Old."), "rsk_old", "critical", "active")
	stale.Derived.State = "stale"
	for name, sections := range map[string][]documentSection{
		"levels only":       {assessedSection("sec_a", 2), assessedSection("sec_b", 4), mechanical},
		"floor above level": {assessedSection("sec_a", 3), withRisk(assessedSection("sec_b", 2), "rsk_b", "critical", "active")},
		"unassessed floor":  {assessedSection("sec_a", 3), withRisk(unassessed, "rsk_u", "high", "active")},
		"stale excluded":    {stale, assessedSection("sec_a", 2), mechanical},
		"mechanical only":   {mechanical},
	} {
		t.Run(name, func(t *testing.T) {
			highest := 0
			for i := range sections {
				score, _ := sectionReviewScore(&sections[i])
				highest = max(highest, score)
			}
			checkEqual(t, highest, scoreOf(t, riskScore(sections, nil)))
		})
	}
}

func TestNormalizationShapesSectionAssessments(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	doc, err := decodeObject(authoredFixture(), "candidate")
	checkNoError(t, err)
	logical := doc["sections"].([]any)[0].(map[string]any)
	logical["assessment"] = map[string]any{"level": 3, "factors": []any{"public_api", "public_api"}, "reasons": []any{map[string]any{"text": "Changes a shared parser.", "anchors": []any{}}}}
	mechanical, err := decodeObject(tileJSON(t, logical), "section")
	checkNoError(t, err)
	mechanical["id"], mechanical["kind"] = "sec_fmt", "mechanical"
	nullAssessment, err := decodeObject(tileJSON(t, logical), "section")
	checkNoError(t, err)
	nullAssessment["id"], nullAssessment["assessment"] = "sec_null", nil
	// A disappeared section has no hunks to anchor reasons to and takes no
	// part in the score, so its assessment must not cost a repair.
	disappeared, err := decodeObject(tileJSON(t, logical), "section")
	checkNoError(t, err)
	disappeared["id"], disappeared["hunks"] = "sec_gone", []any{}
	disappeared["summary"] = map[string]any{"text": "Removed.", "anchors": []any{map[string]any{"type": "file", "path": "main.go"}}}
	bundle.Previous = &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_gone", Kind: "logical", Content: tileJSON(t, disappeared)}}}
	doc["sections"] = []any{disappeared, logical, mechanical, nullAssessment}
	prepared, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir())
	if findings != "" {
		t.Fatal(findings)
	}
	var result map[string]any
	checkNoError(t, json.Unmarshal(prepared, &result))
	sections := result["sections"].([]any)
	assertSameDocumentJSON(t, []byte(`{"level":3,"factors":["public_api"],"reasons":[{"text":"Changes a shared parser.","anchors":[{"type":"hunk","id":"h_0123456789ab"}]}]}`), tileJSON(t, sections[1].(map[string]any)["assessment"]))
	for _, i := range []int{0, 2, 3} {
		if _, present := sections[i].(map[string]any)["assessment"]; present {
			t.Fatalf("section %d kept an assessment: %v", i, sections[i])
		}
	}
}

func TestSchemaRejectsAnAssessmentOutsideTheScale(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go"}}}
	doc, err := decodeObject(authoredFixture(), "candidate")
	checkNoError(t, err)
	doc["sections"].([]any)[0].(map[string]any)["assessment"] = map[string]any{"level": 7, "factors": []any{}, "reasons": []any{map[string]any{"text": "Too high."}}}
	if _, findings := v.Prepare(tileJSON(t, doc), bundle, t.TempDir()); findings == "" {
		t.Fatal("a level outside 1-5 was accepted")
	}
}

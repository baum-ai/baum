package review

import (
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

const outlineCitesItems = "The outline can cite only section IDs; cite the section"

func TestOutlineCanCiteSectionIDsOnly(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	prepare := func(text string, refs ...any) string {
		reply := stagedOutlineReply(t, func(outline map[string]any) {
			summary := outline["summary"].(map[string]any)
			summary["text"] = text
			summary["recommendation"] = map[string]any{"decision": "look_first", "text": "Start with the parser.", "refs": refs}
		})
		_, findings := v.PrepareOutline(reply, b, t.TempDir())
		return findings
	}
	checkEqual(t, "", prepare("See sec_parser and "+testHunk+".", "sec_parser", "sec_tests"))
	for _, id := range []string{"rsk_cache", "ev_tests", "dec_choice"} {
		findings := prepare("The risk "+id+" matters.", "sec_parser")
		if !strings.Contains(strings.ToLower(findings), strings.ToLower(outlineCitesItems)) || !strings.Contains(findings, id) {
			t.Errorf("%s in outline prose: findings %q", id, findings)
		}
		findings = prepare("Plain text.", id)
		if !strings.Contains(findings, `summary recommendation ref "`+id+`": the outline can cite only section IDs; cite the section`) {
			t.Errorf("%s in outline refs: findings %q", id, findings)
		}
	}
	if findings := prepare("Plain text.", "sec_missing"); !strings.Contains(findings, "sec_missing") {
		t.Errorf("an unknown section ref: findings %q", findings)
	}
}

func TestDetailsMayCiteTheirOwnItemsAndAnySection(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	outline := preparedOutline(t, v, b)
	with := func(text string) string {
		details := stagedDetails("sec_parser", testHunk)
		details["risks"] = []any{proseRisk("rsk_own", "The parser rejects valid input.")}
		details["summary"].(map[string]any)["text"] = text
		_, findings := v.PrepareSectionDetails(outline, encodeJSON(t, details), b, t.TempDir())
		return findings
	}
	checkEqual(t, "", with("See rsk_own, sec_parser and sec_tests."))
	if findings := with("See ev_tests in the other section."); !strings.Contains(findings, `"ev_tests" is not a known`) {
		t.Errorf("another section's item: findings %q", findings)
	}
}

// The final assembly runs no prose rule: the stages checked what they wrote,
// and an outline that cites a risk its section never wrote must not fail the
// whole run with nothing left to repair.
func TestFinalAssemblyAppliesNoProseRule(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	directory := t.TempDir()
	outline := preparedOutline(t, v, b)
	details := stagedDetails("sec_parser", testHunk)
	details["risks"] = []any{proseRisk("rsk_own", "The parser rejects valid input.")}
	section, findings := v.PrepareSectionDetails(outline, encodeJSON(t, details), b, directory)
	checkEqual(t, "", findings)
	parts := StagedParts{Outline: outline, Sections: map[string][]byte{"sec_parser": section}}

	// An outline whose recommendation names a risk nobody wrote, and prose that
	// would be a finding in a stage, as an older outline may carry.
	root := mustDecode(t, outline)
	summary := root["summary"].(map[string]any)
	summary["text"] = "The check was still in progress; see rsk_never_written."
	summary["recommendation"] = map[string]any{"decision": "look_first", "text": "Start with rsk_never_written.", "refs": []any{"rsk_never_written"}}
	parts.Outline = encodeJSON(t, root)
	final, err := AssembleStaged(parts, true)
	checkNoError(t, err)
	prepared, findings := v.PrepareStaged(final, b, directory)
	checkEqual(t, "", findings)
	if _, findings := v.CompleteStaged(prepared, b, directory, "model", "staged-v1", time.Now()); findings != "" {
		t.Fatalf("the final assembly was rejected: %s", findings)
	}
	// The same document written in one pass is checked in full.
	if _, findings := v.Prepare(stagedAsSinglePass(t, parts.Outline, section), b, directory); !strings.Contains(findings, "rsk_never_written") {
		t.Errorf("a one-pass document with an unresolved ID: findings %q", findings)
	}
}

func stagedAsSinglePass(t *testing.T, outline, section []byte) []byte {
	t.Helper()
	root := mustDecode(t, outline)
	sections := root["sections"].([]any)
	complete := mustDecode(t, section)
	sections[0] = complete
	other := stagedDetails("sec_tests", stagedTestHunk)
	other["title"], other["kind"], other["hunks"], other["assessment"] = "Tests", "logical", []any{stagedTestHunk}, stagedAssessment(stagedTestHunk)
	sections[1] = other
	root["summary"].(map[string]any)["context"] = map[string]any{"area": map[string]any{"text": "The parser.", "refs": []any{}}}
	root["summary"].(map[string]any)["review_minutes"] = 3
	return encodeJSON(t, root)
}

func TestAStoredOutlineIsPreparedWithoutTheProseRules(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	noisy := stagedOutlineReply(t, func(outline map[string]any) {
		summary := outline["summary"].(map[string]any)
		summary["text"] = "The Open-source module check was still in progress when the bundle was taken; see rsk_old."
		summary["recommendation"] = map[string]any{"decision": "look_first", "text": "I read rsk_old.", "refs": []any{"rsk_old"}}
	})
	if _, findings := v.PrepareOutline(noisy, b, t.TempDir()); findings == "" {
		t.Fatal("a newly written outline with old noise was accepted")
	}
	prepared, findings := v.PrepareStoredOutline(noisy, b, t.TempDir())
	checkEqual(t, "", findings)
	// The continued outline still takes the context and details stages.
	_, findings = v.PrepareContext(prepared, []byte(`{"context":{"area":{"text":"The parser.","refs":[]}}}`), b, t.TempDir())
	checkEqual(t, "", findings)
	// Structure is still checked.
	broken := stagedOutlineReply(t, func(outline map[string]any) { delete(outline, "sections") })
	if _, findings := v.PrepareStoredOutline(broken, b, t.TempDir()); findings == "" {
		t.Error("a stored outline without sections was accepted")
	}
}

func TestReusedDetailsKeepTheWordingRulesButNotIDResolution(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	outline := preparedOutline(t, v, b)
	details := func(text string) []byte {
		d := stagedDetails("sec_parser", testHunk)
		d["risks"] = []any{proseRisk("rsk_own", text)}
		return encodeJSON(t, d)
	}
	// A check and a hunk of an earlier head do not exist in this bundle.
	old := "Failed in check_run:999 at " + stagedTestHunk[:2] + "_ffffffffffff and PRRT_gone."
	if _, findings := v.PrepareReusedSectionDetails(outline, details(old), b, t.TempDir()); findings != "" {
		t.Errorf("reused details rejected for IDs: %s", findings)
	}
	if _, findings := v.PrepareSectionDetails(outline, details(old), b, t.TempDir()); !strings.Contains(findings, "check_run:999") {
		t.Errorf("fresh details with unknown IDs: findings %q", findings)
	}
	noisy := "The check was still in progress."
	for name, prepare := range map[string]func([]byte, []byte, *bundlepkg.Bundle, string) ([]byte, string){"reused": v.PrepareReusedSectionDetails, "fresh": v.PrepareSectionDetails} {
		if _, findings := prepare(outline, details(noisy), b, t.TempDir()); !strings.Contains(findings, "still in progress") {
			t.Errorf("%s details with run-state wording: findings %q", name, findings)
		}
	}
}

// A continued run's stored outline is unchecked, and a details reply cannot
// change its section titles or assessments: the details of its sections are
// checked without them.
func TestDetailsDoNotCheckTheOutlinesTitleAndAssessment(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	b := stagedBundle()
	noisy := stagedOutlineReply(t, func(outline map[string]any) {
		section := outline["sections"].([]any)[0].(map[string]any)
		section["title"] = "ENG" + "-1893: Parser"
		section["assessment"].(map[string]any)["reasons"] = []any{map[string]any{"text": "The check was still in progress.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}
	})
	outline, findings := v.PrepareStoredOutline(noisy, b, t.TempDir())
	checkEqual(t, "", findings)
	details := encodeJSON(t, stagedDetails("sec_parser", testHunk))
	for name, prepare := range map[string]func([]byte, []byte, *bundlepkg.Bundle, string) ([]byte, string){"fresh": v.PrepareSectionDetails, "reused": v.PrepareReusedSectionDetails} {
		if _, findings := prepare(outline, details, b, t.TempDir()); findings != "" {
			t.Errorf("%s details rejected for the outline's title or assessment: %s", name, findings)
		}
	}
	// Written fresh, the same outline is rejected.
	if _, findings := v.PrepareOutline(noisy, b, t.TempDir()); !strings.Contains(findings, "ticket key") || !strings.Contains(findings, "still in progress") {
		t.Errorf("a new outline with a noisy title and assessment: findings %q", findings)
	}
}

func TestHostBuiltExchangeIsNotReadByTheProseRules(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	doc := mustDecode(t, authoredFixture())
	doc["summary"].(map[string]any)["review_exchange"] = map[string]any{"summary": "The check was still running when the bundle was taken; PRRT_kwDOTPgJG862TD-9 is open."}
	if _, findings := v.PrepareHostExchange(encodeJSON(t, doc), proseBundle(), t.TempDir()); findings != "" {
		t.Errorf("host-built exchange rejected: %s", findings)
	}
	if _, findings := v.Prepare(encodeJSON(t, doc), proseBundle(), t.TempDir()); !strings.Contains(findings, "review_exchange summary") {
		t.Errorf("a model-written exchange with noise: findings %q", findings)
	}
	// Everything else is still read.
	doc["summary"].(map[string]any)["text"] = "The check was still running."
	if _, findings := v.PrepareHostExchange(encodeJSON(t, doc), proseBundle(), t.TempDir()); !strings.Contains(findings, "summary text") {
		t.Errorf("findings %q", findings)
	}
}

func TestHostExchangeScopeStillChecksTheRecommendation(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	doc := mustDecode(t, authoredFixture())
	doc["summary"].(map[string]any)["recommendation"] = map[string]any{"decision": "look_first", "text": "Look at the parser first.", "refs": []any{"sec_gone"}}
	if _, findings := v.PrepareHostExchange(encodeJSON(t, doc), proseBundle(), t.TempDir()); !strings.Contains(findings, `"sec_gone"`) {
		t.Errorf("an unresolved recommendation ref in a stack review: findings %q", findings)
	}
}

func TestExtractedDecisionsAreNotReadByTheProseRules(t *testing.T) {
	decision := func(extracted bool) string {
		return proseFindingsOf(t, func(summary, section map[string]any) {
			item := map[string]any{
				"id": "dec_choice", "title": "ENG" + "-1799: Chose a slice", "by": "agent", "text": "Chose a slice.",
				"rationale": "Kept 0123456789abcdef0123456789abcdef01234567 as the base.",
				"anchors":   []any{map[string]any{"type": "hunk", "id": testHunk}},
			}
			if extracted {
				item["decision_id"] = "0b1c2d3e-4f50-4a6b-8c7d-9e0f1a2b3c4d"
			}
			section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{item}}}
		})
	}
	checkEqual(t, "", decision(true))
	if findings := decision(false); !strings.Contains(findings, "ticket") || !strings.Contains(findings, "0123456789abcdef") {
		t.Errorf("a model-written decision with a ticket key and a full SHA: findings %q", findings)
	}
}

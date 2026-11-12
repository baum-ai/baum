package review

import (
	"fmt"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

const proseSession = "11111111-2222-3333-4444-555555555555"

func proseBundle() *bundlepkg.Bundle {
	return &bundlepkg.Bundle{
		Repository:   "org/repo",
		PullRequest:  bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources:      bundlepkg.Sources{"diff": true},
		Diff:         []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}},
		Checks:       []bundlepkg.Check{{ID: "check_run:123", Name: "CI", Kind: "check_run"}, {ID: "status:456", Name: "lint", Kind: "status"}},
		Threads:      []bundlepkg.Thread{{ID: "PRRT_kwDOabc"}, {ID: "PRRT_kwDOTPgJG862TD-9"}},
		Conversation: []bundlepkg.ConversationItem{{ID: "123456789012", Kind: "comment"}},
		Sessions: []bundlepkg.Session{
			{RootSessionID: proseSession, Vendor: "claude"},
			{RootSessionID: "99999999-2222-3333-4444-555555555555", Vendor: "claude", Restricted: true},
		},
	}
}

func proseRisk(id, text string) map[string]any {
	return map[string]any{"id": id, "tier": "low", "category": "api_compat", "source": "model", "text": text, "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}
}

func proseEvidence(id, kind, claim, status string) map[string]any {
	item := map[string]any{"id": id, "kind": kind, "claim": claim, "status": status, "source": "model", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}
	if status == "missing" {
		item["expects"] = "A test of it."
	}
	return item
}

// proseFindingsOf prepares the authored fixture changed by mutate, as
// generation does, and returns the findings.
func proseFindingsOf(t *testing.T, mutate func(summary, section map[string]any)) string {
	t.Helper()
	v, err := sharedValidator()
	checkNoError(t, err)
	doc := mustDecode(t, authoredFixture())
	mutate(doc["summary"].(map[string]any), doc["sections"].([]any)[0].(map[string]any))
	_, findings := v.Prepare(tileJSON(t, doc), proseBundle(), t.TempDir())
	return findings
}

func TestProseFixtureIsClean(t *testing.T) {
	checkEqual(t, "", proseFindingsOf(t, func(summary, section map[string]any) {}))
}

// judged prepares a document whose first risk has the text, which is a field
// that carries the review's judgement, and returns the findings.
func judged(t *testing.T, text string) string {
	t.Helper()
	return proseFindingsOf(t, func(summary, section map[string]any) {
		section["risks"] = []any{proseRisk("rsk_judged", text)}
	})
}

// requireJudged asserts the text is flagged at the risk's location, quoting
// the matched phrase.
func requireJudged(t *testing.T, text, phrase string) {
	t.Helper()
	want := fmt.Sprintf("section 1 (sec_parser) risk rsk_judged text: %q ", phrase)
	if findings := judged(t, text); !strings.Contains(findings, want) {
		t.Errorf("%q: findings %q lack %q", text, findings, want)
	}
}

func requireNotJudged(t *testing.T, text string) {
	t.Helper()
	if findings := judged(t, text); findings != "" {
		t.Errorf("%q rejected: %s", text, findings)
	}
}

func TestRunStateWording(t *testing.T) {
	for text, phrase := range map[string]string{
		"The Open-source module check was still in progress when the bundle was taken.": "still in progress",
		"The deploy job is still pending.":                                              "still pending",
		"The CI run had not yet finished.":                                              "had not yet finished",
		"The lint workflow was running at the snapshot.":                                "at the snapshot",
		"The check has not finished.":                                                   "has not finished",
		"The suite was queued.":                                                         "was queued",
		"The pipeline is pending.":                                                      "is pending",
		"The bot was running.":                                                          "was running",
		"The tests were not yet completed.":                                             "not yet completed",
	} {
		requireJudged(t, text, phrase)
	}
	// Product text that only looks like run state.
	for _, text := range []string{
		"A feature in progress ships behind a flag.",
		"In-progress sessions are listed first.",
		"The migration is pending approval from the owner.",
		"Sessions that are in progress keep their draft.",
		"The queue was pending a rebuild of the index.",
		"The check passes on every commit.",
		"Drain waited until no job was queued or running.",
		"It copies only fix jobs that are queued or running.",
		"The review is pending.",
		"The run is queued behind another.",
	} {
		requireNotJudged(t, text)
	}
	// The rules read the review's judgement only, not fields that describe the
	// code: a behaviour step may say a background command is still running.
	findings := proseFindingsOf(t, func(summary, section map[string]any) {
		section["blocks"] = []any{map[string]any{"kind": "behavior", "steps": []any{map[string]any{"id": "beh_wait", "title": "Wait", "text": "A background command is still running.", "before": "Report usage while a run is in progress.", "after": "The bundle was taken.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}}}
		section["title"] = "The snapshot at the bundle"
	})
	checkEqual(t, "", findings)
}

func TestMetaTalkWording(t *testing.T) {
	for text, phrase := range map[string]string{
		"This is not in the bundle.":                  "in the bundle",
		"The bundle was truncated.":                   "The bundle was",
		"The excerpts show the call.":                 "The excerpts show",
		"Nothing in the excerpt proves it.":           "in the excerpt",
		"It is not visible in the supplied snapshot.": "in the supplied snapshot",
		"The captured snapshot lacks the logs.":       "The captured snapshot",
		"The section details stage wrote this.":       "section details stage",
		"The outline stage chose it.":                 "outline stage",
		"Not found in the head checkout.":             "in the head checkout",
		"The helper is absent in the checkout shown.": "in the checkout shown",
		"I read the handler and it is fine.":          "I read",
		"I couldn't find a caller.":                   "I couldn't",
		"My reading is that it is safe.":              "My reading",
		"It was not available at bundle time.":        "bundle time",
	} {
		requireJudged(t, text, phrase)
	}
	for _, text := range []string{
		"The JS bundle size grows by 4 KB.",
		"The app is bundled with the installer.",
		"The snapshot test covers the output.",
		"The snapshot isolation level is unchanged.",
		"The checkout page keeps the cart.",
		"Keep the failed step of a job log in the fix agent's excerpt.",
		"Code excerpts add no time to the estimate.",
		"It rebuilds a repository's snapshot and the snapshot reads the head.",
		"The snapshot's NextPoll is set once.",
		"Committed links stay inside the checkout.",
		"The reader checked the result.",
		"The checkout flow now validates the address.",
	} {
		requireNotJudged(t, text)
	}
	// Look-alikes from the code under review, in fields the rules do not read.
	for _, text := range []string{"Clear the AppImage magic bytes at bundle time.", "After the bundle step, the same job installs Chromium."} {
		findings := proseFindingsOf(t, func(summary, section map[string]any) {
			section["blocks"] = []any{map[string]any{"kind": "behavior", "steps": []any{map[string]any{"id": "beh_build", "title": "Build", "text": text, "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}}}
			section["summary"].(map[string]any)["text"] = text
		})
		checkEqual(t, "", findings)
	}
	requireNotJudged(t, "After the bundle step, the same job installs Chromium.")
}

func TestIDsInProseMustResolve(t *testing.T) {
	section := func(text string, mutate func(section map[string]any)) string {
		return proseFindingsOf(t, func(summary, section map[string]any) {
			section["summary"].(map[string]any)["text"] = text
			section["risks"] = []any{proseRisk("rsk_real", "A real risk about the parser.")}
			section["evidence"] = []any{proseEvidence("ev_real", "review", "Reviewed by a person.", "asserted")}
			if mutate != nil {
				mutate(section)
			}
		})
	}
	resolvable := []string{
		"Calls " + testHunk + " from here.",
		"See sec_parser and rsk_real and ev_real.",
		"Fails in check_run:123 and status:456.",
		"Thread PRRT_kwDOabc is open.",
		"Thread PRRT_kwDOTPgJG862TD-9 is open.",
		"As said in comment 123456789012, this changed.",
		"As said in `123456789012`, this changed.",
		"Written in session " + proseSession + ".",
		"At commit abcdef1 and abcdef1234567.",
		"Without ids, version 2024 and comment 12 are fine.",
	}
	for _, text := range resolvable {
		if findings := section(text, nil); findings != "" {
			t.Errorf("%q rejected: %s", text, findings)
		}
	}
	unknown := map[string]string{
		"Calls h_ffffffffffff here.":                  `"h_ffffffffffff" is not a known hunk ID`,
		"See rsk_gone.":                               `"rsk_gone" is not a known section, risk, evidence or decision ID`,
		"See ev_gone.":                                `"ev_gone" is not a known section`,
		"See dec_gone.":                               `"dec_gone" is not a known section`,
		"See sec_gone.":                               `"sec_gone" is not a known section`,
		"Fails in check_run:999.":                     `"check_run:999" is not a known check ID`,
		"Fails in status:999.":                        `"status:999" is not a known check ID`,
		"Thread PRRT_other-1 is open.":                `"PRRT_other-1" is not a known review thread ID`,
		"As said in comment 999999999, this changed.": `"comment 999999999" is not a known comment ID`,
		"As said in `999999999`, this changed.":       `"999999999" is not a known comment ID`,
		"Written in session 00000000-2222-3333-4444-555555555555.": "is not a known session ID",
		"Written in session 99999999-2222-3333-4444-555555555555.": "restricted sessions cannot be cited",
		"At commit " + strings.Repeat("a", 40) + ".":               "is a full SHA; write the 7-character short SHA",
	}
	for text, want := range unknown {
		if findings := section(text, nil); !strings.Contains(findings, want) || !strings.Contains(findings, "cite an existing one or drop it") && !strings.Contains(findings, "short SHA") {
			t.Errorf("%q: findings %q lack %q", text, findings, want)
		}
	}
	// A decision's ID resolves too.
	findings := section("Settled in dec_choice.", func(section map[string]any) {
		section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{map[string]any{"id": "dec_choice", "title": "Choice", "by": "agent", "text": "Chose a slice.", "rationale": "Simple.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}}}
	})
	if strings.Contains(findings, "dec_choice") {
		t.Errorf("decision ID rejected: %s", findings)
	}
}

func TestTicketKeyInTitles(t *testing.T) {
	titled := func(title string) string {
		return proseFindingsOf(t, func(summary, section map[string]any) { section["title"] = title })
	}
	for _, title := range []string{"ENG" + "-1893: Parser", "ENG" + "-1893 Parser", "ABC2-7: Parser", "Parser (ENG" + "-123)", "PROJ-9"} {
		if findings := titled(title); !strings.Contains(findings, "carries the ticket key") {
			t.Errorf("%q: findings %q", title, findings)
		}
	}
	for _, title := range []string{"Parser", "UTF-8 handling", "SHA-256 digests", "Fix ENG-style keys", "Parser v2"} {
		if findings := titled(title); findings != "" {
			t.Errorf("%q rejected: %s", title, findings)
		}
	}
}

func TestCIEvidenceIsRejected(t *testing.T) {
	// The authored contract has no ci kind; the schema rejects it.
	findings := proseFindingsOf(t, func(summary, section map[string]any) {
		section["evidence"] = []any{proseEvidence("ev_ci", "ci", "The checks pass.", "asserted")}
	})
	if !strings.Contains(findings, "ev_ci") || !strings.Contains(findings, "ci") {
		t.Errorf("a ci evidence item was accepted: %q", findings)
	}
	// The rule remains as a fallback for a path without the enum.
	section := map[string]any{"evidence": []any{proseEvidence("ev_ci", "ci", "The checks pass.", "asserted")}}
	got := strings.Join(sectionShapeFindings("section 1 (sec_parser)", section), "\n")
	for _, want := range []string{`section 1 (sec_parser) evidence ev_ci has kind "ci"`, `make it a "test" item whose claim names what the tests cover`} {
		if !strings.Contains(got, want) {
			t.Errorf("findings %q lack %q", got, want)
		}
	}
	checkEqual(t, "", proseFindingsOf(t, func(summary, section map[string]any) {
		section["evidence"] = []any{proseEvidence("ev_test", "test", "The parser tests cover the new grammar.", "asserted")}
	}))
	// A stored document keeps accepting it.
	stored := storedExample(t)
	firstSection(stored)["evidence"] = []any{proseEvidence("ev_old", "ci", "CI passed.", "asserted")}
	firstSection(stored)["attention"] = nil
	if findings := validateStored(t, stored, nil); findings != nil {
		t.Errorf("stored ci evidence rejected: %q", findings)
	}
}

func TestAbsenceClaimsNeedStatusMissing(t *testing.T) {
	for _, claim := range []string{"No test covers the new branch.", "None of the tests exercise the retry.", "No passing check exists for it.", "There is no passing check for it.", "No CI run covers it."} {
		findings := proseFindingsOf(t, func(summary, section map[string]any) {
			section["evidence"] = []any{proseEvidence("ev_gap", "test", claim, "asserted")}
		})
		requireFinding(t, strings.Split(findings, "\n"), `section 1 (sec_parser) evidence ev_gap claim: `)
		requireFinding(t, strings.Split(findings, "\n"), `use status "missing" with an "expects"`)
	}
	for _, status := range []string{"missing", "unavailable", "failed", "skipped", "pending"} {
		findings := proseFindingsOf(t, func(summary, section map[string]any) {
			section["evidence"] = []any{proseEvidence("ev_gap", "test", "No test covers the new branch.", status)}
		})
		if strings.Contains(findings, "asserts an absence") {
			t.Errorf("status %s flagged: %s", status, findings)
		}
	}
	// Code facts and claims that only start like an absence are not absence of evidence.
	for _, claim := range []string{"No caller of RESTReader.Tail remains.", "Normal inputs are covered by the parser tests.", "The result is not shown in the checks."} {
		checkEqual(t, "", proseFindingsOf(t, func(summary, section map[string]any) {
			section["evidence"] = []any{proseEvidence("ev_ok", "review", claim, "asserted")}
		}))
	}
}

func TestSectionItemLimits(t *testing.T) {
	findings := proseFindingsOf(t, func(summary, section map[string]any) {
		section["evidence"] = []any{proseEvidence("ev_a", "test", "Lacks a test of the first branch.", "missing"), proseEvidence("ev_b", "test", "Lacks a test of the second branch.", "missing")}
	})
	requireFinding(t, strings.Split(findings, "\n"), `section 1 (sec_parser) has 2 evidence items with status "missing"; keep at most 1`)
	// Risks are not capped: dropping a real risk would record it as mitigated.
	checkEqual(t, "", proseFindingsOf(t, func(summary, section map[string]any) {
		section["evidence"] = []any{proseEvidence("ev_a", "test", "Lacks a test of the first branch.", "missing")}
		var risks []any
		for i, text := range []string{"Alpha causes trouble.", "Bravo breaks callers.", "Charlie leaks memory.", "Delta slows startup.", "Echo drops writes."} {
			risks = append(risks, proseRisk(fmt.Sprintf("rsk_%d", i), text))
		}
		section["risks"] = risks
	}))
}

func TestNearDuplicateRiskAndAttention(t *testing.T) {
	const text = "The retry loop never backs off, so a failing upstream is called in a tight loop"
	findings := proseFindingsOf(t, func(summary, section map[string]any) {
		section["risks"] = []any{proseRisk("rsk_a", text+"."), proseRisk("rsk_b", "Retry loop never backs off so failing upstream called in tight loop!")}
	})
	requireFinding(t, strings.Split(findings, "\n"), "section 1 (sec_parser) risk rsk_b states the same point as section 1 (sec_parser) risk rsk_a")
	findings = proseFindingsOf(t, func(summary, section map[string]any) {
		section["risks"] = []any{proseRisk("rsk_a", text+" and the delay is never capped anywhere in the client.")}
		section["attention"] = map[string]any{"text": "Retry loop never backs off, so a failing upstream is called in a tight loop", "refs": []any{"rsk_a"}}
	})
	requireFinding(t, strings.Split(findings, "\n"), "section 1 (sec_parser) risk rsk_a states the same point as section 1 (sec_parser) attention")
	// Different points, a shared topic, and texts too short to compare.
	checkEqual(t, "", proseFindingsOf(t, func(summary, section map[string]any) {
		section["risks"] = []any{proseRisk("rsk_a", text+"."), proseRisk("rsk_b", "The retry count is read from an environment variable that is not validated at startup.")}
		section["attention"] = map[string]any{"text": "Check the retry loop.", "refs": []any{"rsk_a"}}
	}))
	checkEqual(t, 0.0, textContainment("Breaks callers.", "Breaks callers."))
	if got := textContainment("one two three four five six", "six five four three two one seven"); got != 1 {
		t.Errorf("containment = %v, want 1", got)
	}
}

func TestRecommendationRefs(t *testing.T) {
	recommend := func(decision string, refs ...string) string {
		return proseFindingsOf(t, func(summary, section map[string]any) {
			section["risks"] = []any{proseRisk("rsk_real", "A real risk about the parser.")}
			section["evidence"] = []any{proseEvidence("ev_real", "review", "Reviewed by a person.", "asserted")}
			section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{map[string]any{"id": "dec_choice", "title": "Choice", "by": "agent", "text": "Chose a slice.", "rationale": "Simple.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}}}
			list := make([]any, 0, len(refs))
			for _, ref := range refs {
				list = append(list, ref)
			}
			summary["recommendation"] = map[string]any{"decision": decision, "text": "Look at the parser first.", "refs": list}
		})
	}
	checkEqual(t, "", recommend("look_first", "sec_parser", "rsk_real", "ev_real", "dec_choice"))
	checkEqual(t, "", recommend("approve"))
	if findings := recommend("look_first"); !strings.Contains(findings, `summary recommendation: decision "look_first" needs refs`) {
		t.Errorf("findings %q", findings)
	}
	if findings := recommend("request_changes"); !strings.Contains(findings, `decision "request_changes" needs refs`) {
		t.Errorf("findings %q", findings)
	}
	if findings := recommend("look_first", "sec_parser", "rsk_gone"); !strings.Contains(findings, `summary recommendation ref "rsk_gone": does not resolve`) {
		t.Errorf("findings %q", findings)
	}
}

func TestProseRulesApplyToTheRecommendationText(t *testing.T) {
	findings := proseFindingsOf(t, func(summary, section map[string]any) {
		summary["recommendation"] = map[string]any{"decision": "approve", "text": "The check was still running when the bundle was taken.", "refs": []any{}}
	})
	requireFinding(t, strings.Split(findings, "\n"), "summary recommendation text: ")
}

func TestRecommendationIsRequiredForAuthoredDocumentsOnly(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	// The model is asked for the key, null or not; the stored contract does not.
	for name, build := range map[string]func() ([]byte, error){
		"model":   func() ([]byte, error) { return v.ModelSchema(nil) },
		"outline": v.OutlineSchema,
	} {
		schema, err := build()
		checkNoError(t, err)
		summary := properties(schemaTree(t, schema))["summary"].(map[string]any)
		if !containsString(stringSlice(summary["required"]), "recommendation") {
			t.Errorf("the %s schema does not require summary.recommendation", name)
		}
	}
	contract := v.Contract()
	summary := properties(contract)["summary"].(map[string]any)
	if !containsString(stringSlice(summary["required"]), "recommendation") {
		t.Error("the authored contract does not require summary.recommendation")
	}
	// A stored document from before the field has none and stays valid.
	stored := storedExample(t)
	delete(stored["summary"].(map[string]any), "recommendation")
	if findings := validateStored(t, stored, nil); findings != nil {
		t.Errorf("stored document without a recommendation rejected: %q", findings)
	}
}

func TestStoredDocumentsKeepTheirOldWording(t *testing.T) {
	stored := storedExample(t)
	section := firstSection(stored)
	section["title"] = "ENG" + "-1893: Parser"
	section["summary"].(map[string]any)["text"] = "The check was still in progress when the bundle was taken; I read " + testHunk + " and rsk_gone, comment 999999999."
	section["evidence"] = []any{
		proseEvidence("ev_ci_old", "ci", "No test covers the branch.", "asserted"),
		proseEvidence("ev_gap1", "test", "Lacks one.", "missing"),
		proseEvidence("ev_gap2", "test", "Lacks two.", "missing"),
	}
	stored["summary"].(map[string]any)["recommendation"] = map[string]any{"decision": "look_first", "text": "Look.", "refs": []any{"rsk_gone"}}
	findings := validateStored(t, stored, nil)
	for _, finding := range findings {
		for _, rule := range []string{"ticket key", "captured", "is not a known", "kind \"ci\"", "asserts an absence", "evidence items with status", "near-duplicate", "recommendation"} {
			if strings.Contains(finding, rule) {
				t.Errorf("stored document rejected by a prose rule: %s", finding)
			}
		}
	}
}

func TestRecommendationSurvivesCompletionRefreshAndStaging(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	recommendation := map[string]any{"decision": "look_first", "text": "Start with the parser.", "refs": []any{"sec_parser"}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	check := func(label string, document []byte) {
		t.Helper()
		got := mustDecode(t, document)["summary"].(map[string]any)["recommendation"]
		checkEqual(t, any(recommendation), got)
	}

	doc := mustDecode(t, authoredFixture())
	doc["summary"].(map[string]any)["recommendation"] = recommendation
	completed, findings := v.Complete(tileJSON(t, doc), proseBundle(), "", "model", now)
	checkEqual(t, "", findings)
	check("completed", completed)
	refreshed, err := v.Refresh(completed, proseBundle(), now.Add(time.Hour))
	checkNoError(t, err)
	check("refreshed", refreshed)
	if findings := v.ValidateStored(refreshed, proseBundle()); findings != nil {
		t.Fatalf("refreshed document invalid: %q", findings)
	}

	// An explicit null stays null, and a stored document without the key
	// stays without it through a refresh.
	doc["summary"].(map[string]any)["recommendation"] = nil
	completed, findings = v.Complete(tileJSON(t, doc), proseBundle(), "", "model", now)
	checkEqual(t, "", findings)
	if value, present := mustDecode(t, completed)["summary"].(map[string]any)["recommendation"]; !present || value != nil {
		t.Errorf("recommendation = %v (present %v), want null", value, present)
	}
	old := mustDecode(t, completed)
	delete(old["summary"].(map[string]any), "recommendation")
	refreshed, err = v.Refresh(tileJSON(t, old), proseBundle(), now.Add(time.Hour))
	checkNoError(t, err)
	if _, present := mustDecode(t, refreshed)["summary"].(map[string]any)["recommendation"]; present {
		t.Error("refresh added a recommendation to a document that had none")
	}

	// The outline stage writes it; it survives into the assembled and the
	// completed staged document.
	b := stagedBundle()
	outlineReply := stagedOutlineReply(t, func(outline map[string]any) {
		outline["summary"].(map[string]any)["recommendation"] = map[string]any{"decision": "look_first", "text": "Start with the parser.", "refs": []any{"sec_parser"}}
	})
	outline, findingsText := v.PrepareOutline(outlineReply, b, t.TempDir())
	checkEqual(t, "", findingsText)
	check("outline", outline)
	check("outline of", OutlineOf(outline))
	assembled, err := AssembleStaged(StagedParts{Outline: outline}, false)
	checkNoError(t, err)
	check("assembled", assembled)
	schema, err := v.OutlineSchema()
	checkNoError(t, err)
	if !strings.Contains(string(schema), `"recommendation"`) {
		t.Error("the outline schema does not offer the recommendation")
	}
}

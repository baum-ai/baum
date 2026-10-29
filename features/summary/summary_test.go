package summary

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

type decodedFeature struct {
	PullRequests []struct {
		Number      int32  `json:"number"`
		Description string `json:"description"`
		Summary     string `json:"summary"`
		Review      *struct {
			Findings  []string `json:"findings"`
			Decisions []struct {
				Text string `json:"text"`
			} `json:"decisions"`
		} `json:"review"`
	} `json:"pull_requests"`
	Decisions []struct {
		ID         string `json:"id"`
		Summary    string `json:"summary"`
		Rationale  string `json:"rationale"`
		Trigger    string `json:"trigger"`
		Superseded bool   `json:"superseded"`
	} `json:"decisions"`
	Paths []string `json:"paths"`
}

func decode(t *testing.T, input string) decodedFeature {
	t.Helper()
	var decoded decodedFeature
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// sizable is a feature whose every part is large, so cutting has work at
// every step.
func sizable() FeatureInput {
	text := func(label string) string { return label + " " + strings.Repeat("x", 500) }
	var input FeatureInput
	for i := range 3 {
		input.PullRequests = append(input.PullRequests, PullRequestInfo{
			Number: int32(i + 1), State: "open", Title: fmt.Sprintf("PR %d", i+1), Body: text("body"), Summary: "summary",
			Review: &Review{
				Findings:  []string{text(fmt.Sprintf("finding %d.1", i+1)), text(fmt.Sprintf("finding %d.2", i+1))},
				Decisions: []ReviewDecision{{By: "human", Text: text(fmt.Sprintf("review decision %d", i+1))}},
			},
		})
	}
	for i := range 6 {
		input.Decisions = append(input.Decisions, Decision{
			Summary: text(fmt.Sprintf("decision %d", i+1)), Category: "architecture", Who: "human", Trigger: "user_request",
			Rationale: "because", Superseded: i%2 == 0, Files: []string{"a.go"},
		})
	}
	input.Paths = []string{"a.go", "b.go"}
	return input
}

func TestRenderFeatureHoldsEverythingWithinItsBound(t *testing.T) {
	input := sizable()
	rendered, err := RenderFeature(input, DefaultMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Cut.Total() != 0 {
		t.Fatalf("cut = %+v with room to spare", rendered.Cut)
	}
	decoded := decode(t, rendered.Input)
	if len(decoded.PullRequests) != 3 || len(decoded.Decisions) != 6 || len(decoded.Paths) != 2 ||
		decoded.PullRequests[0].Review == nil || len(decoded.PullRequests[0].Review.Findings) != 2 {
		t.Fatalf("rendered = %s", rendered.Input)
	}
	if decoded.Decisions[0].ID != "D1" || !decoded.Decisions[0].Superseded || decoded.Decisions[1].Trigger != "user_request" ||
		decoded.Decisions[1].Rationale != "because" {
		t.Fatalf("decisions = %+v", decoded.Decisions)
	}
	// Only current decisions can be key decisions.
	if len(rendered.Current) != 3 || rendered.Current["D2"] != 1 || len(rendered.Rendered) != 6 {
		t.Fatalf("current = %v, rendered = %v", rendered.Current, rendered.Rendered)
	}
	if len(rendered.ReviewDecisions) != 3 || !slices.Equal(rendered.ReviewDecisions[2], []int{0}) {
		t.Fatalf("rendered review decisions = %v", rendered.ReviewDecisions)
	}
}

func TestRenderFeatureCutsSupersededDecisionsThenOlderFindingsFirst(t *testing.T) {
	input := sizable()
	full, err := RenderFeature(input, MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	// Each step in cut order, with the bound just below what is left after
	// the previous steps.
	type step struct {
		name  string
		cut   func(*FeatureInput)
		check func(Cut) bool
	}
	withoutSuperseded := func(f *FeatureInput) {
		f.Decisions = slices.DeleteFunc(f.Decisions, func(d Decision) bool { return d.Superseded })
	}
	steps := []step{
		{"superseded decisions", func(*FeatureInput) {}, func(c Cut) bool {
			return c.SupersededDecisions > 0 && c.ReviewFindings == 0 && c.Decisions == 0
		}},
		{"older review findings", withoutSuperseded, func(c Cut) bool {
			return c.SupersededDecisions == 3 && c.ReviewFindings > 0 && c.ReviewDecisions == 0 && c.Decisions == 0
		}},
	}
	for _, s := range steps {
		trimmed := input
		trimmed.Decisions = slices.Clone(input.Decisions)
		s.cut(&trimmed)
		reference, err := RenderFeature(trimmed, MaxInputBytes)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := RenderFeature(input, len(reference.Input)-1)
		if err != nil {
			t.Fatal(err)
		}
		if len(rendered.Input) > len(reference.Input)-1 || !s.check(rendered.Cut) {
			t.Fatalf("%s: cut = %+v, %d bytes of %d", s.name, rendered.Cut, len(rendered.Input), len(full.Input))
		}
	}

	// Findings go from the earliest pull request first.
	rendered, err := RenderFeature(input, len(full.Input)-3*560-600)
	if err != nil {
		t.Fatal(err)
	}
	decoded := decode(t, rendered.Input)
	if rendered.Cut.SupersededDecisions != 3 || rendered.Cut.ReviewFindings != 1 ||
		len(decoded.PullRequests[0].Review.Findings) != 1 || !strings.HasPrefix(decoded.PullRequests[0].Review.Findings[0], "finding 1.2") ||
		len(decoded.PullRequests[2].Review.Findings) != 2 {
		t.Fatalf("cut = %+v, pull requests = %+v", rendered.Cut, decoded.PullRequests)
	}
	for _, decision := range decoded.Decisions {
		if decision.Superseded {
			t.Fatalf("a superseded decision was kept while findings were cut: %+v", decision)
		}
	}

	// Cut hard, everything but the framing goes, in order, and the input
	// still fits.
	rendered, err = RenderFeature(input, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Input) > 120 || rendered.Cut.Decisions != 3 || rendered.Cut.PullRequests == 0 || len(rendered.Current) != 0 ||
		len(rendered.Rendered) != 0 || slices.ContainsFunc(rendered.ReviewDecisions, func(kept []int) bool { return len(kept) > 0 }) {
		t.Fatalf("cut = %+v, input = %s, review decisions %v", rendered.Cut, rendered.Input, rendered.ReviewDecisions)
	}
	if _, err := RenderFeature(input, 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an impossible bound: %v", err)
	}
}

func TestRenderPullRequestIsBounded(t *testing.T) {
	huge := strings.Repeat("<long & \"text\"> ", 10000)
	input := PullRequestInput{Title: huge, Body: huge}
	for range 500 {
		input.Decisions = append(input.Decisions, Decision{Summary: huge, Category: huge, Who: huge, Trigger: huge,
			Rationale: huge, RejectedAlternative: huge, Files: slices.Repeat([]string{huge}, 50)})
	}
	input.Decisions = append(input.Decisions, Decision{Summary: "superseded", Superseded: true})
	rendered, indexes, err := RenderPullRequest(input)
	if err != nil || len(rendered) > MaxPullRequestInputBytes() {
		t.Fatalf("%d bytes over %d, %v", len(rendered), MaxPullRequestInputBytes(), err)
	}
	// Only the decisions rendered are reported, the first within the bound.
	if len(indexes) != maxPullRequestDecision || indexes[0] != 0 || indexes[len(indexes)-1] != maxPullRequestDecision-1 {
		t.Fatalf("rendered decisions = %v", indexes)
	}
	small, smallIndexes, _ := RenderPullRequest(PullRequestInput{Title: "t", Decisions: []Decision{{Summary: "old", Superseded: true}, {Summary: "new"}}})
	if strings.Contains(small, "old") || !slices.Equal(smallIndexes, []int{1}) {
		t.Fatalf("a superseded decision was rendered: %s, %v", small, smallIndexes)
	}
	hash := ContentHash(PublicVersion, small)
	if hash != ContentHash(PublicVersion, strings.Clone(small)) || hash == ContentHash("other", small) {
		t.Fatal("the content hash is not a function of version and input")
	}
}

func TestParseFeatureReplyKeepsOnlyCurrentRenderedKeyDecisions(t *testing.T) {
	rendered, err := RenderFeature(sizable(), DefaultMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	reply := `{"title":"  CSV   export ","description":"Exports reports.","digest":"Built it.","key_decisions":["D2","D1","D9","D2","D4","D6"]}`
	text, err := ParseFeatureReply([]byte(reply), rendered)
	if err != nil {
		t.Fatal(err)
	}
	// D1 is superseded, D9 unknown, the second D2 a repeat.
	if text.Title != "CSV export" || !slices.Equal(text.KeyDecisions, []int{1, 3, 5}) || text.DroppedKeyDecisions != 3 {
		t.Fatalf("text = %+v", text)
	}
	none, err := ParseFeatureReply([]byte(`{"title":"T","description":"D","digest":"","key_decisions":["D1","x"]}`), rendered)
	if err != nil || len(none.KeyDecisions) != 0 {
		t.Fatalf("no valid key decision: %+v, %v", none, err)
	}
	for _, bad := range []string{
		`{"title":"","description":"D","digest":"","key_decisions":[]}`,
		`{"title":"T","description":"D","digest":""}`,
		`{"title":"T","description":"D","digest":"","key_decisions":[],"extra":1}`,
		`{"title":"` + strings.Repeat("t", MaxTitleRunes+1) + `","description":"D","digest":"","key_decisions":[]}`,
		`not json`,
	} {
		if _, err := ParseFeatureReply([]byte(bad), rendered); !errors.Is(err, ErrInvalidReply) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

func TestParsePullRequestReply(t *testing.T) {
	if summary, err := ParsePullRequestReply([]byte(`{"summary":" Adds export. "}`)); err != nil || summary != "Adds export." {
		t.Fatalf("%q, %v", summary, err)
	}
	for _, bad := range []string{`{"summary":""}`, `{}`, `{"summary":"x","y":1}`} {
		if _, err := ParsePullRequestReply([]byte(bad)); !errors.Is(err, ErrInvalidReply) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

// testPrompts are the published versions, a version that owns its pull
// request prompt and one that reuses it and reads the current text.
var testPrompts = Prompts{
	PublicVersion: Public[PublicVersion],
	"owner":       {Feature: "Summarize the feature.", PullRequest: "Summarize the pull request."},
	"reuser": {
		Feature: "Summarize the feature and keep its \"current\" title.", PullRequest: "Summarize the pull request.",
		PullRequestVersion: "owner", CurrentText: true,
	},
}

func TestRequestNamesItsVersionAndKind(t *testing.T) {
	for _, version := range testPrompts.PromptVersions() {
		for _, kind := range []Kind{Feature, PullRequest} {
			req, err := testPrompts.Request(version, kind, "{}", 0)
			if err != nil {
				t.Fatal(err)
			}
			// A reused pull request prompt is named by its owner.
			want := version
			if kind == PullRequest {
				want = testPrompts.PullRequestVersion(version)
			}
			if gotVersion, gotKind, ok := testPrompts.VersionOf(req); !ok || gotVersion != want || gotKind != kind || req.MaxOutputTokens != MaxOutputTokens {
				t.Fatalf("%s %s: %q %q %v", version, kind, gotVersion, gotKind, ok)
			}
			if _, err := json.Marshal(req.Schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := testPrompts.Request("feature-summary-v0", Feature, "{}", 0); err == nil {
		t.Fatal("an unknown version was accepted")
	}
	if _, err := testPrompts.Request(PublicVersion, "other", "{}", 0); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if !Public.KnownPromptVersion(PublicVersion) || !slices.Equal(Public.PromptVersions(), []string{PublicVersion}) {
		t.Fatal("the published prompt version is not released")
	}
}

func TestAVersionReadsTheCurrentTextAndReusesAPullRequestPrompt(t *testing.T) {
	if !testPrompts.UsesCurrentText("reuser") || testPrompts.UsesCurrentText("owner") {
		t.Fatal("only the reusing version reads the current text")
	}
	if testPrompts.PullRequestVersion("reuser") != "owner" || testPrompts.PullRequestVersion("owner") != "owner" {
		t.Fatal("the reusing version does not reuse its owner's pull request prompt")
	}
	// The published version reads the current text and owns its pull
	// request prompt.
	if !Public.UsesCurrentText(PublicVersion) || Public.PullRequestVersion(PublicVersion) != PublicVersion {
		t.Fatal("the published version does not read the current text")
	}
	feature, _ := Public.Request(PublicVersion, Feature, "{}", 0)
	if !strings.Contains(feature.System, `"current"`) {
		t.Fatal("the published feature prompt does not describe the current text")
	}
}

func TestWithCurrentAppendsTheBoundedCurrentText(t *testing.T) {
	rendered, err := RenderFeature(FeatureInput{PullRequests: []PullRequestInfo{{Number: 1, State: "open", Title: "Add export"}}}, DefaultMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := WithCurrent(rendered.Input, "CSV export", strings.Repeat("d", MaxDescriptionRunes+50))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, strings.TrimSuffix(rendered.Input, "}")+`,"current":{"title":"CSV export","description":"d`) {
		t.Fatalf("input = %s", got)
	}
	var decoded struct {
		PullRequests []json.RawMessage `json:"pull_requests"`
		Current      struct{ Title, Description string }
	}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil || len(decoded.PullRequests) != 1 ||
		len([]rune(decoded.Current.Description)) != MaxDescriptionRunes {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	if len(got)-len(rendered.Input) > MaxCurrentTextBytes {
		t.Fatalf("the current text took %d bytes, over %d", len(got)-len(rendered.Input), MaxCurrentTextBytes)
	}
	if empty, err := WithCurrent("{}", "T", "D"); err != nil || empty != `{"current":{"title":"T","description":"D"}}` {
		t.Fatalf("empty input = %s, %v", empty, err)
	}
	for _, input := range []string{"[]", "not json", got} {
		if _, err := WithCurrent(input, "T", "D"); err == nil {
			t.Fatalf("WithCurrent accepted %q", input)
		}
	}
}

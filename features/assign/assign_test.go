package assign

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// plainVersion neither skips nor lists, as a host's earliest versions do.
const plainVersion = "feature-assign-plain"

// testPrompts are the published versions and plainVersion.
var testPrompts = NewPrompts(
	map[string]Version{
		PublicVersion: {Prompt: publicPrompt, Skips: true, Lists: true},
		plainVersion:  {Prompt: "Assign the item to a feature."},
	},
	map[string]Batch{PublicBatchVersion: {
		Single: PublicVersion, Frame: publicBatchFrame, Seeded: PublicBatchVersion + "-2", SeededNote: publicSeededNote,
	}},
)

func TestRenderIsBoundedByTheCandidates(t *testing.T) {
	huge := strings.Repeat("<long & \"text\"> ", 10000)
	var paths []string
	for i := range 1000 {
		paths = append(paths, fmt.Sprintf("dir/%d/%s.go", i, strings.Repeat("p", 500)))
	}
	item := Item{PullRequest: &PullRequest{Title: huge, Body: huge, Branch: huge, Paths: paths}}
	for _, k := range []int{0, 1, 8, 32} {
		candidates := make([]Candidate, k)
		for i := range candidates {
			pulls := make([]CandidatePullRequest, len(paths))
			for j, path := range paths {
				pulls[j] = CandidatePullRequest{Title: path, Summary: huge}
			}
			candidates[i] = Candidate{ID: fmt.Sprint(i), Title: huge, Description: huge, Paths: paths, Digest: huge, PullRequests: pulls}
		}
		rendered, err := testPrompts.Render(PublicVersion, item, candidates)
		if err != nil {
			t.Fatal(err)
		}
		if len(rendered) > MaxInputBytes(k) {
			t.Fatalf("k=%d: %d bytes, over the bound %d", k, len(rendered), MaxInputBytes(k))
		}
		var decoded struct {
			Candidates []json.RawMessage `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(rendered), &decoded); err != nil || len(decoded.Candidates) != k {
			t.Fatalf("k=%d: %d candidates, %v", k, len(decoded.Candidates), err)
		}
	}
	session := Item{Session: &Session{}}
	for range 500 {
		session.Session.Decisions = append(session.Session.Decisions, Decision{Category: huge, Summary: huge, Files: paths})
	}
	if rendered, err := testPrompts.Render(PublicVersion, session, nil); err != nil || len(rendered) > MaxInputBytes(0) {
		t.Fatalf("session: %d bytes, %v", len(rendered), err)
	}
}

func TestRenderNamesCandidatesByReferenceAndListsSharedPathsFirst(t *testing.T) {
	item := Item{PullRequest: &PullRequest{Title: "Add export", Paths: []string{"export/csv.go"}}}
	rendered, err := testPrompts.Render(PublicVersion, item, []Candidate{
		{ID: "uuid-1", Title: "Parser", Paths: []string{"a.go", "export/csv.go"}, Current: true},
		{ID: "uuid-2", Title: "Export"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "uuid-") || !strings.Contains(rendered, `"id":"F1"`) || !strings.Contains(rendered, `"id":"F2"`) ||
		!strings.Contains(rendered, `"paths":["export/csv.go","a.go"]`) || !strings.Contains(rendered, `"current":true`) {
		t.Fatalf("rendered = %s", rendered)
	}
}

func TestRenderWithholdsACandidatesText(t *testing.T) {
	item := Item{PullRequest: &PullRequest{Title: "Add export"}}
	rendered, err := testPrompts.Render(PublicVersion, item, []Candidate{{ID: "uuid-1", Title: "Secret", Description: "Secret too", Paths: []string{"a.go"}, Withheld: true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "Secret") || !strings.Contains(rendered, `"title":"(summary withheld)","description":""`) ||
		!strings.Contains(rendered, `"paths":["a.go"],"withheld":true`) {
		t.Fatalf("rendered = %s", rendered)
	}
}

func TestParseReplyAcceptsOnlyCandidatesOrANewFeature(t *testing.T) {
	candidates := []Candidate{{ID: "uuid-1"}, {ID: "uuid-2"}}
	reply := func(choice, id, title, description string) []byte {
		encoded, _ := json.Marshal(map[string]string{"reason": " why ", "choice": choice, "feature_id": id, "title": title, "description": description})
		return encoded
	}
	choice, err := testPrompts.ParseReply(plainVersion, reply("existing", "F2", "", ""), candidates)
	if err != nil || choice.FeatureID != "uuid-2" || choice.New != nil || choice.Reason != "why" {
		t.Fatalf("existing = %+v, %v", choice, err)
	}
	choice, err = testPrompts.ParseReply(plainVersion, reply("new", "", "  Export   reports ", " Adds CSV export. "), candidates)
	if err != nil || choice.FeatureID != "" || choice.New == nil || *choice.New != (NewFeature{Title: "Export reports", Description: "Adds CSV export."}) {
		t.Fatalf("new = %+v, %v", choice, err)
	}
	for name, data := range map[string][]byte{
		"outside the candidates":  reply("existing", "F3", "", ""),
		"a real id":               reply("existing", "uuid-1", "", ""),
		"no title":                reply("new", "", "", "d"),
		"no description":          reply("new", "", "t", ""),
		"title too long":          reply("new", "", strings.Repeat("w ", MaxNewTitleRunes), "d"),
		"description too long":    reply("new", "", "t", strings.Repeat("d", MaxNewDescriptionRunes+1)),
		"unknown choice":          reply("maybe", "", "", ""),
		"missing field":           []byte(`{"reason":"","choice":"new","title":"t","description":"d"}`),
		"unknown field":           []byte(`{"reason":"","choice":"new","feature_id":"","title":"t","description":"d","extra":1}`),
		"trailing data":           append(reply("existing", "F1", "", ""), []byte(" {}")...),
		"not json":                []byte(`{"choice":`),
		"existing with no choice": reply("existing", "", "", ""),
	} {
		if _, err := testPrompts.ParseReply(plainVersion, data, candidates); !errors.Is(err, ErrInvalidReply) {
			t.Errorf("%s: err = %v, want ErrInvalidReply", name, err)
		}
	}
}

func TestOnlyASkippingVersionSkips(t *testing.T) {
	skip := []byte(`{"reason":" a typo ","choice":"skip","feature_id":"","title":"","description":""}`)
	choice, err := testPrompts.ParseReply(PublicVersion, skip, nil)
	if err != nil || !choice.Skip || choice.FeatureID != "" || choice.New != nil || choice.Reason != "a typo" {
		t.Fatalf("skipping version: %+v, %v", choice, err)
	}
	if _, err := testPrompts.ParseReply(plainVersion, skip, nil); !errors.Is(err, ErrInvalidReply) {
		t.Fatalf("plain version skip: err = %v, want ErrInvalidReply", err)
	}
	for version, want := range map[string]string{
		plainVersion:  `"enum":["existing","new"]`,
		PublicVersion: `"enum":["existing","new","skip"]`,
	} {
		schema, err := json.Marshal(testPrompts.Schema(version))
		if err != nil || !strings.Contains(string(schema), want) {
			t.Fatalf("%s schema = %s, %v", version, schema, err)
		}
	}
}

func TestRequestsCarryTheirPromptVersion(t *testing.T) {
	for _, version := range testPrompts.PromptVersions() {
		request, err := testPrompts.Request(version, "input", 0)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := testPrompts.VersionOf(request); !ok || got != version || request.MaxOutputTokens != MaxOutputTokens {
			t.Fatalf("VersionOf = %q, %v; max output %d", got, ok, request.MaxOutputTokens)
		}
		request.System += " and more"
		if _, ok := testPrompts.VersionOf(request); ok {
			t.Fatal("an altered prompt is taken for a released version")
		}
	}
	if _, err := testPrompts.Request("feature-assign-v0", "input", 0); err == nil {
		t.Fatal("an unknown prompt version built a request")
	}
	if !Public.KnownPromptVersion(PublicVersion) || !Public.ListsCandidates(PublicVersion) {
		t.Fatal("the published prompt version is not released, or does not list candidates")
	}
	if request, err := Public.Request(PublicVersion, "input", 0); err != nil || request.System != publicPrompt {
		t.Fatalf("the published request = %v", err)
	}
	schema, err := json.Marshal(Public.Schema(PublicVersion))
	if err != nil || !strings.HasPrefix(string(schema), `{"additionalProperties":false,"properties":{"reason"`) {
		t.Fatalf("schema = %s, %v", schema, err)
	}
}

func TestSearchQueryIsOnlyWordsORedTogether(t *testing.T) {
	item := Item{PullRequest: &PullRequest{Title: "Fix: the parser's (CSV) export!", Body: "it's a & b | c", Branch: "paul/eng-12-csv-export"}}
	if got := SearchQuery(item); got != "fix | the | parser | csv | export | paul | eng" {
		t.Fatalf("SearchQuery = %q", got)
	}
	session := Item{Session: &Session{Decisions: []Decision{{Summary: "Use Postgres"}, {Summary: "use postgres too"}}}}
	if got := SearchQuery(session); got != "use | postgres | too" {
		t.Fatalf("SearchQuery = %q", got)
	}
	if got := SearchQuery(Item{PullRequest: &PullRequest{Title: "a b !"}}); got != "" {
		t.Fatalf("SearchQuery = %q, want none", got)
	}
}

func TestOnlyAListingVersionShowsACandidatesPullRequests(t *testing.T) {
	item := Item{PullRequest: &PullRequest{Title: "Add export"}}
	var pulls []CandidatePullRequest
	for i := range 30 {
		pulls = append(pulls, CandidatePullRequest{Title: fmt.Sprintf("PR %d", i), Summary: fmt.Sprintf("Summary %d", i)})
	}
	candidates := []Candidate{
		{ID: "uuid-1", Title: "Export", Digest: "Exports reports.", PullRequests: pulls},
		{ID: "uuid-2", Title: "New"},
		{ID: "uuid-3", Withheld: true, Digest: "Secret digest", PullRequests: []CandidatePullRequest{{Title: "PR x", Summary: "Secret summary"}}},
	}
	rendered, err := testPrompts.Render(PublicVersion, item, candidates)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Candidates []struct {
			Digest       string `json:"digest"`
			Count        *int   `json:"pull_request_count"`
			PullRequests []struct {
				Title, Summary string
			} `json:"pull_requests"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatal(err)
	}
	first, second := decoded.Candidates[0], decoded.Candidates[1]
	if first.Count == nil || *first.Count != 30 || len(first.PullRequests) != maxCandidatePullRequests || first.Digest != "Exports reports." ||
		first.PullRequests[0].Title != "PR 0" || first.PullRequests[0].Summary != "Summary 0" ||
		first.PullRequests[maxCandidatePullRequests-1].Title != "PR 29" {
		t.Fatalf("first candidate = %+v", first)
	}
	if strings.Contains(rendered, "Secret") || decoded.Candidates[2].PullRequests[0].Title != "PR x" {
		t.Fatalf("a withheld candidate's digest or summaries were shown: %s", rendered)
	}
	if second.Count == nil || *second.Count != 0 || len(second.PullRequests) != 0 {
		t.Fatalf("second candidate = %+v", second)
	}
	plain, err := testPrompts.Render(plainVersion, item, candidates)
	if err != nil || strings.Contains(plain, "pull_request_count") || strings.Contains(plain, `"pull_requests"`) {
		t.Fatalf("plain rendered = %s, %v", plain, err)
	}
}

func TestMentionedPullRequestsAreDistinctNumbers(t *testing.T) {
	got := MentionedPullRequests("Fix #12 (follow-up to #9)", "See #12, owner/repo#7 and https://x.test/a#3; also #0 and #41.")
	if !slices.Equal(got, []int32{12, 9, 41}) {
		t.Fatalf("mentioned = %v", got)
	}
}

func TestAListingVersionShowsACandidatesLinks(t *testing.T) {
	item := Item{PullRequest: &PullRequest{Title: "Add export"}}
	candidates := []Candidate{
		{ID: "uuid-1", Links: Links{Stacked: []int32{4}, SharedSessions: []int32{4, 5}}},
		{ID: "uuid-2"},
	}
	rendered, err := testPrompts.Render(PublicVersion, item, candidates)
	if err != nil || !strings.Contains(rendered, `"links":{"stacked":[4],"shared_sessions":[4,5]}`) || strings.Count(rendered, `"links"`) != 1 {
		t.Fatalf("rendered = %s, %v", rendered, err)
	}
	if plain, err := testPrompts.Render(plainVersion, item, candidates); err != nil || strings.Contains(plain, "links") {
		t.Fatalf("plain rendered = %s, %v", plain, err)
	}
}

package consolidate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type decodedMerge struct {
	Features []struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Paths        []string `json:"paths"`
		PullRequests []struct {
			Number int32  `json:"number"`
			Title  string `json:"title"`
		} `json:"pull_requests"`
		Sessions int `json:"sessions"`
	} `json:"features"`
}

func features(n int) []Feature {
	var listed []Feature
	for i := range n {
		listed = append(listed, Feature{
			Title: fmt.Sprintf("Feature %d", i+1), Description: strings.Repeat("d", 900), Paths: []string{"a.go", "b/c.go"},
			PullRequests: []PullRequestTitle{{Number: int32(i + 1), Title: fmt.Sprintf("PR %d", i+1)}}, Sessions: i,
		})
	}
	return listed
}

func TestRenderMergeNamesFeaturesByRefAndCutsTheLeastRecentFirst(t *testing.T) {
	rendered, err := RenderMerge(features(3), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded decodedMerge
	if err := json.Unmarshal([]byte(rendered.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Features) != 3 || decoded.Features[0].ID != "F1" || decoded.Features[2].ID != "F3" ||
		decoded.Features[1].PullRequests[0].Number != 2 || decoded.Features[2].Sessions != 2 || rendered.Cut != 0 ||
		!slices.Equal(rendered.Features, []int{0, 1, 2}) {
		t.Fatalf("rendered = %+v, %s", rendered, rendered.Input)
	}

	// Two features fit: the third, the least recently active, is left out.
	limit := len(rendered.Input) - 500
	rendered, err = RenderMerge(features(3), limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Input) > limit || rendered.Cut != 1 || !slices.Equal(rendered.Features, []int{0, 1}) {
		t.Fatalf("cut rendered = %d bytes, cut %d, features %v", len(rendered.Input), rendered.Cut, rendered.Features)
	}
	if _, err := RenderMerge(features(1), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a feature that does not fit: %v", err)
	}
}

func TestParseMergeReplyPlacesEveryFeatureAtMostOnce(t *testing.T) {
	rendered, err := RenderMerge(features(4), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	groups, dropped, err := ParseMergeReply([]byte(`{"groups":[{"reason":"same","survivor":"F2","duplicates":["F4"," F1"]}]}`), rendered)
	if err != nil || dropped != 0 {
		t.Fatal(dropped, err)
	}
	if len(groups) != 1 || groups[0].Survivor != 1 || !slices.Equal(groups[0].Duplicates, []int{3, 0}) {
		t.Fatalf("groups = %+v", groups)
	}
	if groups, dropped, err := ParseMergeReply([]byte(`{"groups":[]}`), rendered); err != nil || len(groups) != 0 || dropped != 0 {
		t.Fatalf("no duplicates: %+v, %d, %v", groups, dropped, err)
	}
	// What is invalid is dropped, and the rest kept.
	for name, test := range map[string]struct {
		reply   string
		groups  []Group
		dropped int
	}{
		"an id outside the input": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F9"]}]}`, []Group{}, 1},
		"a duplicate outside the input": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F2","F9"]}]}`,
			[]Group{{Survivor: 0, Duplicates: []int{1}}}, 1},
		"a survivor outside the input": {`{"groups":[{"reason":"r","survivor":"F9","duplicates":["F1"]},{"reason":"r","survivor":"F3","duplicates":["F4"]}]}`,
			[]Group{{Survivor: 2, Duplicates: []int{3}}}, 1},
		"a feature in two groups": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F2"]},{"reason":"r","survivor":"F3","duplicates":["F2"]}]}`,
			[]Group{}, 2},
		"a survivor as a duplicate": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F1"]},{"reason":"r","survivor":"F3","duplicates":["F4"]}]}`,
			[]Group{{Survivor: 2, Duplicates: []int{3}}}, 1},
		"a group of one": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":[]}]}`, []Group{}, 1},
		"a survivor that is another group's duplicate": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F2"]},{"reason":"r","survivor":"F2","duplicates":["F3"]}]}`,
			[]Group{}, 2},
		"a duplicate named twice in a group": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F2","F2"]},{"reason":"r","survivor":"F3","duplicates":["F4"]}]}`,
			[]Group{{Survivor: 2, Duplicates: []int{3}}}, 1},
		"a feature also named by a dropped group": {`{"groups":[{"reason":"r","survivor":"F9","duplicates":["F2"]},{"reason":"r","survivor":"F1","duplicates":["F2"]}]}`,
			[]Group{}, 2},
		"a feature also named by a group of one": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":[]},{"reason":"r","survivor":"F2","duplicates":["F1"]}]}`,
			[]Group{}, 2},
		"an empty duplicate": {`{"groups":[{"reason":"r","survivor":"F1","duplicates":["F2",""]}]}`,
			[]Group{{Survivor: 0, Duplicates: []int{1}}}, 1},
	} {
		groups, dropped, err := ParseMergeReply([]byte(test.reply), rendered)
		if err != nil || dropped != test.dropped || !slices.EqualFunc(groups, test.groups, func(a, b Group) bool {
			return a.Survivor == b.Survivor && slices.Equal(a.Duplicates, b.Duplicates)
		}) {
			t.Errorf("%s: groups %+v, dropped %d, err %v; want %+v, %d", name, groups, dropped, err, test.groups, test.dropped)
		}
	}
	for name, reply := range map[string]string{
		"a missing field":  `{"groups":[{"survivor":"F1","duplicates":["F2"]}]}`,
		"an unknown field": `{"groups":[],"extra":1}`,
		"no groups":        `{}`,
		"trailing data":    `{"groups":[]} {}`,
	} {
		if _, _, err := ParseMergeReply([]byte(reply), rendered); !errors.Is(err, ErrInvalidReply) {
			t.Errorf("%s: err = %v, want ErrInvalidReply", name, err)
		}
	}
}

type decodedSplit struct {
	Feature struct {
		Title string `json:"title"`
	} `json:"feature"`
	PullRequests []struct {
		ID          string   `json:"id"`
		Number      int32    `json:"number"`
		Description string   `json:"description"`
		Opened      string   `json:"opened"`
		Paths       []string `json:"paths"`
	} `json:"pull_requests"`
}

func splitInput(n int) SplitInput {
	input := SplitInput{Title: "Big feature", Description: "Does many things."}
	for i := range n {
		input.PullRequests = append(input.PullRequests, PullRequest{
			Number: int32(i + 1), Title: fmt.Sprintf("PR %d", i+1), Body: strings.Repeat("b", 900), State: "merged",
			OpenedAt: time.Date(2026, 3, 1+i, 12, 0, 0, 0, time.UTC), Paths: []string{strings.Repeat("p", 200)},
		})
	}
	return input
}

func TestRenderSplitKeepsEveryPullRequestAndCutsDescriptionsFirst(t *testing.T) {
	rendered, err := RenderSplit(splitInput(4), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded decodedSplit
	if err := json.Unmarshal([]byte(rendered.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	if rendered.PullRequests != 4 || len(decoded.PullRequests) != 4 || decoded.PullRequests[3].ID != "P4" ||
		decoded.PullRequests[1].Opened != "2026-03-02" || decoded.Feature.Title != "Big feature" || rendered.Cut.Total() != 0 {
		t.Fatalf("rendered = %+v", rendered)
	}

	limit := len(rendered.Input) - 1500
	rendered, err = RenderSplit(splitInput(4), limit)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(rendered.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(rendered.Input) > limit || rendered.PullRequests != 4 || rendered.Cut.Descriptions != 2 || rendered.Cut.Paths != 0 ||
		decoded.PullRequests[0].Description != "" || decoded.PullRequests[3].Description == "" {
		t.Fatalf("cut rendered = %d bytes, %+v", len(rendered.Input), rendered.Cut)
	}

	if _, err := RenderSplit(splitInput(4), 300); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a split that cannot fit: %v", err)
	}
	if _, err := RenderSplit(splitInput(MaxSplitPullRequests+1), 1<<20); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a split over MaxSplitPullRequests: %v", err)
	}
}

func TestParseSplitReplyPlacesEveryPullRequestExactlyOnce(t *testing.T) {
	rendered, err := RenderSplit(splitInput(5), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := ParseSplitReply([]byte(`{"parts":[
		{"title":"Export","description":"Exports.","pull_requests":["P5","P2"]},
		{"title":"  Import   flow ","description":"Imports.","pull_requests":["P1","P3","P4"]}]}`), rendered)
	if err != nil {
		t.Fatal(err)
	}
	if len(partition.Parts) != 2 || partition.Keeper != 1 || !slices.Equal(partition.Parts[0].PullRequests, []int{1, 4}) ||
		partition.Parts[1].Title != "Import flow" || !slices.Equal(partition.Parts[1].PullRequests, []int{0, 2, 3}) {
		t.Fatalf("partition = %+v", partition)
	}

	// Parts of one size: the one with the earliest pull request keeps the id.
	rendered4, _ := RenderSplit(splitInput(4), 1<<20)
	partition, err = ParseSplitReply([]byte(`{"parts":[
		{"title":"B","description":"b.","pull_requests":["P2","P4"]},
		{"title":"A","description":"a.","pull_requests":["P3","P1"]}]}`), rendered4)
	if err != nil || partition.Keeper != 1 {
		t.Fatalf("tied partition = %+v, %v", partition, err)
	}

	whole, err := ParseSplitReply([]byte(`{"parts":[{"title":"All","description":"All of it.","pull_requests":["P1","P2","P3","P4"]}]}`), rendered4)
	if err != nil || len(whole.Parts) != 1 || whole.Keeper != 0 {
		t.Fatalf("one part = %+v, %v", whole, err)
	}

	for name, reply := range map[string]string{
		"a pull request in no part":   `{"parts":[{"title":"A","description":"a.","pull_requests":["P1","P2","P3"]}]}`,
		"a pull request in two parts": `{"parts":[{"title":"A","description":"a.","pull_requests":["P1","P2"]},{"title":"B","description":"b.","pull_requests":["P2","P3","P4"]}]}`,
		"an id outside the input":     `{"parts":[{"title":"A","description":"a.","pull_requests":["P1","P2","P3","P4","P5"]}]}`,
		"an empty part":               `{"parts":[{"title":"A","description":"a.","pull_requests":["P1","P2","P3","P4"]},{"title":"B","description":"b.","pull_requests":[]}]}`,
		"no title":                    `{"parts":[{"title":" ","description":"a.","pull_requests":["P1","P2","P3","P4"]}]}`,
		"a title too long":            `{"parts":[{"title":"` + strings.Repeat("t", MaxTitleRunes+1) + `","description":"a.","pull_requests":["P1","P2","P3","P4"]}]}`,
		"no parts":                    `{"parts":[]}`,
		"a missing field":             `{"parts":[{"title":"A","pull_requests":["P1","P2","P3","P4"]}]}`,
	} {
		if _, err := ParseSplitReply([]byte(reply), rendered4); !errors.Is(err, ErrInvalidReply) {
			t.Errorf("%s: err = %v, want ErrInvalidReply", name, err)
		}
	}
}

func TestRenderWithholdsAFeaturesText(t *testing.T) {
	listed := features(2)
	listed[1].Title, listed[1].Description, listed[1].Withheld = "Secret title", "Secret description", true
	rendered, err := RenderMerge(listed, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.Input, "Secret") || !strings.Contains(rendered.Input, `"title":"(summary withheld)","description":""`) ||
		!strings.Contains(rendered.Input, `"withheld":true`) || strings.Count(rendered.Input, `"withheld"`) != 1 {
		t.Fatalf("merge input = %s", rendered.Input)
	}
	input := splitInput(2)
	input.Title, input.Description, input.Withheld = "Secret title", "Secret description", true
	split, err := RenderSplit(input, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(split.Input, "Secret") ||
		!strings.Contains(split.Input, `"feature":{"title":"(summary withheld)","description":"","withheld":true}`) {
		t.Fatalf("split input = %s", split.Input)
	}
}

// testPrompts are the published versions and one whose merge call sees no
// feature context.
var testPrompts = Prompts{
	PublicVersion: Public[PublicVersion],
	"plain":       {Merge: "Merge the features.", Split: "Split the feature."},
}

func TestRequestRoundTripsItsVersionAndKind(t *testing.T) {
	for _, wantVersion := range testPrompts.PromptVersions() {
		for _, kind := range []Kind{Merge, Split} {
			request, err := testPrompts.Request(wantVersion, kind, "{}", 0)
			if err != nil {
				t.Fatal(err)
			}
			if version, got, ok := testPrompts.VersionOf(request); !ok || version != wantVersion || got != kind || request.MaxOutputTokens != MaxOutputTokens {
				t.Fatalf("%s %s: VersionOf = %q %q %v", wantVersion, kind, version, got, ok)
			}
			schema, err := json.Marshal(request.Schema)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(schema), `"additionalProperties":false`) {
				t.Fatalf("%s %s: schema %s is not strict", wantVersion, kind, schema)
			}
		}
	}
	if _, err := testPrompts.Request("feature-consolidate-v9", Merge, "{}", 0); err == nil {
		t.Fatal("an unknown prompt version built a request")
	}
	if _, err := testPrompts.Request(PublicVersion, "other", "{}", 0); err == nil {
		t.Fatal("an unknown kind built a request")
	}
	if !Public.KnownPromptVersion(PublicVersion) || !slices.Equal(Public.PromptVersions(), []string{PublicVersion}) {
		t.Fatalf("prompt versions = %v", Public.PromptVersions())
	}
}

func TestRenderMergeShowsContextOnlyWhenSetAndCutsItFirst(t *testing.T) {
	plain, err := RenderMerge(features(3), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Input, `"digest"`) || strings.Contains(plain.Input, `"summary"`) {
		t.Fatalf("context without a contextual version: %s", plain.Input)
	}
	withContext := func(withheld bool) []Feature {
		listed := features(3)
		for i := range listed {
			listed[i].Digest = strings.Repeat("g", 2000)
			listed[i].PullRequests = append(listed[i].PullRequests, PullRequestTitle{Number: int32(100 + i), Title: "Later"})
			for k := range listed[i].PullRequests {
				listed[i].PullRequests[k].Summary = strings.Repeat("s", 1000)
			}
		}
		listed[2].Withheld = withheld
		return listed
	}
	full, err := RenderMerge(withContext(true), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Features []struct {
			Digest       string `json:"digest"`
			PullRequests []struct {
				Summary string `json:"summary"`
			} `json:"pull_requests"`
		} `json:"features"`
	}
	if err := json.Unmarshal([]byte(full.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	first := decoded.Features[0]
	if len([]rune(first.Digest)) != maxDigestRunes || len([]rune(first.PullRequests[0].Summary)) != maxPullSummaryRunes ||
		decoded.Features[2].Digest != "" || decoded.Features[2].PullRequests[0].Summary != "" {
		t.Fatalf("digest %d runes, summary %d runes, withheld digest %q",
			len([]rune(first.Digest)), len([]rune(first.PullRequests[0].Summary)), decoded.Features[2].Digest)
	}
	if !testPrompts.UsesFeatureContext(PublicVersion) || testPrompts.UsesFeatureContext("plain") {
		t.Fatal("only the published version reads feature context")
	}

	// Summaries go first, each feature's earliest first, then digests;
	// every feature still fits.
	whole, err := RenderMerge(withContext(false), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	limit := len(whole.Input) - 700
	cut, err := RenderMerge(withContext(false), limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Input) > limit || cut.Cut != 0 || cut.CutSummaries != 3 || cut.CutDigests != 0 {
		t.Fatalf("cut = %d bytes, %+v", len(cut.Input), cut)
	}
	decoded.Features = nil
	if err := json.Unmarshal([]byte(cut.Input), &decoded); err != nil || decoded.Features[0].PullRequests[0].Summary != "" ||
		decoded.Features[0].PullRequests[1].Summary == "" {
		t.Fatalf("the earliest summaries were not cut first: %s, %v", cut.Input, err)
	}
	noSummaries := withContext(false)
	for i := range noSummaries {
		for k := range noSummaries[i].PullRequests {
			noSummaries[i].PullRequests[k].Summary = ""
		}
	}
	bare, err := RenderMerge(noSummaries, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	limit = len(bare.Input) - 100
	cut, err = RenderMerge(withContext(false), limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Input) > limit || cut.Cut != 0 || cut.CutSummaries != 6 || cut.CutDigests != 1 {
		t.Fatalf("cut = %d bytes, %+v", len(cut.Input), cut)
	}
}

func TestRenderMergeNamesARecentSplitsOriginWhenItIsRendered(t *testing.T) {
	origin, gone := 0, 5
	features := []Feature{
		{Title: "Reviews", PullRequests: []PullRequestTitle{{Number: 1, Title: "Review"}}},
		{Title: "Review workspace", PullRequests: []PullRequestTitle{{Number: 2, Title: "Workspace"}}, SplitFrom: &origin},
		{Title: "Review CLI", PullRequests: []PullRequestTitle{{Number: 3, Title: "CLI"}}, SplitFrom: &gone},
		{Title: "Plain", PullRequests: []PullRequestTitle{{Number: 4, Title: "Plain"}}},
	}
	rendered, err := RenderMerge(features, MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.Input, `"id":"F2","title":"Review workspace"`) || strings.Count(rendered.Input, `"split_from":"F1"`) != 1 ||
		strings.Count(rendered.Input, "split_from") != 1 {
		t.Fatalf("input = %s", rendered.Input)
	}
	// An origin left out of the input is named nowhere.
	two, err := RenderMerge(features[:2], MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	cut, err := RenderMerge(features[:2], len(two.Input)-len(`{"id":"F1","title":"Reviews","description":"","paths":[],"pull_requests":[{"number":1,"title":"Review"}],"sessions":0},`)+5)
	if err == nil && strings.Contains(cut.Input, "split_from") {
		t.Fatalf("a split_from names a feature left out: %s", cut.Input)
	}
	plain := []Feature{{Title: "A"}, {Title: "B"}}
	if rendered, err := RenderMerge(plain, MaxInputBytes); err != nil || strings.Contains(rendered.Input, "split_from") {
		t.Fatalf("plain input = %s, %v", rendered.Input, err)
	}
}

// A feature near the split call's pull request limit with long descriptions:
// under the default bound the descriptions go first, then paths, and every
// pull request keeps its title and summary.
func TestRenderSplitOfALargeFeatureKeepsTitlesAndSummaries(t *testing.T) {
	input := SplitInput{Title: "Pull request reviews", Description: "Reviews."}
	for i := range 199 {
		var paths []string
		for k := range 20 {
			paths = append(paths, fmt.Sprintf("services/internal/area%d/file%d.go", i%7, k))
		}
		input.PullRequests = append(input.PullRequests, PullRequest{
			Number: int32(i + 1), Title: fmt.Sprintf("Change %d to the review flow", i), Body: strings.Repeat("long description ", 80),
			Summary: fmt.Sprintf("Summary of change %d: %s", i, strings.Repeat("what it does ", 20)),
			State:   "merged", OpenedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour), Paths: paths,
		})
	}
	const defaultBound = 200_000
	rendered, err := RenderSplit(input, defaultBound)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Input) > defaultBound || rendered.PullRequests != 199 {
		t.Fatalf("%d bytes, %d pull requests", len(rendered.Input), rendered.PullRequests)
	}
	var decoded struct {
		PullRequests []struct {
			Title, Summary, Description string
			Paths                       []string
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(rendered.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	withSummary, withDescription, withPaths := 0, 0, 0
	for _, pull := range decoded.PullRequests {
		if pull.Title == "" {
			t.Fatal("a pull request lost its title")
		}
		if pull.Summary != "" {
			withSummary++
		}
		if pull.Description != "" {
			withDescription++
		}
		if len(pull.Paths) > 0 {
			withPaths++
		}
	}
	t.Logf("cut %+v; of 199: %d summaries, %d descriptions, %d with paths", rendered.Cut, withSummary, withDescription, withPaths)
	if withSummary != 199 || rendered.Cut.Summaries != 0 || rendered.Cut.Descriptions == 0 {
		t.Fatalf("summaries kept %d, cut %+v", withSummary, rendered.Cut)
	}

	// Without summaries (a version without feature context) most
	// descriptions go, so most pull requests are left with a title and paths
	// alone.
	plain := input
	plain.PullRequests = slices.Clone(input.PullRequests)
	for i := range plain.PullRequests {
		plain.PullRequests[i].Summary = ""
	}
	if before, err := RenderSplit(plain, defaultBound); err != nil || before.Cut.Descriptions < 100 {
		t.Fatalf("without summaries: cut %+v, %v", before.Cut, err)
	} else {
		t.Logf("without summaries: cut %+v", before.Cut)
	}

	// Summaries go last, and a withheld feature shows none.
	tight, err := RenderSplit(input, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if tight.Cut.Descriptions != 199 || tight.Cut.Paths != 199 || tight.Cut.Summaries == 0 {
		t.Fatalf("tight cut = %+v", tight.Cut)
	}
	input.Withheld = true
	if withheld, err := RenderSplit(input, defaultBound); err != nil || strings.Contains(withheld.Input, `"summary"`) {
		t.Fatalf("a withheld feature's summaries were shown: %v", err)
	}
}

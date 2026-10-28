package discover

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/features/assign"
)

var opened = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestParseFeatureTaxonomyRefusesABadTaxonomy(t *testing.T) {
	good := FeatureTaxonomy{SchemaVersion: TaxonomySchemaVersion, RepositoryID: "acme/app", Features: []TaxonomyFeature{
		{Title: "Search", Description: "Find things.", Paths: []string{"api/search/"}},
		{Title: "Billing", Description: "Pay."},
	}}
	encode := func(t FeatureTaxonomy) []byte { data, _ := json.Marshal(t); return data }
	if _, err := ParseFeatureTaxonomy(encode(good), "acme/app"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*FeatureTaxonomy){
		"other repository":  func(t *FeatureTaxonomy) { t.RepositoryID = "acme/other" },
		"schema version":    func(t *FeatureTaxonomy) { t.SchemaVersion = 2 },
		"no features":       func(t *FeatureTaxonomy) { t.Features = nil },
		"too many features": func(t *FeatureTaxonomy) { t.Features = make([]TaxonomyFeature, MaxTaxonomyFeatures+1) },
		"no title":          func(t *FeatureTaxonomy) { t.Features[0].Title = " " },
		"long title":        func(t *FeatureTaxonomy) { t.Features[0].Title = strings.Repeat("x", MaxTaxonomyTitleRunes+1) },
		"long description": func(t *FeatureTaxonomy) {
			t.Features[1].Description = strings.Repeat("é", MaxTaxonomyDescriptionRunes+1)
		},
		"NUL": func(t *FeatureTaxonomy) { t.Features[1].Description = "a\x00b" },
	} {
		bad := good
		bad.Features = append([]TaxonomyFeature(nil), good.Features...)
		change(&bad)
		if _, err := ParseFeatureTaxonomy(encode(bad), "acme/app"); !errors.Is(err, ErrInvalidTaxonomy) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseFeatureTaxonomy([]byte("{"), "acme/app"); !errors.Is(err, ErrInvalidTaxonomy) {
		t.Fatal(err)
	}
}

func TestCheckHoldsAReplyToTheTaxonomysBounds(t *testing.T) {
	if findings := Check([]byte(`{"features":[{"title":"Search","description":"","paths":[],"examples":[]}]}`)); findings != "" {
		t.Fatal(findings)
	}
	if findings := Check([]byte(`{"features":[]}`)); !strings.Contains(findings, "0 features") {
		t.Fatalf("an empty reply gives %q", findings)
	}
}

// The published prompt carries the published assigner prompt's definition
// of a feature, so discovery and assignment group at the same level.
func TestThePublicPromptCarriesThePublicDefinition(t *testing.T) {
	single, ok := assign.Public.Prompt(assign.PublicVersion)
	definition, _, err := assign.CutDefinition(single)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if !strings.Contains(PublicPrompt, definition) || strings.Contains(PublicPrompt, "{{") {
		t.Fatal("the discovery prompt lacks the definition of a feature")
	}
	if _, err := ComposePrompt("{{definition}}", "no definition"); err == nil {
		t.Fatal("a prompt without a definition composed")
	}
}

func TestPathScoresShareEachPathAmongTheSeedsHoldingIt(t *testing.T) {
	seeds := [][]string{{"a/"}, {"a", "b/z.go"}, {"c/"}}
	got := PathScores(seeds, []string{"a/x.go", "a/x.go", "b/z.go", "ab/y.go"})
	if want := []float64{0.5, 1.5, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scores %v, want %v", got, want)
	}
	if !Under("a", "a/") || Under("ab", "a") || !Under("a/b/c", "a/b") {
		t.Fatal("Under")
	}
}

func TestHistoryPathPrefixesAreTheMostTouchedDirectories(t *testing.T) {
	got := HistoryPathPrefixes([]string{
		"platform/internal/decisionlog/a.go", "platform/internal/decisionlog/b.go", "platform/internal/db/c.sql",
		"ui/src/a.tsx", "ui/src/b.tsx", "docs/x.md", "README.md", "go.mod", "justfile",
	})
	want := []string{"platform/internal/decisionlog/", "ui/src/", "README.md", "docs/", "go.mod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefixes %v, want %v", got, want)
	}
}

func digestLines(t *testing.T, digest []byte) (HistoryDigestHeader, []HistoryDigestLine) {
	t.Helper()
	rows := strings.Split(strings.TrimSuffix(string(digest), "\n"), "\n")
	var header HistoryDigestHeader
	if err := json.Unmarshal([]byte(rows[0]), &header); err != nil {
		t.Fatal(err)
	}
	lines := make([]HistoryDigestLine, len(rows)-1)
	for i, row := range rows[1:] {
		if err := json.Unmarshal([]byte(row), &lines[i]); err != nil {
			t.Fatal(err)
		}
	}
	return header, lines
}

func TestTheHistoryDigestListsEachPullRequestOnALine(t *testing.T) {
	digest := HistoryDigest("acme/app", []HistoryPullRequest{
		{Number: 3, Opened: time.Date(2026, 9, 1, 23, 0, 0, 0, time.FixedZone("", -2*3600)), Merged: true,
			Title: "Add <search>", Branch: "search", Paths: []string{"api/search/a.go"}},
		{Number: 7, Opened: opened, Title: "Fix", Branch: "fix"},
	})
	want := `{"repository":"acme/app","pull_requests":2,"merged":1,"open":1,"trimmed":{"branches":0,"paths":0,"pull_requests":0}}
{"n":3,"opened":"2026-09-02","state":"merged","title":"Add <search>","branch":"search","paths":["api/search/a.go"]}
{"n":7,"opened":"2026-09-01","state":"open","title":"Fix","branch":"fix","paths":[]}
`
	if string(digest) != want {
		t.Fatalf("digest\n%s\nwant\n%s", digest, want)
	}
}

func TestTheHistoryDigestTrimsToItsCap(t *testing.T) {
	pulls := make([]HistoryPullRequest, 10)
	for i := range pulls {
		pulls[i] = HistoryPullRequest{Number: int32(i + 1), Opened: opened, Merged: i%2 == 0, Title: "title",
			Branch: "branch", Paths: []string{"a/x", "b/x", "c/x", "d/x", "e/x"}}
	}
	full := HistoryDigest("acme/app", pulls)
	noBranches := HistoryDigestWithin("acme/app", pulls, len(full)-1)
	header, lines := digestLines(t, noBranches)
	if header.Trimmed != (HistoryDigestTrimmed{Branches: 10}) || lines[9].Branch != "" {
		t.Fatalf("one byte over the cap gives %+v", header)
	}
	small := HistoryDigestWithin("acme/app", pulls, 400)
	header, lines = digestLines(t, small)
	if len(small) > 400 || header.Trimmed.PullRequests == 0 || header.PullRequests != len(lines) ||
		lines[len(lines)-1].N != 10 || !bytes.HasSuffix(small, []byte("\n")) {
		t.Fatalf("a 400 byte cap gives %d bytes, %+v", len(small), header)
	}
}

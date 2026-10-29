package features

import (
	"slices"
	"testing"
	"time"
)

func TestAttributorGivesEachDecisionOneFeature(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	links := SessionLinks{PullRequests: []LinkedPullRequest{
		{FeatureID: "export", PullRequestID: "pr-1", Number: 1, OpenedAt: day, Commits: []string{"a", "b"}, Paths: []string{"export/csv.go"}},
		{FeatureID: "export", PullRequestID: "pr-2", Number: 2, OpenedAt: day.Add(time.Hour), Commits: []string{"c"}, Paths: []string{"export/pdf.go"}},
		{FeatureID: "billing", PullRequestID: "pr-3", Number: 3, OpenedAt: day, Commits: []string{"d"}, Paths: []string{"billing/invoice.go"}},
	}}
	if got := links.Features(); !slices.Equal(got, []string{"export", "billing"}) {
		t.Fatalf("Features = %v, want the most linked commits first", got)
	}
	a := links.Attributor()
	for _, tc := range []struct {
		files                []string
		feature, pullRequest string
	}{
		{[]string{"invoice.go"}, "billing", "pr-3"},
		{[]string{"export/pdf.go"}, "export", "pr-2"},
		// No cited file in any pull request: the dominant feature, its
		// earliest pull request.
		{[]string{"README.md"}, "export", "pr-1"},
		// A tie goes to the more dominant feature.
		{[]string{"csv.go", "invoice.go"}, "export", "pr-1"},
	} {
		feature := a.Attribute(tc.files)
		if pull := a.PullRequest(feature, tc.files); feature != tc.feature || pull != tc.pullRequest {
			t.Fatalf("%v: %s %s, want %s %s", tc.files, feature, pull, tc.feature, tc.pullRequest)
		}
	}
	if own := (SessionLinks{OwnFeature: "own"}).Attributor(); own.Attribute([]string{"x.go"}) != "own" || own.PullRequest("own", nil) != "" {
		t.Fatal("a session with no pull request is not attributed to its own feature")
	}
	if (SessionLinks{}).Attributor().Attribute(nil) != "" {
		t.Fatal("a session in no feature attributed a decision")
	}
}

func TestDeriveStatus(t *testing.T) {
	for _, tc := range []struct {
		pulls, open, merged int
		want                Status
	}{
		{0, 0, 0, StatusInProgress}, {2, 1, 1, StatusInProgress}, {2, 0, 1, StatusShipped}, {2, 0, 0, StatusAbandoned},
	} {
		if got := DeriveStatus(tc.pulls, tc.open, tc.merged); got != tc.want {
			t.Fatalf("DeriveStatus(%d, %d, %d) = %s, want %s", tc.pulls, tc.open, tc.merged, got, tc.want)
		}
	}
}

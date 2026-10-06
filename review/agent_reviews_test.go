package review

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func TestRefreshIndexesReviewThreadsAndAgentReviews(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	line, resolved := 11, false
	bundle.Diff[0].StartLine, bundle.Diff[0].LineCount = 10, 3
	bundle.Sources["review_threads"] = true
	bundle.Threads = []bundlepkg.Thread{{ID: "PRRT_1", Path: "main.go", Line: &line, URL: "https://github.com/org/repo/pull/1#discussion_r200"}}
	bundle.Conversation = []bundlepkg.ConversationItem{
		{ID: "100", Kind: "review", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", State: "commented", Commit: "aaaaaaa"},
		{ID: "200", Kind: "review_comment", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", Path: "main.go", Review: "100", Thread: "PRRT_1", ThreadRoot: true, Resolved: &resolved,
			Body: "### Guard the nil parser\n\n**High Severity**\n\nThe parser can be nil."},
		{ID: "201", Kind: "review_comment", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", Path: "main.go", Review: "100", Thread: "PRRT_1", Resolved: &resolved, Body: "A reply is not a finding."},
		{ID: "300", Kind: "review", AuthorLogin: "chatgpt-codex-connector[bot]", Bot: true, BotTool: "Codex", State: "approved", Commit: "0123456789abcdef"},
		{ID: "400", Kind: "review", AuthorLogin: "octocat", State: "changes_requested", Commit: "aaaaaaa"},
	}
	refreshed, err := validator.Refresh(published, bundle, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var doc reviewDocument
	if err := json.Unmarshal(refreshed, &doc); err != nil {
		t.Fatal(err)
	}
	wantThreads := []documentThread{{ID: "PRRT_1", Path: new("main.go"), Line: &line, URL: new("https://github.com/org/repo/pull/1#discussion_r200")}}
	if !reflect.DeepEqual(doc.Index.Threads, wantThreads) {
		t.Fatalf("threads = %+v, want %+v", doc.Index.Threads, wantThreads)
	}
	if len(doc.AgentReviews) != 2 {
		t.Fatalf("agent reviews = %s, want Cursor and Codex only", encodedJSON(t, doc.AgentReviews))
	}
	cursor, codex := doc.AgentReviews[0], doc.AgentReviews[1]
	if cursor.ID != "ar_cursor_aaaaaaa" || cursor.Stale || cursor.Verdict != "comment" || cursor.Counts != (documentAgentReviewCounts{MustFix: 1}) {
		t.Fatalf("cursor entry = %s", encodedJSON(t, cursor))
	}
	if len(cursor.Findings) != 1 {
		t.Fatalf("cursor findings = %s, want the thread root only", encodedJSON(t, cursor.Findings))
	}
	finding := cursor.Findings[0]
	if finding.ID != "fnd_cursor_200" || finding.Text != "Guard the nil parser" || finding.State != "open" || finding.Section == nil || *finding.Section != "sec_parser" || len(finding.Anchors) != 2 {
		t.Fatalf("finding = %s", encodedJSON(t, finding))
	}
	if codex.ID != "ar_codex_0123456" || !codex.Stale || codex.Verdict != "approve" || len(codex.Findings) != 0 {
		t.Fatalf("codex entry = %s", encodedJSON(t, codex))
	}
	if doc.Stats.AgentFindings != (documentStatsAgentFindings{Open: 1}) || doc.Sections[0].Derived.OpenFindings != 1 {
		t.Fatalf("finding counts = %+v, section open = %d", doc.Stats.AgentFindings, doc.Sections[0].Derived.OpenFindings)
	}
	if !doc.Review.Sources.ReviewThreads || doc.Review.Sources.AgentReviews != 2 {
		t.Fatalf("sources = %+v", doc.Review.Sources)
	}
	if slices.Contains(doc.Review.Generator.Truncated, threadsUnavailableNote) {
		t.Fatalf("refresh kept the missing-threads note once threads arrived: %v", doc.Review.Generator.Truncated)
	}

	bundle.PullRequest.HeadSHA = "bbbbbbbbbb"
	refreshed, err = validator.Refresh(refreshed, bundle, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(refreshed, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.AgentReviews[0].Stale || doc.AgentReviews[0].Findings[0].Section == nil {
		t.Fatalf("after a push the finding lost its path's section: %s", encodedJSON(t, doc.AgentReviews[0]))
	}
	bundle.PullRequest.HeadSHA = "aaaaaaa"

	resolved = true
	bundle.Threads[0].Resolved = true
	refreshed, err = validator.Refresh(refreshed, bundle, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(refreshed, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Stats.AgentFindings != (documentStatsAgentFindings{Resolved: 1}) || doc.Sections[0].Derived.OpenFindings != 0 {
		t.Fatalf("resolved finding counts = %+v, section open = %d", doc.Stats.AgentFindings, doc.Sections[0].Derived.OpenFindings)
	}
	if !doc.Index.Threads[0].Resolved || doc.AgentReviews[0].Findings[0].State != "resolved" {
		t.Fatalf("resolving the thread did not refresh the finding: %s", refreshed)
	}
}

func TestFindingSeverityAndText(t *testing.T) {
	for _, tc := range []struct{ body, severity, text string }{
		{"**<sub><sub>![P1 Badge](https://img.shields.io/badge/P1-orange?style=flat)</sub></sub>  Serialize evidence updates**\n\nDetail", "must_fix", "Serialize evidence updates"},
		{"![P2 Badge](https://img.shields.io/badge/P2-yellow?style=flat) Keep the order", "should_fix", "Keep the order"},
		{"### Branch agreement\n\n**Medium Severity**", "should_fix", "Branch agreement"},
		{"### Lock scope\n\n**Low Severity**", "consider", "Lock scope"},
		{"Consider renaming this.", "consider", "Consider renaming this."},
		{"", "consider", "Finding"},
	} {
		if got := findingSeverity(tc.body); got != tc.severity {
			t.Errorf("severity(%q) = %q, want %q", tc.body, got, tc.severity)
		}
		if got := findingText(tc.body); got != tc.text {
			t.Errorf("text(%q) = %q, want %q", tc.body, got, tc.text)
		}
	}
}

func encodedJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

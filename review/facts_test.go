package review

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// Facts hold what a completed document derives from the same bundle, through
// the same code, and nothing a model writes or that needs a version.
func TestFactsAreTheDocumentsDerivedFields(t *testing.T) {
	validator, bundle, _ := refreshFixture(t)
	line, resolved := 11, false
	bundle.Diff[0].StartLine, bundle.Diff[0].LineCount, bundle.Diff[0].Additions = 10, 3, 3
	bundle.Diff = append(bundle.Diff, bundlepkg.Hunk{ID: "h_0123456789ab", Path: "main_test.go", Role: bundlepkg.RoleTest, Change: "added", Additions: 2, Deletions: 1})
	bundle.Sources["review_threads"] = true
	bundle.Threads = []bundlepkg.Thread{{ID: "PRRT_1", Path: "main.go", Line: &line}}
	bundle.Conversation = []bundlepkg.ConversationItem{
		{ID: "100", Kind: "review", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", State: "commented", Commit: "aaaaaaa"},
		{ID: "200", Kind: "review_comment", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", Path: "main.go", Review: "100", Thread: "PRRT_1", ThreadRoot: true, Resolved: &resolved,
			Body: "### Guard the nil parser\n\n**High Severity**\n\nThe parser can be nil."},
	}
	bundle.PullRequest.Title, bundle.PullRequest.AuthorLogin = "Add the parser", "octocat"
	bundle.Checks[0].Conclusion, bundle.Checks[0].Status = "failure", "completed"
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	authored, _ := json.Marshal(mustDecode(t, authoredFixture()))
	completed, findings := validator.Complete(authored, bundle, "", "fixture-model", now)
	if findings != "" {
		t.Fatal(findings)
	}
	var doc reviewDocument
	if err := json.Unmarshal(completed, &doc); err != nil {
		t.Fatal(err)
	}
	facts := BuildFacts(bundle, now)
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}

	if facts.Schema != FactsSchema {
		t.Errorf("schema = %q", facts.Schema)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sections", "stack"} {
		if _, ok := fields[key]; ok {
			t.Errorf("facts carry %s", key)
		}
	}
	var header map[string]json.RawMessage
	_ = json.Unmarshal(fields["review"], &header)
	for _, key := range []string{"version", "previous_version", "generation_state", "generated_at", "generator", "relations"} {
		if _, ok := header[key]; ok {
			t.Errorf("facts' review carries %s, which only a version has", key)
		}
	}
	var summary map[string]json.RawMessage
	_ = json.Unmarshal(fields["summary"], &summary)
	for _, key := range []string{"text", "context", "change_kind", "implements", "release_note", "breaking_change", "attention", "risk_score", "review_minutes"} {
		if _, ok := summary[key]; ok {
			t.Errorf("facts' summary carries the authored %s", key)
		}
	}
	var exchange map[string]json.RawMessage
	_ = json.Unmarshal(summary["review_exchange"], &exchange)
	if _, ok := exchange["summary"]; ok {
		t.Error("facts carry the review exchange's written summary")
	}

	if !reflect.DeepEqual(facts.Index, doc.Index) {
		t.Errorf("index = %s\nwant the document's %s", encodedJSON(t, facts.Index), encodedJSON(t, doc.Index))
	}
	if len(facts.AgentReviews) != 1 || len(facts.AgentReviews[0].Findings) != 1 || facts.AgentReviews[0].Findings[0].Section != nil {
		t.Errorf("agent reviews = %s, want Cursor's finding with no section", encodedJSON(t, facts.AgentReviews))
	}
	stats := facts.Stats
	if stats.Files != doc.Stats.Files || stats.Hunks != 2 || stats.Additions != doc.Stats.Additions || stats.Deletions != doc.Stats.Deletions ||
		!reflect.DeepEqual(stats.RoleLOC, doc.Stats.RoleLOC) || stats.Checks != doc.Stats.Checks || stats.AgentFindings != doc.Stats.AgentFindings {
		t.Errorf("stats = %+v, document's %+v", stats, doc.Stats)
	}
	if stats.Checks.RequiredFailed != 1 || stats.AgentFindings.Open != 1 {
		t.Errorf("stats = %+v, want the failing required check and the open finding", stats)
	}
	if !reflect.DeepEqual(facts.UnassignedHunks, []string{testHunk, "h_0123456789ab"}) {
		t.Errorf("unassigned hunks = %v, want every hunk", facts.UnassignedHunks)
	}
	if facts.Summary.Title != "Add the parser" || facts.Summary.Author != doc.Summary.Author ||
		facts.Summary.Authorship.ObservedSessions != doc.Summary.Authorship.ObservedSessions ||
		!reflect.DeepEqual(facts.Summary.Authorship.ObservedVendors, doc.Summary.Authorship.ObservedVendors) {
		t.Errorf("summary = %+v", facts.Summary)
	}
	if facts.Summary.ReviewExchange.Reviews != doc.Summary.ReviewExchange.Reviews ||
		!reflect.DeepEqual(facts.Summary.ReviewExchange.CriticalIssuesFound, doc.Summary.ReviewExchange.CriticalIssuesFound) {
		t.Errorf("review exchange = %+v, document's %+v", facts.Summary.ReviewExchange, doc.Summary.ReviewExchange)
	}
	review := facts.Review
	if review.Repository != "org/repo" || review.PullRequest != 1 || review.Head != "aaaaaaa" || review.Base != "bbbbbbb" || review.MergeBase != "bbbbbbb" ||
		review.PRState != "open" || review.EvidenceRefreshedAt != now.Format(time.RFC3339Nano) || !reflect.DeepEqual(review.Sources, doc.Review.Sources) {
		t.Errorf("review = %+v, document's sources %+v", review, doc.Review.Sources)
	}
}

func mustDecode(t *testing.T, document []byte) map[string]any {
	t.Helper()
	doc, err := decodeObject(document, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

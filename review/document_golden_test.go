package review

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/goldentest"
)

// A completed document is byte for byte what it was before its index,
// totals, author, state and sources were shared with the facts. The goldens
// were written by the code before that change; regenerate them only for an
// intended change to completed documents.
func TestCompletedDocumentGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state func(*bundlepkg.PullRequest)
	}{
		{"merged", func(pr *bundlepkg.PullRequest) { pr.State, pr.MergedSHA = "closed", "ccccccc" }},
		{"draft", func(pr *bundlepkg.PullRequest) { pr.Draft = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, bundle, _ := refreshFixture(t)
			at := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
			later := at.Add(time.Hour)
			line, resolved, optional := 11, false, false
			bundle.PullRequest.Title, bundle.PullRequest.AuthorLogin, bundle.PullRequest.AuthorAssociation = "Add the parser", "octocat", "MEMBER"
			bundle.PullRequest.RequestedReviewers = []string{"hubot"}
			tc.state(&bundle.PullRequest)
			bundle.Diff[0].StartLine, bundle.Diff[0].LineCount, bundle.Diff[0].Additions, bundle.Diff[0].DiffURL = 10, 3, 3, "https://github.com/org/repo/pull/1/files#diff-1"
			bundle.Diff[0].Patch = "@@ -9,1 +10,3 @@\n context\n+added one\n+added two\n"
			bundle.Diff = append(bundle.Diff, bundlepkg.Hunk{ID: "h_0123456789ab", Path: "main_test.go", Role: bundlepkg.RoleTest, Change: "added", Additions: 2, Deletions: 1})
			bundle.Checks = append(bundle.Checks,
				bundlepkg.Check{ID: "lint", Name: "Lint", Kind: "check_run", Status: "completed", Conclusion: "failure", Required: &optional, URL: "https://github.com/org/repo/actions/runs/1", StartedAt: &at, CompletedAt: &later},
				bundlepkg.Check{ID: "old", Name: "Old", Kind: "check_run", Status: "completed", Conclusion: "success", HeadSHA: "0000000"},
			)
			bundle.Sources["review_threads"] = true
			bundle.Threads = []bundlepkg.Thread{{ID: "PRRT_1", Path: "main.go", Line: &line, URL: "https://github.com/org/repo/pull/1#discussion_r200"}}
			bundle.Conversation = []bundlepkg.ConversationItem{
				{ID: "50", Kind: "comment", AuthorLogin: "hubot", Body: "Looks fine.", CreatedAt: &at},
				{ID: "100", Kind: "review", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", State: "commented", Commit: "aaaaaaa", CreatedAt: &at},
				{ID: "200", Kind: "review_comment", AuthorLogin: "cursor[bot]", Bot: true, BotTool: "Cursor", Path: "main.go", Review: "100", Thread: "PRRT_1", ThreadRoot: true, Resolved: &resolved,
					Body: "### Guard the nil parser\n\n**High Severity**\n\nThe parser can be nil.", CreatedAt: &later},
				{ID: "300", Kind: "review", AuthorLogin: "hubot", State: "changes_requested", Commit: "aaaaaaa", CreatedAt: &later},
			}
			bundle.Sessions = append(bundle.Sessions, bundlepkg.Session{RootSessionID: "restricted", Vendor: "codex", Restricted: true})
			bundle.ReviewConfig = &bundlepkg.ReviewConfig{Present: true, Digest: "sha256:review"}
			bundle.References = []bundlepkg.Reference{{Kind: "spec", Value: "docs/spec.md", Source: "body"}}
			bundle.Truncated = []string{"Some comments were left out."}

			authored, _ := json.Marshal(mustDecode(t, authoredFixture()))
			completed, findings := validator.CompleteWithPrompt(authored, bundle, "", "fixture-model", "v1", later)
			if findings != "" {
				t.Fatal(findings)
			}
			goldentest.Text(t, filepath.Join("testdata", "completed-"+tc.name+".golden.json"), string(completed)+"\n")
		})
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

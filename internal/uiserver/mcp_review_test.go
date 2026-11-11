package uiserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// The review tools read a stored review, a group's, a running draft and the
// conversations through the real routes, a part per call.
func TestReviewToolsReadPartsOfTheRealRoutes(t *testing.T) {
	f := newPullFixture(t)
	chatToken, chatID, revokeChat, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeChat()
	if err := f.mcp.Registry.SetPage(chatID, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "owner/repo", PullRequest: 8}); err != nil {
		t.Fatal(err)
	}
	actionToken, _, revokeAction, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: "owner/repo", PullRequest: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeAction()
	selector := "clone=" + f.clone.ID + "&pr=8"
	for name, token := range map[string]string{"chat": chatToken, "action": actionToken} {
		t.Run(name, func(t *testing.T) {
			// The outline: the overview and the sections, no details, no context.
			text, failed := f.callTool(t, token, "review_outline", nil)
			if failed || !strings.Contains(text, "Overview8") || !strings.Contains(text, `"title":"Search box"`) || !strings.Contains(text, `"details":"failed"`) || !strings.Contains(text, `"files":["main.go"]`) {
				t.Fatalf("outline: %v %s", failed, text)
			}
			for _, hidden := range []string{"DETAILS-SEC-1", "Context8", `"derived"`} {
				if strings.Contains(text, hidden) {
					t.Errorf("the outline carries %s: %s", hidden, text)
				}
			}
			// A section is the real document's section.
			var document struct{ Sections []json.RawMessage }
			if err := json.Unmarshal(f.route(t, "/api/document?"+selector), &document); err != nil || len(document.Sections) != 2 {
				t.Fatalf("document: %v", err)
			}
			text, failed = f.callTool(t, token, "review_section", map[string]any{"id": "sec_1"})
			if failed || strings.Contains(text, "Overview8") || strings.Contains(text, "Docs") {
				t.Fatalf("section: %v %s", failed, text)
			}
			sameJSON(t, "section", field(t, text, "section"), document.Sections[0])
			text, _ = f.callTool(t, token, "review_section", map[string]any{"id": "sec_2"})
			if !strings.Contains(text, `"details":"failed"`) || !strings.Contains(text, "could not be written") {
				t.Errorf("a failed section: %s", text)
			}
			text, failed = f.callTool(t, token, "review_context", nil)
			if failed || !strings.Contains(text, "Context8") || strings.Contains(text, "sec_1") || strings.Contains(text, "Overview8") {
				t.Errorf("context: %v %s", failed, text)
			}
			// The versions of the real store, without the session that wrote them.
			text, failed = f.callTool(t, token, "review_versions", nil)
			if failed || !strings.Contains(text, `"model":"opus"`) || strings.Contains(text, "secret-session") || strings.Contains(text, `"session"`) {
				t.Errorf("versions: %v %s", failed, text)
			}
			var versions []struct{ Version int }
			if err := json.Unmarshal(field(t, text, "versions"), &versions); err != nil || len(versions) != 1 || versions[0].Version != 1 {
				t.Errorf("versions %s", text)
			}
			// The conversations of the real chat route.
			text, failed = f.callTool(t, token, "review_conversations", nil)
			if failed || !strings.Contains(text, `"id":"conv-1"`) || !strings.Contains(text, "Is the search box safe?") || strings.Contains(text, "Yes.") {
				t.Errorf("conversations: %v %s", failed, text)
			}
			text, failed = f.callTool(t, token, "review_conversations", map[string]any{"id": "conv-1"})
			if failed || !strings.Contains(text, `"answer_markdown":"Yes."`) || !strings.Contains(text, `"path":"main.go"`) || strings.Contains(text, "job-secret") || strings.Contains(text, "cost") {
				t.Errorf("a conversation: %v %s", failed, text)
			}
			// The facts of a pull request without a review.
			text, failed = f.callTool(t, token, "facts", map[string]any{"pr": 7})
			if failed {
				t.Fatalf("facts: %s", text)
			}
			sameJSON(t, "facts", json.RawMessage(text), f.route(t, "/api/facts?clone="+f.clone.ID+"&pr=7&head="+pullHead))

			// A running review's draft, and none when nothing runs.
			text, failed = f.callTool(t, token, "review_draft", nil)
			if failed || !strings.Contains(text, "DraftOverview of j-running") || !strings.Contains(text, `"details":"pending"`) || strings.Contains(text, "foreign") {
				t.Errorf("draft: %v %s", failed, text)
			}
			text, failed = f.callTool(t, token, "review_draft", map[string]any{"pr": 7})
			if failed || !strings.Contains(text, "No running review") {
				t.Errorf("no running review: %v %s", failed, text)
			}

			// A group's combined review, from the real groups store.
			for tool, want := range map[string]string{"review_outline": "GroupOverview", "review_context": "GroupContext"} {
				text, failed := f.callTool(t, token, tool, map[string]any{"group": f.own.ID})
				if failed || !strings.Contains(text, want) {
					t.Errorf("group %s: %v %s", tool, failed, text)
				}
			}
			text, failed = f.callTool(t, token, "review_section", map[string]any{"group": f.own.ID, "id": "gsec_1"})
			if failed || !strings.Contains(text, "GROUP-DETAILS") {
				t.Errorf("group section: %v %s", failed, text)
			}
			text, _ = f.callTool(t, token, "review_versions", map[string]any{"group": f.own.ID})
			if !strings.Contains(text, `"model":"opus"`) || !strings.Contains(text, f.own.ID) {
				t.Errorf("group versions: %s", text)
			}
		})
	}
	// The action's own group is the default for "this".
	if text, failed := f.callTool(t, actionToken, "review_outline", map[string]any{"group": "this"}); failed || !strings.Contains(text, "GroupOverview") {
		t.Errorf("an action's group: %v %s", failed, text)
	}
	// Another repository's group, job and clone are refused, for each family.
	for _, tool := range []string{"review_outline", "review_section", "review_context", "review_versions"} {
		if text, failed := f.callTool(t, actionToken, tool, map[string]any{"group": f.foreign.ID, "id": "x"}); !failed || strings.Contains(text, "Private") {
			t.Errorf("%s of a foreign group: %v %s", tool, failed, text)
		}
	}
	for _, tool := range []string{"facts", "review_outline", "review_section", "review_context", "review_draft", "review_versions", "review_conversations"} {
		if text, failed := f.callTool(t, actionToken, tool, map[string]any{"repository": "other/repo", "pr": 8, "id": "x"}); !failed || strings.Contains(text, "Another") {
			t.Errorf("%s of another repository: %v %s", tool, failed, text)
		}
	}
}

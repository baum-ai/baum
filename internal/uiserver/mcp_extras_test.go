package uiserver

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// rawRoute is the status a GET of path through the daemon answers.
func (f *pullFixture) rawRoute(path string) int {
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path, nil))
	return w.Code
}

// mintBoth mints a chat on the pull request's page and an action on it.
func (f *pullFixture) mintBoth(t *testing.T, repository string, number int) (chat, action string) {
	t.Helper()
	chat, id, revokeChat, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revokeChat)
	if err := f.mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: repository, PullRequest: number}); err != nil {
		t.Fatal(err)
	}
	action, _, revokeAction, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Action, Repository: repository, PullRequest: number})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revokeAction)
	return chat, action
}

// The pull request page's extra tools return what the routes the app's
// views call return, as a chat and as an action. Fakes, where the real thing
// needs a whole app: the commit and file readers read git, which the
// fixture's empty clone answers as unavailable (the routes' own answer), so
// the commit's files and a file's text are not compared; the decisions and
// kept state are in TestKeptStateAndDecisionsToolsReadTheRealRoutes.
func TestPullRequestExtraToolsReturnWhatTheRoutesReturn(t *testing.T) {
	f := newPullFixture(t)
	chat, action := f.mintBoth(t, "owner/repo", 7)
	const linked = "cccccccccccccccccccccccccccccccccccccccc"
	for name, token := range map[string]string{"chat": chat, "action": action} {
		t.Run(name, func(t *testing.T) {
			// Pull request 8 has a saved review: its routes; pull request 7 has
			// only facts.
			for tool, route := range map[string]string{
				"commit": "/api/commit?clone=" + f.clone.ID + "&pr=8&oid=" + linked,
			} {
				text, failed := f.callTool(t, token, tool, map[string]any{"pr": 8, "oid": linked})
				if failed {
					t.Fatalf("%s: %s", tool, text)
				}
				sameJSON(t, tool, json.RawMessage(text), f.route(t, route))
			}
			text, failed := f.callTool(t, token, "commit", map[string]any{"oid": linked})
			if failed {
				t.Fatalf("commit of the facts: %s", text)
			}
			sameJSON(t, "facts commit", json.RawMessage(text), f.route(t, "/api/facts/commit?clone="+f.clone.ID+"&pr=7&head="+pullHead+"&oid="+linked))
			// The review's file: the fixture's saved document has a context the
			// file route cannot parse, so it cites no file and the route answers
			// not found, which the tool reports (a cited file is read in
			// agentcontext's tests, against a fake route).
			if code := f.rawRoute("/api/file?clone=" + f.clone.ID + "&pr=8&path=main.go"); code != 404 {
				t.Fatalf("the route's own answer for an uncited file: %d", code)
			}
			if text, failed := f.callTool(t, token, "file", map[string]any{"pr": 8, "path": "main.go"}); !failed || !strings.Contains(text, "cites") {
				t.Errorf("file: %v %s", failed, text)
			}
			for tool, args := range map[string]map[string]any{
				"file":         {"pr": 8, "path": "never-cited.go"},
				"commit_patch": {"pr": 8, "oid": linked, "path": "main.go"},
				"commit":       {"pr": 8, "oid": strings.Repeat("a", 40)},
			} {
				if text, failed := f.callTool(t, token, tool, args); !failed {
					t.Errorf("%s %v: %s", tool, args, text)
				}
			}
			if text, failed := f.callTool(t, token, "file", map[string]any{"path": "main.go"}); !failed || !strings.Contains(text, "no saved review") {
				t.Errorf("a file of a pull request without a review: %v %s", failed, text)
			}
			for tool, route := range map[string]string{
				"update_branch_options": "/api/prs/update-branch-options?repository=owner/repo&pr=7",
				"rerun_options":         "/api/prs/rerun-checks-options?repository=owner/repo&pr=7",
			} {
				text, failed := f.callTool(t, token, tool, nil)
				if failed {
					t.Fatalf("%s: %s", tool, text)
				}
				sameJSON(t, tool, json.RawMessage(text), f.route(t, route))
			}
			// A group's patch and file name a path its review cites; this one
			// cites none, which the route answers as not found.
			for _, tool := range []string{"group_patch", "group_file"} {
				if text, failed := f.callTool(t, token, tool, map[string]any{"id": f.own.ID, "path": "main.go"}); !failed || !strings.Contains(text, "cites") {
					t.Errorf("%s of a path the group does not cite: %v %s", tool, failed, text)
				}
				if text, failed := f.callTool(t, token, tool, map[string]any{"id": f.foreign.ID, "path": "main.go"}); !failed || strings.Contains(text, "Private") {
					t.Errorf("%s of another repository's group: %v %s", tool, failed, text)
				}
			}
		})
	}
	// An action asking for another repository's pull request, or another
	// pull request's kept state, reaches nothing.
	other := map[string]any{"repository": "other/repo", "pr": 7}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range other {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for tool, args := range map[string]map[string]any{
		"file":                   with(map[string]any{"path": "main.go"}),
		"commit":                 with(map[string]any{"oid": linked}),
		"commit_patch":           with(map[string]any{"oid": linked, "path": "main.go"}),
		"pull_request_decisions": with(nil),
		"update_branch_options":  with(nil),
		"rerun_options":          with(nil),
		"pending_review":         with(nil),
		"walkthrough_position":   with(map[string]any{"version": 1}),
	} {
		if text, failed := f.callTool(t, action, tool, args); !failed || !strings.Contains(text, "own pull request's repository") {
			t.Errorf("%s of another repository: %v %s", tool, failed, text)
		}
	}
}

package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/agentrequests"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/repos"
)

// A request the app shows cleaned or with a secret masked cannot be allowed
// from a proposal (the person would allow what they did not see); denying it
// can. A long request is shown whole and may be allowed. (One whose input
// would be cut is never shown: agentrequests refuses it.)
func TestAnAlteredRequestIsNotAllowed(t *testing.T) {
	f := newOpFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSessions})
	masked := f.ask(agentrequests.Ask{Agent: "claude-code", SessionID: "s1", Kind: agentrequests.KindPermission, Tool: "Bash",
		Input: []agentrequests.Row{{Label: "Command", Value: "curl -H 'Authorization: SECRET-TOKEN' https://example.com"}}})
	cleaned := f.ask(agentrequests.Ask{Agent: "claude-code", SessionID: "s1", Kind: agentrequests.KindPermission, Tool: "Bash",
		Input: []agentrequests.Row{{Label: "Command", Value: "echo safe\u202e; rm -rf ~"}}})
	for name, id := range map[string]string{"masked": masked, "cleaned": cleaned} {
		if _, failed := f.propose("propose_answer_request", map[string]any{"id": id, "behavior": "allow"}); !strings.Contains(failed, "cannot be allowed") {
			t.Errorf("a %s request was allowed: %s", name, failed)
		}
		if _, failed := f.propose("propose_answer_request", map[string]any{"id": id, "behavior": "deny"}); failed != "" {
			t.Errorf("a %s request could not be denied: %s", name, failed)
		}
	}
	if _, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "allow"}); failed != "" {
		t.Fatalf("a request of %d characters, shown whole, was refused: %s", len([]rune(longCommand)), failed)
	}
}

// A question is answered with the answers the agent wrote, each shown with
// its question in full, and the route records them.
func TestAQuestionIsAnswered(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	question := "Which database should the search index use, offline?"
	id := f.ask(agentrequests.Ask{Agent: "claude-code", SessionID: "s1", Kind: agentrequests.KindQuestion, Tool: "AskUserQuestion",
		Questions: []agentrequests.Question{{Question: question, Options: []agentrequests.Option{{Label: "SQLite"}, {Label: "Postgres"}}}}})
	f.page(agentcontext.PageContext{Page: agentcontext.PageSessions})
	if _, failed := f.propose("propose_answer_request", map[string]any{"id": id, "behavior": "allow", "answers": map[string]string{"Another question?": "SQLite"}}); failed == "" {
		t.Fatal("an answer to a question the request does not ask was proposed")
	}
	receipt, failed := f.propose("propose_answer_request", map[string]any{"id": id, "behavior": "allow", "answers": map[string]string{question: "SQLite"}})
	if failed != "" {
		t.Fatal(failed)
	}
	p := f.proposal(receipt["id"].(string))
	if q, _ := field(p, "Question"); q.Value != question || !q.Multiline {
		t.Fatalf("question %+v", q)
	}
	if a, _ := field(p, "Answer"); a.Value != "SQLite" || !a.ByAgent {
		t.Fatalf("answer %+v", a)
	}
	if outcome, err := f.confirm(ctx, p.ID); err != nil || !outcome.OK() {
		t.Fatal(outcome, err)
	}
	pending, _ := f.requests.List()
	for _, r := range pending {
		if r.ID == id {
			t.Fatal("the question is still pending")
		}
	}
}

// Confirm takes the revision of the summary the app showed: after a change,
// a second click that still carries the old revision runs nothing, and one
// carrying the new revision runs.
func TestConfirmRunsOnlyTheSummaryShown(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	f.page(pullPage)
	receipt, failed := f.propose("propose_merge", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	id := receipt["id"].(string)
	shown := f.proposal(id).Operation.Revision
	if _, err := f.mcp.Proposals.Confirm(ctx, f.id, id, ""); !errors.Is(err, agentcontext.ErrOperationChanged) {
		t.Fatal("a confirm that names no summary ran:", err)
	}
	f.setTitle(7, "Delete everything")
	if _, err := f.mcp.Proposals.Confirm(ctx, f.id, id, shown); !errors.Is(err, agentcontext.ErrOperationChanged) {
		t.Fatal(err)
	}
	updated := f.proposal(id).Operation.Revision
	if updated == shown {
		t.Fatal("the summary's revision did not change")
	}
	// The double click: the app's second confirm still carries the old
	// revision, though the store now holds the new summary.
	if _, err := f.mcp.Proposals.Confirm(ctx, f.id, id, shown); !errors.Is(err, agentcontext.ErrOperationChanged) {
		t.Fatal(err)
	}
	if writes, merged := f.log.take(), f.github.take(); len(writes) != 0 || len(merged) != 0 {
		t.Fatalf("ran %v %v", writes, merged)
	}
	if outcome, err := f.mcp.Proposals.Confirm(ctx, f.id, id, updated); err != nil || !outcome.OK() {
		t.Fatal(outcome, err)
	}
	if merged := f.github.take(); len(merged) != 1 {
		t.Fatalf("merged %v", merged)
	}
}

// A paid run's summary names the model and effort from the settings: a
// change to them before Confirm runs nothing, and settings that cannot be
// read refuse the proposal and the Confirm rather than show a guess.
func TestAPaidRunConfirmsItsSettings(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	f.page(pullPage)
	receipt, failed := f.propose("propose_review", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	id := receipt["id"].(string)
	before := f.proposal(id).Operation.Summary
	other, effort := "claude-other", "max"
	if _, err := f.registry.UpdatePreferences(ctx, func(p repos.Preferences) repos.Preferences {
		p.Model, p.Effort = &other, &effort
		return p
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.confirm(ctx, id); !errors.Is(err, agentcontext.ErrOperationChanged) {
		t.Fatal("a review ran with another model:", err)
	}
	after := f.proposal(id).Operation.Summary
	if after.Model != other || after.Effort != effort || before.Model == after.Model {
		t.Fatalf("the summary shows %s/%s, was %s/%s", after.Model, after.Effort, before.Model, before.Effort)
	}
	if writes := f.log.take(); len(writes) != 0 || len(f.queue.OfPullRequest("owner/repo", 7)) != 0 {
		t.Fatalf("ran %v", writes)
	}
	f.log.failSettings.Store(true)
	if _, err := f.confirm(ctx, id); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatal("a review was confirmed without its settings:", err)
	}
	if _, failed := f.propose("propose_fix", map[string]any{}); !strings.Contains(failed, "cannot be read") {
		t.Fatal("a fix was proposed without its settings:", failed)
	}
	if writes := f.log.take(); len(writes) != 0 {
		t.Fatalf("ran %v", writes)
	}

	// The settings route still answers when the saved choices cannot be
	// read, with the config file's alone: a run is not shown with those.
	f.log.failSettings.Store(false)
	db, err := appdb.Open(f.registryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `UPDATE preferences SET value='{' WHERE key='automatic'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Preferences(ctx); err == nil {
		t.Fatal("the preferences were still readable")
	}
	if _, err := f.confirm(ctx, id); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatal("a review was confirmed with the config file's settings:", err)
	}
	if _, failed := f.propose("propose_review", map[string]any{}); !strings.Contains(failed, "cannot be read") {
		t.Fatal("a review was proposed with the config file's settings:", failed)
	}
	if writes := f.log.take(); len(writes) != 0 || len(f.queue.OfPullRequest("owner/repo", 7)) != 0 {
		t.Fatalf("ran %v", writes)
	}
}

// A chat whose own session is not known proposes nothing on sessions:
// answers, messages, queued messages, ends and focus are all refused. Its
// session, set later, cannot then be changed, so no proposal made before it
// was set can come to target it.
func TestAChatWithNoSessionActsOnNone(t *testing.T) {
	f := newOpFixture(t)
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	reads := &readFixture{mcp: f.mcp}
	sessions := agentcontext.PageContext{Page: agentcontext.PageSessions}
	if err := f.mcp.Registry.SetPage(id, sessions); err != nil {
		t.Fatal(err)
	}
	for tool, args := range map[string]map[string]any{
		"propose_answer_request": {"id": f.request, "behavior": "deny"},
		"propose_send_message":   {"session": "claude-code:s2", "text": "hi"},
		"propose_queue_message":  {"session": "claude-code:s1", "text": "hi"},
		"propose_end_session":    {"session": "claude-code:s2"},
		"propose_focus_session":  {"session": "claude-code:s2"},
	} {
		if text, failed := reads.callTool(t, token, tool, args); !failed || !strings.Contains(text, "own session is not known") {
			t.Errorf("%s: %s", tool, text)
		}
	}
	if len(f.mcp.Proposals.List(id)) != 0 {
		t.Fatal("a chat with no session holds a proposal")
	}
}

// What is sent and named is the registry's spelling of a repository, not
// the agent's; a message queued for a session that holds one says which it
// replaces.
func TestSummariesAgreeWithWhatIsSent(t *testing.T) {
	f := newOpFixture(t)
	for _, tt := range []struct {
		page agentcontext.PageContext
		tool string
		args map[string]any
		sent string
	}{
		{agentcontext.PageContext{Page: agentcontext.PageInbox}, "propose_refresh", map[string]any{"repository": "OWNER/Repo"}, "owner/repo"},
		{agentcontext.PageContext{Page: agentcontext.PageRepositories}, "propose_register_repository", map[string]any{"repository": "Acme/Web"}, "acme/web"},
	} {
		f.page(tt.page)
		receipt, failed := f.propose(tt.tool, tt.args)
		if failed != "" {
			t.Fatal(failed)
		}
		p := f.proposal(receipt["id"].(string))
		var body struct{ Repository string }
		_ = json.Unmarshal(p.Operation.Request.Body, &body)
		if body.Repository != tt.sent || !strings.Contains(p.Operation.Summary.Action, " "+tt.sent+" ") || p.Target != tt.sent {
			t.Errorf("%s sends %q and says %q", tt.tool, body.Repository, p.Operation.Summary.Action)
		}
	}
	if err := f.requests.Queue("claude-code", "s1", "The message queued before"); err != nil {
		t.Fatal(err)
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageSessions})
	receipt, failed := f.propose("propose_queue_message", map[string]any{"session": "claude-code:s1", "text": "The new one"})
	if failed != "" {
		t.Fatal(failed)
	}
	p := f.proposal(receipt["id"].(string))
	if _, ok := field(p, "Replaces"); !ok {
		t.Fatalf("the summary does not say it replaces a message: %+v", p.Operation.Summary)
	}
	if queued, _ := field(p, "Queued message"); queued.Value != "The message queued before" {
		t.Fatalf("queued %+v", queued)
	}
}

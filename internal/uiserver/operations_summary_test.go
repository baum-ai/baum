package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// field is the summary field labelled label, and whether there is one.
func field(p agentcontext.Proposal, label string) (agentcontext.SummaryField, bool) {
	for _, f := range p.Operation.Summary.Details {
		if f.Label == label {
			return f, true
		}
	}
	return agentcontext.SummaryField{}, false
}

// What the agent wrote is shown in full, exactly as it is sent; text longer
// than the operation takes is refused, never cut; and text the summary could
// not show as it is (control and bidirectional characters) is refused.
func TestTheAgentsTextIsShownInFull(t *testing.T) {
	f := newOpFixture(t)
	sessionPage := agentcontext.PageContext{Page: agentcontext.PageSession, Session: "claude-code:s2"}
	// The longest messages, most of them two-byte characters, with a tail of
	// their own.
	long := func(n int) string { return strings.Repeat("é", n-len("TAIL")) + "TAIL" }
	for _, tt := range []struct {
		page agentcontext.PageContext
		tool string
		args map[string]any
		max  int
	}{
		{sessionPage, "propose_send_message", map[string]any{}, 10000},
		{agentcontext.PageContext{Page: agentcontext.PageSessions}, "propose_queue_message", map[string]any{"session": "claude-code:s1"}, 4000},
	} {
		f.page(tt.page)
		tt.args["text"] = long(tt.max + 1)
		if _, failed := f.propose(tt.tool, tt.args); failed == "" {
			t.Fatalf("%s: a message longer than %d characters was proposed", tt.tool, tt.max)
		}
		text := long(tt.max-len("\nend")) + "\nend"
		tt.args["text"] = text
		receipt, failed := f.propose(tt.tool, tt.args)
		if failed != "" {
			t.Fatalf("%s: %s", tt.tool, failed)
		}
		p := f.proposal(receipt["id"].(string))
		message, ok := field(p, "Message")
		if !ok || message.Value != text || !message.Quoted || !message.ByAgent || !message.Multiline {
			t.Fatalf("%s: the summary shows %d characters", tt.tool, len([]rune(message.Value)))
		}
		var body struct{ Text string }
		_ = json.Unmarshal(p.Operation.Request.Body, &body)
		if body.Text != text {
			t.Fatalf("%s: what runs is not what is shown", tt.tool)
		}
		if err := f.mcp.Proposals.Dismiss(f.id, p.ID); err != nil {
			t.Fatal(err)
		}
		for _, hidden := range []string{"hi \u202eevil", "a\u2066b", "zero\u200bwidth", "bell\a", "carriage\rreturn"} {
			tt.args["text"] = hidden
			if _, failed := f.propose(tt.tool, tt.args); !strings.Contains(failed, "character") && !strings.Contains(failed, "carriage") {
				t.Errorf("%s: %q was proposed: %s", tt.tool, hidden, failed)
			}
		}
	}
	// A group's name of 80 two-byte characters is shown whole; 81 are
	// refused, and so is one with a bidirectional override.
	f.page(agentcontext.PageContext{Page: agentcontext.PagePulls})
	name := strings.Repeat("ü", 80)
	receipt, failed := f.propose("propose_create_group", map[string]any{"name": name, "repository": "owner/repo", "numbers": []int{7, 8}})
	if failed != "" {
		t.Fatal(failed)
	}
	if got, _ := field(f.proposal(receipt["id"].(string)), "Name"); got.Value != name || !got.ByAgent {
		t.Fatalf("the name shows as %q", got.Value)
	}
	for _, bad := range []string{name + "ü", "Pair\u202e"} {
		if _, failed := f.propose("propose_create_group", map[string]any{"name": bad, "repository": "owner/repo", "numbers": []int{7, 8}}); failed == "" {
			t.Errorf("%q was proposed", bad)
		}
	}
	if writes := f.log.take(); len(writes) != 0 {
		t.Fatalf("ran %v", writes)
	}
}

// An approval shows the request's every input value in full and names the
// session by its title and repository; a permission's suggestions (allow
// always, a mode) cannot be proposed.
func TestAnApprovalShowsWhatWasAsked(t *testing.T) {
	f := newOpFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSessions})
	if _, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "allow", "suggestion": "0"}); failed == "" {
		t.Fatal("a suggestion was proposed")
	}
	if _, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "allow", "answers": map[string]string{"Q?": "yes"}}); failed == "" {
		t.Fatal("answers to a permission request were proposed")
	}
	receipt, failed := f.propose("propose_answer_request", map[string]any{"id": f.request, "behavior": "allow"})
	if failed != "" {
		t.Fatal(failed)
	}
	p := f.proposal(receipt["id"].(string))
	if name, _ := field(p, "Input name"); name.Value != "Command" || !name.Quoted {
		t.Fatalf("input name %+v", name)
	}
	if input, _ := field(p, "Input value"); input.Value != longCommand || !input.Quoted {
		t.Fatalf("the input shows as %d characters, ending %q", len([]rune(input.Value)), input.Value[max(0, len(input.Value)-12):])
	}
	if len([]rune(longCommand)) != 4000 {
		t.Fatalf("the fixture's command is %d characters", len([]rune(longCommand)))
	}
	if tool, _ := field(p, "Tool"); tool.Value != "Bash" || !tool.Quoted {
		t.Fatalf("tool %+v", tool)
	}
	if title, ok := field(p, "Session title"); !ok || title.Value == "" {
		t.Fatalf("the session is not named by its title: %+v", p.Operation.Summary)
	}
	if repository, _ := field(p, "Repository"); repository.Value != "owner/repo" {
		t.Fatalf("repository %+v", repository)
	}
	if strings.Contains(p.Operation.Summary.Action, "Bash") {
		t.Fatal("the tool's own name is in baum's sentence")
	}
}

// Untrusted text read from the routes is shown plain and apart from baum's
// sentence: no bidirectional override, control or invisible character, and
// no line break in a one-line field.
func TestUntrustedTextIsShownPlain(t *testing.T) {
	f := newOpFixture(t)
	f.setTitle(7, "Add \u202esearch\nnow\u200b\a")
	f.page(pullPage)
	receipt, failed := f.propose("propose_merge", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	p := f.proposal(receipt["id"].(string))
	if title, _ := field(p, "Pull request title"); title.Value != "Add search now" || !title.Quoted || title.Multiline || title.ByAgent {
		t.Fatalf("the title shows as %q", title.Value)
	}
	if strings.ContainsAny(p.Operation.Summary.Action, "\u202e\n") || strings.Contains(p.Operation.Summary.Action, "search") {
		t.Fatalf("the sentence holds the title: %q", p.Operation.Summary.Action)
	}
}

// A summary that reads differently at Confirm (here the pull request's
// title changed) runs nothing; the proposal then holds the new summary, and
// confirming that runs it.
func TestAChangedSummaryRefusesTheConfirm(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	f.page(pullPage)
	receipt, failed := f.propose("propose_merge", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	id := receipt["id"].(string)
	f.setTitle(7, "Delete everything")
	if _, err := f.confirm(ctx, id); !errors.Is(err, agentcontext.ErrOperationChanged) {
		t.Fatal(err)
	}
	if writes, merged := f.log.take(), f.github.take(); len(writes) != 0 || len(merged) != 0 {
		t.Fatalf("ran %v %v", writes, merged)
	}
	if title, _ := field(f.proposal(id), "Pull request title"); title.Value != "Delete everything" {
		t.Fatalf("the proposal shows %q", title.Value)
	}
	if outcome, err := f.confirm(ctx, id); err != nil || !outcome.OK() {
		t.Fatal(outcome, err)
	}
	if merged := f.github.take(); len(merged) != 1 {
		t.Fatalf("merged %v", merged)
	}
}

// A chat whose session is not known answers no request; one whose session
// is known never answers, messages, queues for, ends or brings forward its
// own session. The session is set once.
func TestAChatNeverActsOnItsOwnSession(t *testing.T) {
	f := newOpFixture(t)
	token, id, revoke, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	reads := &readFixture{mcp: f.mcp}
	propose := func(token, id string, page agentcontext.PageContext, tool string, args map[string]any) string {
		t.Helper()
		if err := f.mcp.Registry.SetPage(id, page); err != nil {
			t.Fatal(err)
		}
		text, failed := reads.callTool(t, token, tool, args)
		if !failed {
			return ""
		}
		return text
	}
	sessions := agentcontext.PageContext{Page: agentcontext.PageSessions}
	if failed := propose(token, id, sessions, "propose_answer_request", map[string]any{"id": f.request, "behavior": "deny"}); !strings.Contains(failed, "own session is not known") {
		t.Fatalf("a chat with no session answered a request: %s", failed)
	}
	// The panel learns the session late, and sets it once.
	if err := f.mcp.Registry.SetSession(id, agentcontext.SessionKey("claude-code", "s2")); err != nil {
		t.Fatal(err)
	}
	if err := f.mcp.Registry.SetSession(id, agentcontext.SessionKey("claude-code", "s1")); err == nil {
		t.Fatal("a chat's session was swapped")
	}
	own := agentcontext.PageContext{Page: agentcontext.PageSession, Session: "claude-code:s2"}
	for _, tt := range []struct {
		page agentcontext.PageContext
		tool string
		args map[string]any
	}{
		{own, "propose_send_message", map[string]any{"text": "hi"}},
		{sessions, "propose_send_message", map[string]any{"session": "claude-code:s2", "text": "hi"}},
		{own, "propose_end_session", map[string]any{}},
		{own, "propose_focus_session", map[string]any{}},
	} {
		if failed := propose(token, id, tt.page, tt.tool, tt.args); !strings.Contains(failed, "its own session") {
			t.Errorf("%s on %s: %s", tt.tool, tt.page.Page, failed)
		}
	}
	// It may act on another session.
	if failed := propose(token, id, sessions, "propose_queue_message", map[string]any{"session": "claude-code:s1", "text": "hi"}); failed != "" {
		t.Fatal(failed)
	}
	// s1's chat: its queued message and its own request.
	token1, id1, revoke1, err := f.mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat, Session: agentcontext.SessionKey("claude-code", "s1")})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke1()
	for tool, args := range map[string]map[string]any{
		"propose_queue_message":  {"session": "claude-code:s1", "text": "hi"},
		"propose_answer_request": {"id": f.request, "behavior": "allow"},
	} {
		if failed := propose(token1, id1, sessions, tool, args); !strings.Contains(failed, "its own session") {
			t.Errorf("%s: %s", tool, failed)
		}
	}
	if writes := f.log.take(); len(writes) != 0 {
		t.Fatalf("ran %v", writes)
	}
}

// An agent cannot point an operation at another pull request, session, job,
// group or repository than the page shows.
func TestOperationsKeepToThePagesTarget(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	s2 := agentcontext.PageContext{Page: agentcontext.PageSession, Session: "claude-code:s2"}
	ownRepository := agentcontext.PageContext{Page: agentcontext.PageRepositories, Repository: "owner/repo"}
	for _, tt := range []struct {
		page agentcontext.PageContext
		tool string
		args map[string]any
	}{
		{pullPage, "propose_review", map[string]any{"repository": "owner/repo", "number": 8}},
		{pullPage, "propose_review", map[string]any{"repository": "other/repo", "number": 7}},
		{pullPage, "propose_merge", map[string]any{"number": 8}},
		{pullPage, "propose_fix", map[string]any{"repository": "owner/repo"}},
		{agentcontext.PageContext{Page: agentcontext.PageInbox}, "propose_review", map[string]any{"repository": "owner/repo", "number": 99}},
		{s2, "propose_send_message", map[string]any{"session": "claude-code:s1", "text": "hi"}},
		{s2, "propose_queue_message", map[string]any{"session": "claude-code:s1", "text": "hi"}},
		{s2, "propose_end_session", map[string]any{"session": "claude-code:s1"}},
		{s2, "propose_answer_request", map[string]any{"id": f.request, "behavior": "deny"}},
		{agentcontext.PageContext{Page: agentcontext.PageJob, Job: "j1"}, "propose_cancel_job", map[string]any{"id": "j2"}},
		{agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group}, "propose_delete_group", map[string]any{"id": "other"}},
		{ownRepository, "propose_remove_repository", map[string]any{"repository": "acme/web"}},
		{ownRepository, "propose_clone_repository", map[string]any{"repository": "acme/web"}},
		{ownRepository, "propose_register_repository", map[string]any{"repository": "acme/web"}},
	} {
		f.page(tt.page)
		if _, failed := f.propose(tt.tool, tt.args); failed == "" {
			t.Errorf("%s on %+v proposed %v", tt.tool, tt.page, tt.args)
		}
	}
	// The Jobs page offers a cancel only of a job it lists as queued or
	// running: an unknown id, and one that finished, are refused.
	f.page(pullPage)
	receipt, failed := f.propose("propose_fix", map[string]any{})
	if failed != "" {
		t.Fatal(failed)
	}
	if _, err := f.confirm(ctx, receipt["id"].(string)); err != nil {
		t.Fatal(err)
	}
	fix := f.queue.OfPullRequest("owner/repo", 7)[0]
	if _, err := f.queue.Cancel(fix.ID); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if j := f.queue.OfPullRequest("owner/repo", 7)[0]; j.State != "running" && j.State != "queued" {
			break
		}
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageJobs})
	for _, id := range []string{fix.ID, "0123456789abcdef0123456789abcdef"} {
		if _, failed := f.propose("propose_cancel_job", map[string]any{"id": id}); !strings.Contains(failed, "not one the Jobs page lists") {
			t.Errorf("%s: %s", id, failed)
		}
	}
	f.log.take()
}

// A comment for the pending review is offered on its pull request's page
// only. The person reads what the agent wrote as the agent's, the title as
// the pull request's own, and the comment joins their review only once they
// confirm, on that page.
func TestACommentJoinsThePendingReviewOnlyOnConfirm(t *testing.T) {
	ctx := context.Background()
	f := newOpFixture(t)
	args := map[string]any{"body": "Why is the delay **capped**?\nIt was not before.", "target": map[string]any{"kind": "section", "id": "sec_retry", "title": "Retry policy"},
		"place": map[string]any{"path": "retry.go", "line": 9, "side": "RIGHT", "start_line": 4}, "head": "abcdef1"}
	for _, page := range []agentcontext.PageContext{{Page: agentcontext.PageInbox}, {Page: agentcontext.PagePulls}} {
		f.page(page)
		if _, failed := f.propose("propose_add_review_comment", args); failed == "" {
			t.Fatalf("proposed on %+v", page)
		}
	}
	f.page(pullPage)
	for _, refused := range []map[string]any{
		{"body": "hi \u202eevil"},
		{"body": "ok", "target": map[string]any{"kind": "section", "id": "s", "title": "a\nb"}},
		{"body": "ok", "target": map[string]any{"kind": "line", "id": "s", "title": "t"}},
	} {
		if _, failed := f.propose("propose_add_review_comment", refused); failed == "" {
			t.Errorf("%v was proposed", refused)
		}
	}
	receipt, failed := f.propose("propose_add_review_comment", args)
	if failed != "" {
		t.Fatal(failed)
	}
	p := f.proposal(receipt["id"].(string))
	for label, want := range map[string]agentcontext.SummaryField{
		"Comment":            {Value: "Why is the delay **capped**?\nIt was not before.", Quoted: true, Multiline: true, ByAgent: true},
		"About the section":  {Value: "Retry policy", Quoted: true, ByAgent: true},
		"On":                 {Value: "retry.go:4-9", Quoted: true, ByAgent: true},
		"Pull request title": {Value: "Add search", Quoted: true},
	} {
		got, ok := field(p, label)
		want.Label = label
		if !ok || got != want {
			t.Errorf("%s: %+v, want %+v", label, got, want)
		}
	}
	if record, _ := f.ui.Read("review", "owner/repo#7"); record.Revision != 0 {
		t.Fatalf("the review changed before the confirm: %s", record.Value)
	}
	// Another pull request's page does not run it.
	f.page(forkPullPage)
	if _, err := f.confirm(ctx, p.ID); !errors.Is(err, agentcontext.ErrNotCurrentPage) {
		t.Fatalf("confirmed on another page: %v", err)
	}
	f.page(pullPage)
	outcome, err := f.confirm(ctx, p.ID)
	if err != nil || !outcome.OK() {
		t.Fatalf("%+v %v", outcome, err)
	}
	record, err := f.ui.Read("review", "owner/repo#7")
	if err != nil {
		t.Fatal(err)
	}
	var review struct {
		Comments []struct {
			Target struct{ Kind, ID, Title string }
			Body   string
			Head   string
			Place  struct{ Path, Side string }
		}
	}
	if err := json.Unmarshal(record.Value, &review); err != nil || len(review.Comments) != 1 {
		t.Fatalf("%s %v", record.Value, err)
	}
	if c := review.Comments[0]; c.Body != args["body"] || c.Target.ID != "sec_retry" || c.Head != "abcdef1" || c.Place.Path != "retry.go" {
		t.Fatalf("added %+v", c)
	}
}

package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// takeAll takes every proposal and records the input of each save.
type takeAll struct {
	inputs []SaveInput
	fail   error
}

func (r *takeAll) Current(context.Context, PageContext, string) (json.RawMessage, string, error) {
	return json.RawMessage(`"current"`), "r1", nil
}
func (r *takeAll) Validate(context.Context, PageContext, Proposal) error { return nil }
func (r *takeAll) Apply(_ context.Context, _ PageContext, _ Proposal, _ string, input SaveInput) error {
	r.inputs = append(r.inputs, input)
	return r.fail
}

// Each propose tool records its page's target and the change asked for, and
// answers only the receipt.
func TestProposeToolsNameTheirTargets(t *testing.T) {
	rec := &takeAll{}
	proposers := map[string]Proposer{}
	for page := range proposePageTools {
		proposers[page] = rec
	}
	s := New(Options{API: http.NotFoundHandler(), Proposers: proposers})
	token, id := chat(t, s)
	settings := func(section string) PageContext { return PageContext{Page: PageSettings, Section: section} }
	repository := PageContext{Page: PageRepositories, Repository: "acme/web", Section: "prompts"}
	for _, tt := range []struct {
		page   PageContext
		tool   string
		args   map[string]any
		target string
		after  string
	}{
		{settings("general"), "propose_appearance", map[string]any{"theme": "dark"}, TargetAppearance, `{"theme":"dark"}`},
		{settings("general"), "propose_plan_usage", map[string]any{"provider": "codex", "shown": nil}, TargetPlanUsage + "codex", `{"shown":null}`},
		{settings("reviews"), "propose_reviews", map[string]any{"enabled": false, "model": "m"}, TargetReviews, `{"enabled":false,"model":"m"}`},
		{settings("reviews"), "propose_decision_capture", map[string]any{"effort": "low"}, TargetDecisions, `{"effort":"low"}`},
		{settings("fixes"), "propose_fixes", map[string]any{"backend": ""}, TargetFixes, `{"backend":""}`},
		{settings("computer"), "propose_time_limits", map[string]any{"run_time_limit_minutes": 5}, TargetTimeLimits, `{"run_time_limit_minutes":5}`},
		{settings("computer"), "propose_job_concurrency", map[string]any{"job_concurrency": 2}, TargetJobConcurrency, `{"job_concurrency":2}`},
		{settings("computer"), "propose_codex_approvals", map[string]any{"codex_approvals": false}, TargetCodexApprovals, `{"codex_approvals":false}`},
		{settings("computer"), "propose_agent_env", map[string]any{"name": "API_URL", "action": "add"}, TargetAgentEnv + "API_URL", `{"name":"API_URL","needs_value":true}`},
		{settings("computer"), "propose_agent_env", map[string]any{"name": "API_URL", "action": "remove"}, TargetAgentEnv + "API_URL", `{"name":"API_URL","remove":true}`},
		{settings("prompts"), "propose_prompt", map[string]any{"id": "fix", "text": "t"}, TargetPrompt + "fix", `{"text":"t"}`},
		{settings("prompts"), "propose_prompt_reset", map[string]any{"id": "fix"}, TargetPrompt + "fix", `{"text":null}`},
		{repository, "propose_prompt", map[string]any{"id": "fix", "text": "t", "repository": "ACME/web"}, "acme/web:prompt/fix", `{"text":"t"}`},
		{repository, "propose_prompt_reset", map[string]any{"id": "fix"}, "acme/web:prompt/fix", `{"text":null}`},
		{repository, "propose_repository_settings", map[string]any{"automatic": nil}, "acme/web:settings", `{"automatic":null}`},
		{PageContext{Page: PageInbox}, "propose_inbox", map[string]any{"name": "N"}, TargetNewInbox, `{"name":"N"}`},
		{PageContext{Page: PageInbox}, "propose_inbox", map[string]any{"id": strings.Repeat("a", 32), "badge": false}, TargetInbox + strings.Repeat("a", 32), `{"badge":false}`},
		{PageContext{Page: PageGroup, Group: "g1"}, "propose_group", map[string]any{"add": []int{3}}, TargetGroup + "g1", `{"add":[3]}`},
		{PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}, "propose_review_comment", map[string]any{"body": "b"}, "acme/web#12:review_comment", `{"body":"b"}`},
	} {
		setPage(t, s, id, tt.page)
		text, failed := data(t, s, token, tt.tool, tt.args)
		if failed {
			t.Errorf("%s: %s", tt.tool, text)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(text), &got); err != nil || len(got) != 4 || got["status"] != proposedStatus || strings.Contains(text, "current") {
			t.Errorf("%s answered %s", tt.tool, text)
		}
		list := s.Proposals.List(id)
		p := list[len(list)-1]
		if p.ID != got["id"] || p.Page != tt.page.Page || p.Target != tt.target || string(p.After) != tt.after || string(p.Before) != `"current"` {
			t.Errorf("%s recorded %s %s %s", tt.tool, p.Page, p.Target, p.After)
		}
		if repo, what, ok := ParseRepositoryTarget(p.Target); tt.page.Page == PageRepositories && (!ok || repo != "acme/web" || what == "") {
			t.Errorf("%s: %q", tt.tool, p.Target)
		}
	}
	if repo, n, ok := ParsePullRequestTarget(PullRequestTarget("acme/web", 12)); !ok || repo != "acme/web" || n != 12 {
		t.Error("a pull request target does not parse back")
	}
	for _, bad := range []string{"acme/web#12", "acme/web#0:review_comment", "#3:review_comment", "acme/web#x:review_comment"} {
		if _, _, ok := ParsePullRequestTarget(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	// A tool for another page is refused, and records nothing.
	before := len(s.Proposals.List(id))
	setPage(t, s, id, PageContext{Page: PageInbox})
	for tool, args := range map[string]map[string]any{
		"propose_appearance": {"theme": "dark"}, "propose_prompt": {"id": "fix", "text": "t"}, "propose_group": {"name": "x"},
		"propose_repository_settings": {"automatic": true}, "propose_review_comment": {"body": "b"},
	} {
		if text, failed := data(t, s, token, tool, args); !failed {
			t.Errorf("%s on the Inbox: %s", tool, text)
		}
	}
	setPage(t, s, id, PageContext{Page: PageSettings, Section: "prompts"})
	if text, failed := data(t, s, token, "propose_prompt", map[string]any{"id": "fix", "text": "t", "repository": "acme/web"}); !failed {
		t.Errorf("a repository's prompt on Settings: %s", text)
	}
	setPage(t, s, id, repository)
	if text, failed := data(t, s, token, "propose_prompt", map[string]any{"id": "fix", "text": "t", "repository": "acme/other"}); !failed {
		t.Errorf("another repository's prompt: %s", text)
	}
	if len(s.Proposals.List(id)) != before {
		t.Error("a refused proposal was recorded")
	}
	// An action sees none of them.
	actionToken, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 12})
	defer revoke()
	r := rpc(t, s, actionToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(string(r.Result), "propose_") {
		t.Error("an action lists a propose tool")
	}
}

// Save takes what the person adds and passes it to Apply alone: it is never
// kept in the proposal, printed or logged.
func TestSaveInputReachesApplyAlone(t *testing.T) {
	ctx := context.Background()
	rec := &takeAll{fail: errors.New("refused by the page")}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{PageSettings: rec}})
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageSettings, Section: "computer"})
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: TargetAgentEnv + "X", After: json.RawMessage(`{"name":"X","needs_value":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	secret := "TYPED_SECRET"
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{Value: &secret}); err == nil {
		t.Fatal("the page's refusal was lost")
	}
	if len(rec.inputs) != 1 || rec.inputs[0].Value == nil || *rec.inputs[0].Value != secret {
		t.Fatalf("Apply got %v", rec.inputs)
	}
	raw, _ := json.Marshal(s.Proposals.List(id))
	if strings.Contains(string(raw), secret) {
		t.Errorf("the proposal kept the value: %s", raw)
	}
	input := SaveInput{Value: &secret}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("save", "input", input)
	if out := fmt.Sprintf("%v %+v %#v %s", input, input, input, input) + logged.String(); strings.Contains(out, secret) {
		t.Errorf("a save input printed its value: %s", out)
	}
}

// A proposal is saved only while the person still has its page open,
// showing the same thing; the proposer then checks the target against the
// page as it is now.
func TestSaveRefusesAfterThePersonNavigated(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	threads := newFake("acme/web#12:thread-1", "open")
	if err := s.RegisterPage(PageSpec{Name: PagePullRequest, Proposer: threads}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	page := PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}
	setPage(t, s, id, page)
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PagePullRequest, Target: "acme/web#12:thread-1", After: json.RawMessage(`"resolved"`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []PageContext{{Page: PagePullRequest, Repository: "acme/web", PullRequest: 13}, {Page: PagePullRequest, Repository: "acme/other", PullRequest: 12}, {Page: PageInbox}} {
		setPage(t, s, id, other)
		if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, ErrNotCurrentPage) || threads.value("acme/web#12:thread-1") != "open" {
			t.Errorf("saved on %+v: %v", other, err)
		}
	}
	// The same pull request with other filters is the same page.
	setPage(t, s, id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12, Filters: map[string]string{"tab": "files"}})
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); err != nil || threads.value("acme/web#12:thread-1") != "resolved" {
		t.Fatal(err)
	}
}

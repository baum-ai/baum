package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var errNotOnPage = errors.New("the target is not on this page")

// fakeProposer edits string values by target, each with a revision that
// counts its changes. On a pull request's page a target belongs to the page
// when it starts with "owner/name#N:" for the pull request the page shows.
type fakeProposer struct {
	mu       sync.Mutex
	values   map[string]string
	revision map[string]int
	// gone lists targets that no longer belong to any page.
	gone       map[string]bool
	invalid    error
	onValidate func()
	onCurrent  func()
	// beforeWrite runs inside Apply between its reads and its write, as an
	// edit landing concurrently would.
	beforeWrite func()
}

func newFake(target, value string) *fakeProposer {
	return &fakeProposer{values: map[string]string{target: value}, revision: map[string]int{}, gone: map[string]bool{}}
}

func (f *fakeProposer) belongs(page PageContext, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone[target] {
		return errNotOnPage
	}
	if page.Page == PagePullRequest && !strings.HasPrefix(target, fmt.Sprintf("%s#%d:", page.Repository, page.PullRequest)) {
		return errNotOnPage
	}
	return nil
}

func (f *fakeProposer) Current(_ context.Context, page PageContext, target string) (json.RawMessage, string, error) {
	if f.onCurrent != nil {
		f.onCurrent()
	}
	if err := f.belongs(page, target); err != nil {
		return nil, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	value, _ := json.Marshal(f.values[target])
	return value, strconv.Itoa(f.revision[target]), nil
}

func (f *fakeProposer) Validate(_ context.Context, _ PageContext, _ Proposal) error {
	if f.onValidate != nil {
		f.onValidate()
	}
	return f.invalid
}

func (f *fakeProposer) Apply(_ context.Context, page PageContext, p Proposal, revision string, _ SaveInput) error {
	if err := f.belongs(page, p.Target); err != nil {
		return err
	}
	var value string
	if err := json.Unmarshal(p.After, &value); err != nil {
		return err
	}
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	// The comparison and the write are one step under the lock, as a
	// transaction makes them.
	f.mu.Lock()
	defer f.mu.Unlock()
	if strconv.Itoa(f.revision[p.Target]) != revision {
		return ErrStale
	}
	f.values[p.Target] = value
	f.revision[p.Target]++
	return nil
}

func (f *fakeProposer) set(target, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[target] = value
	f.revision[target]++
}

func (f *fakeProposer) value(target string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[target]
}

func TestProposalsChangeNothingUntilSaved(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	edit := Proposal{Page: PageSettings, Target: "review", Before: json.RawMessage(`"old"`), After: json.RawMessage(`"new"`)}
	if _, err := s.Proposals.Propose(ctx, id, edit); !errors.Is(err, ErrNotCurrentPage) {
		t.Fatal("proposed without a page:", err)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PageInbox}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Propose(ctx, id, edit); !errors.Is(err, ErrNotCurrentPage) {
		t.Fatal("proposed for a page that is not open:", err)
	}
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageInbox, Target: "x", After: json.RawMessage(`1`)}); !errors.Is(err, ErrNoProposer) {
		t.Fatal(err)
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`not json`)}); err == nil {
		t.Fatal("a proposal that is not JSON was recorded")
	}
	prompts.invalid = errors.New("prompt too long")
	if _, err := s.Proposals.Propose(ctx, id, edit); err != prompts.invalid {
		t.Fatal(err)
	}
	prompts.invalid = nil
	first, err := s.Proposals.Propose(ctx, id, edit)
	if err != nil || first.ID == "" || first.Context.Page != PageSettings {
		t.Fatal(first, err)
	}
	second, err := s.Proposals.Propose(ctx, id, edit)
	if err != nil {
		t.Fatal(err)
	}
	if prompts.value("review") != "old" || len(s.Proposals.List(id)) != 2 {
		t.Fatal("a proposal changed the target")
	}
	if err := s.Proposals.Save(ctx, "someone-else", first.ID, SaveInput{}); !errors.Is(err, ErrUnknownProposal) {
		t.Fatal("another consumer saved the proposal:", err)
	}
	if err := s.Proposals.Save(ctx, id, first.ID, SaveInput{}); err != nil {
		t.Fatal(err)
	}
	if prompts.value("review") != "new" || len(s.Proposals.List(id)) != 1 {
		t.Fatal("save did not apply the proposal")
	}
	// The second proposal was read at revision 0; the save moved it on.
	prompts.set("review", "edited by hand")
	if err := s.Proposals.Save(ctx, id, second.ID, SaveInput{}); !errors.Is(err, ErrStale) {
		t.Fatal("a stale proposal was saved:", err)
	}
	if prompts.value("review") != "edited by hand" || len(s.Proposals.List(id)) != 1 {
		t.Fatal("a stale save changed something")
	}
	if err := s.Proposals.Dismiss(id, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Proposals.Dismiss(id, second.ID); !errors.Is(err, ErrUnknownProposal) {
		t.Fatal(err)
	}
	if prompts.value("review") != "edited by hand" {
		t.Fatal("dismiss changed the target")
	}
}

func TestProposalRevisionComesFromTheServer(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	prompts.set("review", "newer")
	// The agent read "old" before the edit and claims a revision ahead of
	// the target's; neither is believed.
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", Before: json.RawMessage(`"old"`), After: json.RawMessage(`"mine"`), Revision: "99"}); !errors.Is(err, ErrNotRecorded) || strings.Contains(err.Error(), "newer") {
		t.Fatal("an outdated before was accepted:", err)
	}
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"mine"`), Revision: "99"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != "1" || string(p.Before) != `"newer"` {
		t.Fatalf("recorded revision %q before %s", p.Revision, p.Before)
	}
	prompts.set("review", "newest")
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, ErrStale) || prompts.value("review") != "newest" {
		t.Fatal("a proposal was saved over a newer edit:", err)
	}
}

func TestSaveRefusesAnEditThatLandsDuringTheWrite(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"proposed"`)})
	if err != nil {
		t.Fatal(err)
	}
	// Save's own read sees revision 0; the edit lands before Apply writes.
	prompts.beforeWrite = func() { prompts.set("review", "concurrent") }
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if prompts.value("review") != "concurrent" || len(s.Proposals.List(id)) != 1 {
		t.Fatal("the concurrent edit was overwritten")
	}
}

func TestProposalsBelongToWhatThePageShows(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	threads := newFake("acme/web#12:thread-1", "open")
	if err := s.RegisterPage(PageSpec{Name: PagePullRequest, Proposer: threads}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 12}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"acme/web#13:thread-1", "acme/other#12:thread-1", "thread-1"} {
		if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PagePullRequest, Target: target, After: json.RawMessage(`"resolved"`)}); !errors.Is(err, errNotOnPage) {
			t.Fatalf("%s: %v", target, err)
		}
	}
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PagePullRequest, Target: "acme/web#12:thread-1", After: json.RawMessage(`"resolved"`)})
	if err != nil || p.Context.Repository != "acme/web" || p.Context.PullRequest != 12 {
		t.Fatal(p, err)
	}
	// The target left the page before the user saved.
	threads.gone["acme/web#12:thread-1"] = true
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, errNotOnPage) || threads.value("acme/web#12:thread-1") != "open" {
		t.Fatal(err)
	}
}

func TestProposeThroughATool(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "current-secret-value")
	if err := s.RegisterTool(Tool{Name: "propose_test_prompt", Description: "Proposes a prompt.", Scope: ScopeRepository,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"before":{"type":"string"}}}`),
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct{ Before *string }
			_ = json.Unmarshal(args, &a)
			proposal := Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"proposed"`)}
			if a.Before != nil {
				proposal.Before, _ = json.Marshal(*a.Before)
			}
			receipt, err := c.Propose(ctx, proposal)
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(receipt)
			return string(out), err
		}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Tools: []string{"propose_test_prompt"}, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	token, id, revoke := mint(t, s, Consumer{Kind: Chat})
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	result := call(t, s, token, "propose_test_prompt", nil)
	data, _ := unwrap(t, result.Content[0].Text)
	var receipt Receipt
	if err := json.Unmarshal([]byte(data), &receipt); err != nil {
		t.Fatal(err, data)
	}
	proposalID := receipt.ID
	if result.IsError || len(s.Proposals.List(id)) != 1 || s.Proposals.List(id)[0].ID != proposalID || prompts.value("review") != "current-secret-value" {
		t.Fatalf("%+v", result)
	}
	// The person sees the current value; the agent never does, not even by
	// guessing it.
	if string(s.Proposals.List(id)[0].Before) != `"current-secret-value"` || strings.Contains(result.Content[0].Text, "current-secret-value") {
		t.Fatalf("the current value reached the agent: %s", result.Content[0].Text)
	}
	if r := call(t, s, token, "propose_test_prompt", map[string]string{"before": "a guess"}); !r.IsError || strings.Contains(r.Content[0].Text, "current-secret-value") || !strings.Contains(r.Content[0].Text, ErrNotRecorded.Error()) {
		t.Fatalf("a wrong guess: %+v", r)
	}
	// The tool is repository-scoped, so an action may call it; Propose
	// still refuses an action.
	actionToken, _, revokeAction := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 1})
	defer revokeAction()
	if r := call(t, s, actionToken, "propose_test_prompt", nil); !r.IsError || !strings.Contains(r.Content[0].Text, "only reads") {
		t.Fatal("an action proposed a change")
	}
	revoke()
	if len(s.Proposals.List(id)) != 0 {
		t.Fatal("a revoked consumer's proposals were kept")
	}
	if err := s.Proposals.Save(ctx, id, proposalID, SaveInput{}); !errors.Is(err, ErrUnknownProposal) || prompts.value("review") != "current-secret-value" {
		t.Fatal(err)
	}
}

func TestListedProposalsAreCopies(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: newFake("review", "old")}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings, Filters: map[string]string{"tab": "prompts"}, Focus: json.RawMessage(`{"k":1}`)}); err != nil {
		t.Fatal(err)
	}
	after := json.RawMessage(`"new"`)
	returned, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: after})
	if err != nil {
		t.Fatal(err)
	}
	after[1] = 'X' // the caller reuses its buffer
	for _, p := range []Proposal{returned, s.Proposals.List(id)[0]} {
		p.Context.Filters["tab"] = "changed"
		p.Context.Focus[1] = 'X'
		p.Before[1] = 'X'
		p.After[1] = 'X'
	}
	p := s.Proposals.List(id)[0]
	if p.Context.Filters["tab"] != "prompts" || string(p.Context.Focus) != `{"k":1}` || string(p.Before) != `"old"` || string(p.After) != `"new"` {
		t.Fatalf("a stored proposal was changed from outside: %+v", p)
	}
}

func TestSaveRacingARevoke(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id, revoke := mint(t, s, Consumer{Kind: Chat})
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"new"`)})
	if err != nil {
		t.Fatal(err)
	}
	// The token is revoked after Save found the proposal, before it writes.
	prompts.onCurrent = revoke
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); !errors.Is(err, ErrUnknownConsumer) || prompts.value("review") != "old" {
		t.Fatal("a revoked consumer's proposal was saved:", err)
	}
}

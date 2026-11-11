package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
)

// fakeProposer edits one value per target, with a revision that counts its
// changes.
type fakeProposer struct {
	mu       sync.Mutex
	values   map[string]string
	revision map[string]int
	invalid  error
}

func (f *fakeProposer) Validate(_ context.Context, p Proposal) error { return f.invalid }
func (f *fakeProposer) Revision(_ context.Context, target string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strconv.Itoa(f.revision[target]), nil
}
func (f *fakeProposer) Apply(_ context.Context, p Proposal) error {
	var value string
	if err := json.Unmarshal(p.After, &value); err != nil {
		return err
	}
	f.set(p.Target, value)
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
	prompts := &fakeProposer{values: map[string]string{"review": "old"}, revision: map[string]int{}}
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id := chat(t, s)
	edit := Proposal{Page: PageSettings, Target: "review", Before: json.RawMessage(`"old"`), After: json.RawMessage(`"new"`), Revision: "0"}
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
	if err != nil || first.ID == "" {
		t.Fatal(first, err)
	}
	second, err := s.Proposals.Propose(ctx, id, edit)
	if err != nil {
		t.Fatal(err)
	}
	if prompts.value("review") != "old" || len(s.Proposals.List(id)) != 2 {
		t.Fatal("a proposal changed the target")
	}
	if err := s.Proposals.Save(ctx, "someone-else", first.ID); !errors.Is(err, ErrUnknownProposal) {
		t.Fatal("another consumer saved the proposal:", err)
	}
	if err := s.Proposals.Save(ctx, id, first.ID); err != nil {
		t.Fatal(err)
	}
	if prompts.value("review") != "new" || len(s.Proposals.List(id)) != 1 {
		t.Fatal("save did not apply the proposal")
	}
	// The second proposal was read at revision 0; the save moved it on.
	if err := s.Proposals.Save(ctx, id, second.ID); !errors.Is(err, ErrStale) {
		t.Fatal("a stale proposal was saved:", err)
	}
	prompts.set("review", "edited by hand")
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

func TestProposeThroughATool(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := &fakeProposer{values: map[string]string{}, revision: map[string]int{}}
	if err := s.RegisterTool(Tool{Name: "propose_prompt", Description: "Proposes a prompt.", Call: func(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
		p, err := c.Propose(ctx, Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"proposed"`), Revision: "0"})
		return p.ID, err
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Tools: []string{"propose_prompt"}, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	token, id, revoke := s.Registry.Mint(Consumer{Kind: Chat})
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	result := call(t, s, token, "propose_prompt", nil)
	proposalID, _ := unwrap(t, result.Content[0].Text)
	if result.IsError || len(s.Proposals.List(id)) != 1 || s.Proposals.List(id)[0].ID != proposalID || prompts.value("review") != "" {
		t.Fatalf("%+v", result)
	}
	actionToken, _, revokeAction := s.Registry.Mint(Consumer{Kind: Action, Repository: "acme/web", PullRequest: 1})
	defer revokeAction()
	if r := call(t, s, actionToken, "propose_prompt", nil); !r.IsError {
		t.Fatal("an action proposed a change")
	}
	revoke()
	if len(s.Proposals.List(id)) != 0 {
		t.Fatal("a revoked consumer's proposals were kept")
	}
	if err := s.Proposals.Save(ctx, id, proposalID); !errors.Is(err, ErrUnknownProposal) || prompts.value("review") != "" {
		t.Fatal(err)
	}
}

package agentcontext

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
)

// OnChange hears of every change to a consumer's open proposals, after it,
// and of nothing that changes none.
func TestOnChangeFollowsTheOpenProposals(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	prompts := newFake("review", "old")
	if err := s.RegisterPage(PageSpec{Name: PageSettings, Proposer: prompts}); err != nil {
		t.Fatal(err)
	}
	_, id, revoke := mint(t, s, Consumer{Kind: Chat})
	var mu sync.Mutex
	var heard []string
	s.Proposals.OnChange(func(consumer string) {
		// Called without the lock: reading the proposals here must not
		// deadlock.
		n := len(s.Proposals.List(consumer))
		mu.Lock()
		heard = append(heard, consumer+":"+string(rune('0'+n)))
		mu.Unlock()
	})
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := heard
		heard = nil
		return out
	}
	if err := s.Registry.SetPage(id, PageContext{Page: PageSettings}); err != nil {
		t.Fatal(err)
	}
	edit := Proposal{Page: PageSettings, Target: "review", After: json.RawMessage(`"new"`)}
	first, err := s.Proposals.Propose(ctx, id, edit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Proposals.Propose(ctx, id, edit)
	if err != nil {
		t.Fatal(err)
	}
	if got := took(); !slices.Equal(got, []string{id + ":1", id + ":2"}) {
		t.Fatalf("after proposing: %q", got)
	}
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageInbox, After: json.RawMessage(`1`)}); err == nil {
		t.Fatal("a refused proposal was recorded")
	}
	if err := s.Proposals.Save(ctx, id, first.ID, SaveInput{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Proposals.Save(ctx, id, second.ID, SaveInput{}); err == nil {
		t.Fatal("a stale proposal was saved")
	}
	if got := took(); !slices.Equal(got, []string{id + ":1"}) {
		t.Fatalf("after saving: %q", got)
	}
	if err := s.Proposals.Dismiss(id, second.ID); err != nil {
		t.Fatal(err)
	}
	if got := took(); !slices.Equal(got, []string{id + ":0"}) {
		t.Fatalf("after dismissing: %q", got)
	}
	if _, err := s.Proposals.Propose(ctx, id, edit); err != nil {
		t.Fatal(err)
	}
	took()
	revoke()
	if got := took(); !slices.Equal(got, []string{id + ":0"}) {
		t.Fatalf("after revoking: %q", got)
	}
}

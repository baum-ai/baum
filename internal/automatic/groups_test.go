package automatic

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/reviewgroups"
)

// withGroups gives the harness a group store with one stack group of #1 and
// #2 and one group a person made of #3 and #4.
func withGroups(t *testing.T, h *harness) (stack, mine reviewgroups.Group) {
	t.Helper()
	groups, err := reviewgroups.Open(filepath.Join(h.dir, "groups.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = groups.Close() })
	ctx := context.Background()
	stacked := []discovery.PullRequest{{Number: 1, Title: "Stack", HeadRef: "a", BaseRef: "main"}, {Number: 2, HeadRef: "b", BaseRef: "a"}}
	if _, err := groups.SyncStacks(ctx, "org/repo", stacked, nil); err != nil {
		t.Fatal(err)
	}
	if mine, err = groups.Create(ctx, "org/repo", "Mine", []int{3, 4}); err != nil {
		t.Fatal(err)
	}
	list, err := groups.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range list {
		if g.Stack {
			stack = g
		}
	}
	h.exec.groups = groups
	h.scheduler.Groups = groups
	return stack, mine
}

// A stack has an overview page instead of a combined review: its members'
// own reviews follow their heads, and nothing is queued for the stack.
func TestStackGroupGetsNoAutomaticCombinedReview(t *testing.T) {
	h := newHarness(t)
	withGroups(t, h)
	open := []pr{{number: 1, head: "a1", authored: true}, {number: 2, head: "b1", authored: true}}
	h.observe(open...)
	open[0].head, open[1].head = "a2", "b2"
	h.observe(open...)
	h.pass()
	members := []string{"org/repo#1@a2:new_head", "org/repo#2@b2:new_head"}
	h.want(members...)
	h.waitIdle()
	h.reviewed[1], h.reviewed[2] = "a2", "b2"
	h.pass()
	h.pass()
	h.want(members...)
}

package automatic

import (
	"context"
	"path/filepath"
	"testing"

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
	if _, err := groups.SyncStacks(ctx, "org/repo", []reviewgroups.Stack{{Members: []int{1, 2}, Name: "Stack"}}); err != nil {
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

func TestStackGroupReviewFollowsItsMembersReviews(t *testing.T) {
	h := newHarness(t)
	withGroups(t, h)
	open := []pr{{number: 1, head: "a1", authored: true}, {number: 2, head: "b1", authored: true}, {number: 3, head: "c1", authored: true}, {number: 4, head: "d1", authored: true}}
	h.observe(open...)
	for i := range open {
		open[i].head = open[i].head[:1] + "2"
	}
	h.observe(open...)
	h.pass()
	members := []string{"org/repo#1@a2:new_head", "org/repo#2@b2:new_head", "org/repo#3@c2:new_head", "org/repo#4@d2:new_head"}
	h.want(members...)
	h.waitIdle()
	// Only one member's review is saved: no group review yet.
	h.reviewed[1] = "a2"
	h.pass()
	h.want(members...)
	// Every member reviewed at its head: the stack's review, once. The
	// person's own group is generated only on request.
	h.reviewed[2], h.reviewed[3], h.reviewed[4] = "b2", "c2", "d2"
	h.pass()
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed")...)
	h.waitIdle()
	h.restart()
	h.scheduler.Groups = h.exec.groups
	h.pass()
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed")...)
	// A member moves on: its own review first, then the stack's again.
	open[1].head = "b3"
	h.observe(open...)
	h.pass()
	members = append(members, "org/repo#2@b3:new_head")
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed")...)
	h.waitIdle()
	h.reviewed[2] = "b3"
	h.pass()
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed", "org/repo#group@1@a2,2@b3:stack_reviewed")...)
}

func TestStackGroupReviewWaitsForEveryMemberToBeEligible(t *testing.T) {
	h := newHarness(t)
	withGroups(t, h)
	// #2 is a draft: it gets no automatic review, so neither does the stack,
	// even when a person reviewed #2 meanwhile.
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true, draft: true})
	h.observe(pr{number: 1, head: "a2", authored: true}, pr{number: 2, head: "b2", authored: true, draft: true})
	h.pass()
	h.want("org/repo#1@a2:new_head")
	h.waitIdle()
	h.reviewed[1], h.reviewed[2] = "a2", "b2"
	h.pass()
	h.want("org/repo#1@a2:new_head")
}

// A stack whose members' current heads automatic generation did not handle
// (reviewed before the baseline) is not caught up, as pull requests open at
// the baseline are not.
func TestStackGroupReviewedBeforeTheBaselineIsNotCaughtUp(t *testing.T) {
	h := newHarness(t)
	withGroups(t, h)
	h.reviewed[1], h.reviewed[2] = "a1", "b1"
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	h.pass()
	h.pass()
	h.want()
	// A new head afterwards is followed as usual.
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b2", authored: true})
	h.pass()
	h.want("org/repo#2@b2:new_head")
	h.waitIdle()
	h.reviewed[2] = "b2"
	h.pass()
	h.want("org/repo#2@b2:new_head", "org/repo#group@1@a1,2@b2:stack_reviewed")
}

func TestCoveredIgnoresMembersThatLeft(t *testing.T) {
	reviewed := []reviewgroups.ReviewedMember{{Number: 1, HeadSHA: "a"}, {Number: 2, HeadSHA: "b"}, {Number: 3, HeadSHA: "c"}}
	if !covered(reviewed, map[int]string{2: "b", 3: "c"}) {
		t.Fatal("the merged bottom called for another review")
	}
	if covered(reviewed, map[int]string{2: "b", 3: "c2"}) || covered(reviewed, map[int]string{2: "b", 4: "d"}) || covered(nil, map[int]string{}) {
		t.Fatal("a moved or new member was covered")
	}
}

// A stack review a daemon stop cut short is retried once, labelled as a retry.
func TestInterruptedStackReviewIsRetriedOnce(t *testing.T) {
	h := newHarness(t)
	h.exec.block = make(chan struct{})
	h.exec.started = make(chan struct{}, 10)
	withGroups(t, h)
	h.observe(pr{number: 1, head: "a1", authored: true}, pr{number: 2, head: "b1", authored: true})
	snapshot := []pr{{number: 1, head: "a2", authored: true}, {number: 2, head: "b2", authored: true}}
	h.observe(snapshot...)
	h.pass()
	for range 2 {
		<-h.exec.started
		h.exec.block <- struct{}{}
	}
	h.waitIdle()
	h.reviewed[1], h.reviewed[2] = "a2", "b2"
	h.pass()
	<-h.exec.started
	h.stop()
	h.scheduler.Groups = h.exec.groups
	h.observe(snapshot...)
	h.pass()
	members := []string{"org/repo#1@a2:new_head", "org/repo#2@b2:new_head"}
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed", "org/repo#group@1@a2,2@b2:retry")...)
	<-h.exec.started
	h.stop()
	h.scheduler.Groups = h.exec.groups
	h.observe(snapshot...)
	h.pass()
	h.want(append(members, "org/repo#group@1@a2,2@b2:stack_reviewed", "org/repo#group@1@a2,2@b2:retry")...)
	close(h.exec.block)
}

package stacks

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
)

// memory holds stacks the way the tables do and recomputes them the way the
// repository does: it loads the stacks that have not ended or have an open
// current member, and the open pull requests plus those stacks' members.
type memory struct {
	t      *testing.T
	stacks []*memoryStack
}

type memoryStack struct {
	id      string
	created time.Time
	ended   bool
	current map[int]*int
	left    map[int]bool
}

func newMemory(t *testing.T) *memory { return &memory{t: t} }

func (m *memory) recompute(pulls ...Candidate) Changes {
	m.t.Helper()
	changes := m.plan(pulls)
	m.apply(changes)
	if again := m.plan(pulls); !again.Empty() {
		m.t.Fatalf("second recompute changed %+v; want nothing", again)
	}
	return changes
}

func (m *memory) plan(pulls []Candidate) Changes {
	isOpen := map[int]bool{}
	for _, pull := range pulls {
		isOpen[pull.Number] = pull.Open
	}
	var stored []StoredStack
	members := map[int]bool{}
	for _, stack := range m.stacks {
		load := !stack.ended
		for number := range stack.current {
			load = load || isOpen[number]
		}
		if !load {
			continue
		}
		storedStack := StoredStack{ID: stack.id, CreatedAt: stack.created, Ended: stack.ended}
		for _, number := range slices.Sorted(maps.Keys(stack.current)) {
			storedStack.Members = append(storedStack.Members, Placement{Number: number, Parent: stack.current[number]})
			members[number] = true
		}
		stored = append(stored, storedStack)
	}
	var candidates []Candidate
	for _, pull := range pulls {
		if pull.Open || members[pull.Number] {
			candidates = append(candidates, pull)
		}
	}
	return Plan(candidates, stored)
}

func (m *memory) apply(changes Changes) {
	m.t.Helper()
	for _, change := range changes.Updated {
		stack := m.stack(change.ID)
		for _, number := range change.Leave {
			delete(stack.current, number)
			stack.left[number] = true
		}
	}
	for _, created := range changes.Created {
		stack := &memoryStack{
			id: fmt.Sprintf("s%d", len(m.stacks)+1), created: time.Unix(int64(len(m.stacks)), 0),
			current: map[int]*int{}, left: map[int]bool{},
		}
		for _, member := range created.Members {
			stack.current[member.Number] = member.Parent
		}
		m.stacks = append(m.stacks, stack)
	}
	for _, change := range changes.Updated {
		stack := m.stack(change.ID)
		for _, member := range change.Join {
			stack.current[member.Number] = member.Parent
			delete(stack.left, member.Number)
		}
		if change.Ended != nil {
			stack.ended = *change.Ended
		}
	}
	seen := map[int]string{}
	for _, stack := range m.stacks {
		for number := range stack.current {
			if other, ok := seen[number]; ok {
				m.t.Fatalf("#%d is a current member of %s and %s", number, other, stack.id)
			}
			seen[number] = stack.id
		}
	}
}

func (m *memory) stack(id string) *memoryStack {
	m.t.Helper()
	for _, stack := range m.stacks {
		if stack.id == id {
			return stack
		}
	}
	m.t.Fatalf("no stack %s", id)
	return nil
}

// expect checks a stack's current members and their parents (0 for the
// root), whether it ended and which members left.
func (m *memory) expect(id string, parents map[int]int, ended bool, left ...int) {
	m.t.Helper()
	stack := m.stack(id)
	got := map[int]int{}
	for number, parent := range stack.current {
		got[number] = 0
		if parent != nil {
			got[number] = *parent
		}
	}
	if !maps.Equal(got, parents) {
		m.t.Fatalf("%s members = %v; want %v", id, got, parents)
	}
	if stack.ended != ended {
		m.t.Fatalf("%s ended = %t; want %t", id, stack.ended, ended)
	}
	if gotLeft := slices.Sorted(maps.Keys(stack.left)); !slices.Equal(gotLeft, left) {
		m.t.Fatalf("%s left = %v; want %v", id, gotLeft, left)
	}
}

func (m *memory) expectStacks(count int) {
	m.t.Helper()
	if len(m.stacks) != count {
		m.t.Fatalf("%d stacks; want %d", len(m.stacks), count)
	}
}

func open(number int, base, head string) Candidate {
	return Candidate{Number: number, Open: true, BaseRef: base, HeadRef: head}
}

func closed(pull Candidate) Candidate {
	pull.Open = false
	return pull
}

func merged(pull Candidate) Candidate {
	pull.Open, pull.Merged = false, true
	return pull
}

func retarget(pull Candidate, base string) Candidate {
	pull.BaseRef = base
	return pull
}

func TestPlanChainsPullRequestsByBranch(t *testing.T) {
	m := newMemory(t)
	m.recompute(open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-b", "feat-c"), open(4, "main", "other"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2}, false)
}

func TestPlanLeavesOutForks(t *testing.T) {
	m := newMemory(t)
	fork := open(2, "feat-a", "feat-b")
	fork.CrossRepository = true
	m.recompute(open(1, "main", "feat-a"), fork)
	m.expectStacks(0)

	m.recompute(open(1, "main", "feat-a"), fork, open(3, "feat-a", "feat-c"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 3: 1}, false)

	// A fork's head branch is in another repository, so nothing stacks on it.
	m = newMemory(t)
	fork = open(1, "main", "feat-a")
	fork.CrossRepository = true
	m.recompute(fork, open(2, "feat-a", "feat-b"))
	m.expectStacks(0)
}

func TestPlanFormsNoStackOnAPullRequestMergedBeforeDetection(t *testing.T) {
	m := newMemory(t)
	m.recompute(closed(open(1, "main", "feat-a")), open(2, "feat-a", "feat-b"))
	m.expectStacks(0)
}

func TestPlanKeepsTheStackWhenTheBottomMergesAndTheNextIsRetargeted(t *testing.T) {
	m := newMemory(t)
	a, b, c := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-b", "feat-c")
	m.recompute(a, b, c)
	if changes := m.recompute(merged(a), b, c); !changes.Empty() {
		t.Fatalf("merge changed %+v; want nothing", changes)
	}
	if changes := m.recompute(merged(a), retarget(b, "main"), c); !changes.Empty() {
		t.Fatalf("retarget after merge changed %+v; want nothing", changes)
	}
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2}, false)

	// A new pull request on the merged bottom's branch joins the stack.
	m.recompute(merged(a), retarget(b, "main"), c, open(4, "feat-a", "feat-d"))
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2, 4: 1}, false)
}

func TestPlanKeepsTheStackWhenAMergeRetargetsSeveralDescendants(t *testing.T) {
	for _, descendantFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("descendantFirst=%t", descendantFirst), func(t *testing.T) {
			m := newMemory(t)
			a, b, c := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-b", "feat-c")
			m.recompute(a, b, c)
			a = merged(a)
			if descendantFirst {
				c = retarget(c, "main")
			} else {
				b = retarget(b, "main")
			}
			if changes := m.recompute(a, b, c); !changes.Empty() {
				t.Fatalf("first retarget changed %+v; want nothing", changes)
			}
			if changes := m.recompute(a, retarget(b, "main"), retarget(c, "main")); !changes.Empty() {
				t.Fatalf("remaining retarget changed %+v; want nothing", changes)
			}
			m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2}, false)
		})
	}
}

func TestPlanEndsAStackOnceEveryMemberIsClosedAndReopensIt(t *testing.T) {
	m := newMemory(t)
	a, b := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b")
	m.recompute(a, b)
	m.recompute(merged(a), b)
	m.expect("s1", map[int]int{1: 0, 2: 1}, false)
	m.recompute(merged(a), closed(retarget(b, "main")))
	m.expect("s1", map[int]int{1: 0, 2: 1}, true)
	// An ended stack is not loaded again until a member reopens.
	m.recompute(merged(a), closed(retarget(b, "main")), open(3, "feat-b", "feat-c"))
	m.expectStacks(1)
	m.recompute(merged(a), retarget(b, "main"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1}, false)
}

// Only GitHub's own retarget after a merge keeps the parent: the next pull
// request moved onto the merged parent's base.
func TestPlanKeepsAMergedParentOnlyForGitHubsRetarget(t *testing.T) {
	a, b, c := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-b", "feat-c")

	// The parent was closed without merging and B moved to main.
	m := newMemory(t)
	m.recompute(a, b, c)
	m.recompute(closed(a), retarget(b, "main"), c)
	m.expectStacks(1)
	m.expect("s1", map[int]int{2: 0, 3: 2}, false, 1)

	// The parent merged, but B moved to a branch other than the parent's base.
	m = newMemory(t)
	m.recompute(a, b, c)
	m.recompute(merged(a), retarget(b, "release"), c)
	m.expectStacks(1)
	m.expect("s1", map[int]int{2: 0, 3: 2}, false, 1)

	// The same with B a leaf: B leaves and A, merged, is left on its own.
	m = newMemory(t)
	m.recompute(a, b)
	m.recompute(merged(a), retarget(b, "release"))
	m.expect("s1", map[int]int{}, true, 1, 2)

	// The larger-part rule still applies: B's part is larger and keeps the
	// id; the rest, with one open pull request, forms no stack.
	m = newMemory(t)
	z, d := open(4, "feat-a", "feat-z"), open(5, "feat-c", "feat-d")
	m.recompute(a, b, c, z, d)
	m.recompute(merged(a), retarget(b, "release"), c, z, d)
	m.expectStacks(1)
	m.expect("s1", map[int]int{2: 0, 3: 2, 5: 3}, false, 1, 4)
}

// A pull request is stacked only on one by the same author: the release
// manager's develop-into-main pull request gathers no one's feature pull
// requests based on develop, while one author's own stack still forms.
func TestPlanStacksOnlyOnTheSameAuthor(t *testing.T) {
	by := func(author string, pull Candidate) Candidate {
		pull.Author = author
		return pull
	}
	m := newMemory(t)
	m.recompute(
		by("releaser", open(1, "main", "develop")),
		by("ana", open(2, "develop", "feat-a")), by("ben", open(3, "develop", "feat-b")),
		by("Ana", open(4, "feat-a", "feat-a2")),
	)
	m.expectStacks(1)
	m.expect("s1", map[int]int{2: 0, 4: 2}, false)
}

func TestPlanGivesBranchingStacksOneTree(t *testing.T) {
	m := newMemory(t)
	m.recompute(open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-a", "feat-b2"), open(4, "feat-b2", "feat-c"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 1, 4: 3}, false)
}

func TestPlanRecordsAMemberRetargetedElsewhereAsLeft(t *testing.T) {
	m := newMemory(t)
	a, b, c := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b"), open(3, "feat-b", "feat-c")
	m.recompute(a, b, c)
	m.recompute(a, b, retarget(c, "main"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1}, false, 3)

	// Retargeted back, it joins again.
	m.recompute(a, b, c)
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2}, false)
}

func TestPlanEndsAStackLeftWithOneMember(t *testing.T) {
	m := newMemory(t)
	a, b := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b")
	m.recompute(a, b)
	m.recompute(a, retarget(b, "main"))
	m.expect("s1", map[int]int{}, true, 1, 2)
	// Its old members can stack again, as a new stack.
	m.recompute(a, b)
	m.expectStacks(2)
	m.expect("s2", map[int]int{1: 0, 2: 1}, false)
}

func TestPlanSplitKeepsTheIDOnTheLargerPart(t *testing.T) {
	m := newMemory(t)
	pulls := []Candidate{
		open(1, "main", "f1"), open(2, "f1", "f2"), open(3, "f2", "f3"), open(4, "f3", "f4"), open(5, "f4", "f5"),
	}
	m.recompute(pulls...)
	pulls[1] = retarget(pulls[1], "main")
	m.recompute(pulls...)
	m.expectStacks(1)
	m.expect("s1", map[int]int{2: 0, 3: 2, 4: 3, 5: 4}, false, 1)

	// Equal parts: the part with the lowest number keeps the id.
	m = newMemory(t)
	pulls = []Candidate{open(1, "main", "f1"), open(2, "f1", "f2"), open(3, "f2", "f3"), open(4, "f3", "f4")}
	m.recompute(pulls...)
	pulls[2] = retarget(pulls[2], "main")
	m.recompute(pulls...)
	m.expectStacks(2)
	m.expect("s1", map[int]int{1: 0, 2: 1}, false, 3, 4)
	m.expect("s2", map[int]int{3: 0, 4: 3}, false)
}

func TestPlanJoinKeepsTheOlderID(t *testing.T) {
	m := newMemory(t)
	a, b := open(1, "main", "feat-a"), open(2, "feat-a", "feat-b")
	m.recompute(a, b)
	c, d, e := open(3, "main", "feat-c"), open(4, "feat-c", "feat-d"), open(5, "feat-d", "feat-e")
	m.recompute(a, b, c, d, e)
	m.expectStacks(2)
	// The younger stack is larger, and still the older id survives.
	m.recompute(a, b, retarget(c, "feat-b"), d, e)
	m.expectStacks(2)
	m.expect("s1", map[int]int{1: 0, 2: 1, 3: 2, 4: 3, 5: 4}, false)
	m.expect("s2", map[int]int{}, true, 3, 4, 5)
}

func TestPlanBreaksBranchCycles(t *testing.T) {
	m := newMemory(t)
	m.recompute(open(1, "feat-b", "feat-a"), open(2, "feat-a", "feat-b"))
	m.expectStacks(1)
	m.expect("s1", map[int]int{1: 0, 2: 1}, false)
}

func TestPlanPrefersAnOpenParentAmongPullRequestsSharingAHeadBranch(t *testing.T) {
	m := newMemory(t)
	// #1 and #2 share the head branch feat-a: GitHub allows one open pull
	// request per head and base, not per head.
	m.recompute(open(1, "main", "feat-a"), open(2, "release", "feat-a"), open(3, "feat-a", "feat-b"))
	m.expect("s1", map[int]int{1: 0, 3: 1}, false)
}

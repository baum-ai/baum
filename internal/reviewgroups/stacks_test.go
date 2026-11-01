package reviewgroups

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/discovery"
)

func open(number int, head, base string) discovery.PullRequest {
	return discovery.PullRequest{Number: number, HeadRef: head, BaseRef: base, Title: "PR " + head, Author: "dev"}
}

func landedNumbers(g Group) []int {
	var out []int
	for _, m := range g.Landed {
		out = append(out, m.Number)
	}
	return out
}

func TestStackNameIsTheBottomTitleAsReviewTogetherSuggests(t *testing.T) {
	long := open(1, "a", "main")
	long.Title = strings.Repeat("word ", 40) + "\tend"
	if name, err := cleanName(stackName(long)); err != nil || len([]rune(name)) > MaxNameLength || !strings.HasPrefix(name, "word word") {
		t.Fatalf("name %q %v", name, err)
	}
	untitled := open(4, "a", "main")
	untitled.Title = " \n"
	if name := stackName(untitled); name != "#4" {
		t.Fatalf("name %q", name)
	}
}

func TestSyncStacksFollowsTheStackAndLeavesThePersonsGroups(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	sync := func(prs ...discovery.PullRequest) bool {
		t.Helper()
		changed, err := s.SyncStacks(ctx, "Acme/Demo", prs, nil)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	groups := func() []Group {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	// main <- a (#1) <- b (#2) <- c (#3), and an unstacked #9.
	if !sync(open(3, "c", "b"), open(1, "a", "main"), open(2, "b", "a"), open(9, "x", "main")) {
		t.Fatal("a new stack changed nothing")
	}
	list := groups()
	if len(list) != 1 || !list[0].Stack || list[0].Name != "PR a" || list[0].Repository != "acme/demo" || !slices.Equal(list[0].Members, []int{1, 2, 3}) {
		t.Fatalf("groups %+v", list)
	}
	id := list[0].ID
	if sync(open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b")) {
		t.Fatal("the same stack changed something")
	}
	// The bottom merged (#2 retargeted to main) and #4 was added on top: same
	// group, new members, same name; #1 stays, landed.
	if !sync(open(2, "b", "main"), open(3, "c", "b"), open(4, "d", "c")) {
		t.Fatal("new members changed nothing")
	}
	if list = groups(); len(list) != 1 || list[0].ID != id || list[0].Name != "PR a" || !slices.Equal(list[0].Members, []int{2, 3, 4}) || !slices.Equal(landedNumbers(list[0]), []int{1}) {
		t.Fatalf("groups %+v", list)
	}
	// The last open member merges too: the stack is gone, the group is
	// dissolved, and its pull requests may form a stack group again.
	allGone := []discovery.Observation{{Number: 1, State: "MERGED"}, {Number: 2, State: "CLOSED"}, {Number: 3, State: "CLOSED"}, {Number: 4, State: "MERGED"}}
	if changed, err := s.SyncStacks(ctx, "Acme/Demo", nil, allGone); err != nil || !changed || len(groups()) != 0 {
		t.Fatalf("dissolved %+v %v %v", groups(), changed, err)
	}
	sync(open(3, "c", "main"), open(4, "d", "c"))
	if list = groups(); len(list) != 1 || list[0].ID == id {
		t.Fatalf("groups %+v", list)
	}
	// The person deletes it: its pull requests take no part again.
	if err := s.Delete(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if sync(open(3, "c", "main"), open(4, "d", "c"), open(5, "e", "d")) || len(groups()) != 0 {
		t.Fatalf("a deleted stack group came back: %+v", groups())
	}
	// A pull request in the person's own group takes no part; another
	// repository's groups do not matter.
	if _, err := s.Create(ctx, "acme/demo", "Mine", []int{7, 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "acme/other", "Elsewhere", []int{10, 11}); err != nil {
		t.Fatal(err)
	}
	sync(open(8, "h", "main"), open(9, "i", "h"), open(10, "j", "main"), open(11, "k", "j"))
	var stack Group
	for _, g := range groups() {
		if g.Stack {
			stack = g
		}
	}
	if len(groups()) != 3 || !slices.Equal(stack.Members, []int{10, 11}) {
		t.Fatalf("groups %+v", groups())
	}
	// Renaming makes it the person's: sync no longer changes or dissolves it.
	name := "Renamed"
	if g, err := s.Update(ctx, stack.ID, &name, nil); err != nil || g.Stack {
		t.Fatalf("renamed %+v %v", g, err)
	}
	if sync() || len(groups()) != 3 {
		t.Fatalf("a renamed stack group was dissolved: %+v", groups())
	}
}

func TestSyncStacksLeavesOutStacksPastMaxMembers(t *testing.T) {
	s := openStore(t)
	prs := []discovery.PullRequest{open(1, "b1", "main")}
	for i := 2; i <= MaxMembers+1; i++ {
		prs = append(prs, open(i, "b"+string(rune('0'+i)), "b"+string(rune('0'+i-1))))
	}
	if changed, err := s.SyncStacks(context.Background(), "acme/demo", prs, nil); err != nil || changed {
		t.Fatalf("a stack of %d: %v %v", len(prs), changed, err)
	}
}

func TestSyncStacksKeepsMergedAndClosedMembersInPlace(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	sync := func(observations []discovery.Observation, prs ...discovery.PullRequest) bool {
		t.Helper()
		changed, err := s.SyncStacks(ctx, "acme/demo", prs, observations)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	only := func() Group {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("groups %+v %v", list, err)
		}
		return list[0]
	}
	observed := func(states ...any) []discovery.Observation {
		var out []discovery.Observation
		for i := 0; i < len(states); i += 2 {
			out = append(out, discovery.Observation{Number: states[i].(int), State: states[i+1].(string)})
		}
		return out
	}
	// main <- a (#1) <- b (#2) <- c (#3) and d (#4) also on b.
	sync(nil, open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b"), open(4, "d", "b"))
	id := only().ID

	// #1 merges and leaves the open list: it stays, as merged, and the open
	// members keep the group (membership is not recomputed from open only).
	if !sync(observed(1, "MERGED"), open(2, "b", "main"), open(3, "c", "b"), open(4, "d", "b")) {
		t.Fatal("a merge changed nothing")
	}
	if g := only(); g.ID != id || !slices.Equal(g.Members, []int{2, 3, 4}) || !slices.Equal(g.Landed, []LandedMember{{1, StateMerged}}) {
		t.Fatalf("after #1 merged %+v", g)
	}
	if sync(observed(1, "MERGED"), open(2, "b", "main"), open(3, "c", "b"), open(4, "d", "b")) {
		t.Fatal("the same state changed something")
	}
	// #2 merges too and its children are retargeted: still one group.
	sync(observed(1, "MERGED", 2, "MERGED"), open(3, "c", "main"), open(4, "d", "main"))
	if g := only(); g.ID != id || !slices.Equal(g.Members, []int{3, 4}) || !slices.Equal(landedNumbers(g), []int{1, 2}) {
		t.Fatalf("after #2 merged %+v", g)
	}
	// A member not known to have merged yet is kept, without a state, and
	// keeps the state it had once known.
	sync(nil, open(3, "c", "main"), open(4, "d", "main"))
	if g := only(); g.ID != id || !slices.Equal(g.Landed, []LandedMember{{1, StateMerged}, {2, StateMerged}}) {
		t.Fatalf("unobserved members %+v", g)
	}
	// #4 closes without merging: it stays in place, as closed, not merged.
	closedFour := observed(1, "MERGED", 2, "MERGED", 4, "CLOSED")
	sync(closedFour, open(3, "c", "main"))
	if g := only(); g.ID != id || !slices.Equal(g.Members, []int{3}) || !slices.Equal(g.Landed, []LandedMember{{1, StateMerged}, {2, StateMerged}, {4, StateClosed}}) {
		t.Fatalf("after #4 closed %+v", g)
	}
	// The last open member merges: nothing is left to review together.
	sync(append(closedFour, discovery.Observation{Number: 3, State: "MERGED"}))
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Fatalf("an ended stack kept its group: %+v", list)
	}
}

func TestSyncStacksMarksAnUnseenMemberUnknownUntilObserved(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.SyncStacks(ctx, "acme/demo", []discovery.PullRequest{open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b")}, nil); err != nil {
		t.Fatal(err)
	}
	// #1 left the open list and nothing is known of it: it is not called merged.
	if _, err := s.SyncStacks(ctx, "acme/demo", []discovery.PullRequest{open(2, "b", "main"), open(3, "c", "b")}, nil); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || !slices.Equal(list[0].Landed, []LandedMember{{1, StateUnknown}}) {
		t.Fatalf("groups %+v", list)
	}
}

func TestOpenAddsTheChainColumnToAnOlderDatabase(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "state", "groups.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE groups DROP COLUMN chain`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSyncStacksDoesNotKeepAPullRequestThePersonGroupedAsLanded(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.SyncStacks(ctx, "acme/demo", []discovery.PullRequest{open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "acme/demo", "Mine", []int{3, 9}); err != nil {
		t.Fatal(err)
	}
	// #3 is open and in the person's group: it leaves the stack group, not as merged.
	if _, err := s.SyncStacks(ctx, "acme/demo", []discovery.PullRequest{open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b")}, nil); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	for _, g := range list {
		if g.Stack && (slices.Contains(landedNumbers(g), 3) || slices.Contains(g.Members, 3)) {
			t.Fatalf("stack group %+v kept #3", g)
		}
	}
}

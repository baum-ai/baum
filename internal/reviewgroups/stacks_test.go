package reviewgroups

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
)

func open(number int, head, base string) discovery.PullRequest {
	return discovery.PullRequest{Number: number, HeadRef: head, BaseRef: base, Title: "PR " + head, Author: "dev"}
}

func TestStacksFollowBaseBranches(t *testing.T) {
	fork := open(20, "fork-top", "c")
	fork.Fork = true
	long := []discovery.PullRequest{open(30, "l0", "main")}
	for i := 1; i <= MaxMembers; i++ {
		long = append(long, open(30+i, "l"+string(rune('0'+i)), "l"+string(rune('0'+i-1))))
	}
	release := open(40, "develop", "main")
	release.Author = "releaser"
	other := open(43, "theirs", "c")
	other.Author = "someone"
	prs := append([]discovery.PullRequest{
		// A chain listed out of order: main <- a (#5) <- b (#3) <- c (#9).
		open(9, "c", "b"), open(3, "b", "a"), open(5, "a", "main"),
		// Unstacked, a fork on c and another author's PR on c are left out.
		open(7, "solo", "main"), fork, other,
		// Two PRs sharing a base that no open PR heads are not a stack.
		open(12, "x", "release"), open(13, "y", "release"),
		// A develop branch with its own PR into main is shared: the features
		// based on it are no stack.
		release, open(41, "f1", "develop"), open(42, "f2", "develop"),
	}, long...)
	stacks := Stacks(prs)
	if len(stacks) != 1 {
		t.Fatalf("stacks %+v", stacks)
	}
	if !slices.Equal(stacks[0].Members, []int{3, 5, 9}) || stacks[0].Name != "PR a" {
		t.Fatalf("stack %+v", stacks[0])
	}
	// A pull request branching off a stack leaves the branch point's
	// children unstacked: b keeps a, c and c2 do not join.
	branching := Stacks([]discovery.PullRequest{open(1, "a", "main"), open(2, "b", "a"), open(3, "c", "b"), open(4, "c2", "b")})
	if len(branching) != 1 || !slices.Equal(branching[0].Members, []int{1, 2}) {
		t.Fatalf("branching %+v", branching)
	}
}

func TestStackNameFitsAGroupName(t *testing.T) {
	long := open(1, "a", "main")
	long.Title = strings.Repeat("word ", 40) + "\tend"
	if name, err := cleanName(stackName(long)); err != nil || len([]rune(name)) > MaxNameLength || !strings.HasPrefix(name, "word word") {
		t.Fatalf("name %q %v", name, err)
	}
	if name := stackName(open(4, "a", "main")); name != "PR a" {
		t.Fatalf("name %q", name)
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
	sync := func(stacks ...Stack) bool {
		t.Helper()
		changed, err := s.SyncStacks(ctx, "Acme/Demo", stacks)
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
	if !sync(Stack{Members: []int{2, 1, 3}, Name: "Bottom"}) {
		t.Fatal("a new stack changed nothing")
	}
	list := groups()
	if len(list) != 1 || !list[0].Stack || list[0].Name != "Bottom" || list[0].Repository != "acme/demo" || !slices.Equal(list[0].Members, []int{1, 2, 3}) {
		t.Fatalf("groups %+v", list)
	}
	id := list[0].ID
	if sync(Stack{Members: []int{1, 2, 3}, Name: "Bottom"}) {
		t.Fatal("the same stack changed something")
	}
	// The bottom merged and a PR was added on top: same group, new members.
	if !sync(Stack{Members: []int{2, 3, 4}, Name: "Second"}) {
		t.Fatal("new members changed nothing")
	}
	if list = groups(); len(list) != 1 || list[0].ID != id || list[0].Name != "Bottom" || !slices.Equal(list[0].Members, []int{2, 3, 4}) {
		t.Fatalf("groups %+v", list)
	}
	// The stack is gone: the group is dissolved, and its pull requests may
	// form a stack group again.
	if !sync() || len(groups()) != 0 {
		t.Fatalf("dissolved %+v", groups())
	}
	sync(Stack{Members: []int{3, 4}, Name: "Again"})
	if list = groups(); len(list) != 1 || list[0].ID == id {
		t.Fatalf("groups %+v", list)
	}
	// The person deletes it: no stack group for its pull requests again.
	if err := s.Delete(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if sync(Stack{Members: []int{3, 4, 5}, Name: "Again"}) || len(groups()) != 0 {
		t.Fatalf("a deleted stack group came back: %+v", groups())
	}
	// A pull request in the person's own group keeps its stack ungrouped;
	// other repositories are not affected.
	if _, err := s.Create(ctx, "acme/demo", "Mine", []int{7, 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "acme/other", "Elsewhere", []int{10, 11}); err != nil {
		t.Fatal(err)
	}
	sync(Stack{Members: []int{8, 9}, Name: "Touches mine"}, Stack{Members: []int{10, 11}, Name: "Here"})
	list = groups()
	var stack Group
	for _, g := range list {
		if g.Stack {
			stack = g
		}
	}
	if len(list) != 3 || !slices.Equal(stack.Members, []int{10, 11}) {
		t.Fatalf("groups %+v", list)
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

func TestOpenAddsTheStackColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE groups(id TEXT PRIMARY KEY, repository TEXT NOT NULL, name TEXT NOT NULL, members TEXT NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0);
INSERT INTO groups(id,repository,name,members,created,updated) VALUES('old','acme/demo','Old','[1,2]',1,1);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if g, err := s.Get(context.Background(), "old"); err != nil || g.Stack || !slices.Equal(g.Members, []int{1, 2}) {
		t.Fatalf("old group %+v %v", g, err)
	}
}

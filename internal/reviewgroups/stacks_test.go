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
		changed, err := s.SyncStacks(ctx, "Acme/Demo", prs)
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
	// group, new members, same name.
	if !sync(open(2, "b", "main"), open(3, "c", "b"), open(4, "d", "c")) {
		t.Fatal("new members changed nothing")
	}
	if list = groups(); len(list) != 1 || list[0].ID != id || list[0].Name != "PR a" || !slices.Equal(list[0].Members, []int{2, 3, 4}) {
		t.Fatalf("groups %+v", list)
	}
	// The stack is gone: the group is dissolved, and its pull requests may
	// form a stack group again.
	if !sync(open(4, "d", "main")) || len(groups()) != 0 {
		t.Fatalf("dissolved %+v", groups())
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
	if changed, err := s.SyncStacks(context.Background(), "acme/demo", prs); err != nil || changed {
		t.Fatalf("a stack of %d: %v %v", len(prs), changed, err)
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
	defer func() { _ = s.Close() }()
	if g, err := s.Get(context.Background(), "old"); err != nil || g.Stack || !slices.Equal(g.Members, []int{1, 2}) {
		t.Fatalf("old group %+v %v", g, err)
	}
}

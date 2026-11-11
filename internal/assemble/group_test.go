package assemble_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
)

// A file the merged group's index collapses keeps the members of every hunk
// it stands for, so a section placing it still names their pull requests.
func TestMergeGroupKeepsTheMembersOfACollapsedFile(t *testing.T) {
	member := func(number int, from, to int) assemble.GroupMember {
		var diff []bundle.Hunk
		for i := from; i < to; i++ {
			diff = append(diff, bundle.Hunk{
				ID: fmt.Sprintf("h_%012x", i), Path: "yarn.lock", Change: "modified", Role: bundle.RoleGenerated,
				StartLine: 10 * (i + 1), LineCount: 2, Additions: 1, Deletions: 1, DiffURL: bundle.HunkDiffURL("yarn.lock", 10*(i+1)),
			})
		}
		diff = append(diff, bundle.Hunk{ID: fmt.Sprintf("h_%012x", 1000+number), Path: fmt.Sprintf("pr%d.go", number), Change: "modified", Role: bundle.RoleCode, StartLine: 1, LineCount: 1, Additions: 1})
		b := bundle.Bundle{Repository: "acme/widgets", PullRequest: bundle.PullRequest{Number: number, Title: fmt.Sprint(number)}, Diff: diff, Sources: bundle.Sources{}}
		return assemble.GroupMember{Number: number, Result: assemble.Result{Bundle: b, Head: "head", Base: "base", MergeBase: "base"}}
	}
	members := []assemble.GroupMember{member(7, 0, 20), member(9, 20, 40)}
	merged, err := assemble.MergeGroup(context.Background(), "Group", members, nil, bundle.Caps{MaxBytes: 1 << 20, MaxDiffIndexBytes: 2048}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lock *bundle.Hunk
	for i, hunk := range merged.Bundle.Diff {
		if hunk.Path == "yarn.lock" {
			if lock != nil {
				t.Fatalf("yarn.lock not collapsed: %+v", merged.Bundle.Diff)
			}
			lock = &merged.Bundle.Diff[i]
		}
	}
	if lock == nil || lock.TextlessReason != bundle.CollapsedReason {
		t.Fatalf("diff %+v", merged.Bundle.Diff)
	}
	if got := merged.HunkMembers[lock.ID]; !slices.Equal(got, []int{7, 9}) {
		t.Fatalf("members of the collapsed file %v, want [7 9]", got)
	}
	if got := merged.HunkMembers[fmt.Sprintf("h_%012x", 1007)]; !slices.Equal(got, []int{7}) {
		t.Fatalf("members of pr7.go %v, want [7]", got)
	}
}

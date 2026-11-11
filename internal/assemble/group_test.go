package assemble_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	// A member's own fit already collapsed the file, the checkout omitted it,
	// and the other member's hunks collapse into the same entry: it keeps
	// the file's ID and the omission, and still names both members.
	sum := sha256.Sum256([]byte("yarn.lock\x00"))
	fileID := "h_" + hex.EncodeToString(sum[:6])
	first := member(7, 0, 0)
	first.Result.Bundle.Diff = append(first.Result.Bundle.Diff, bundle.Hunk{
		ID: fileID, Path: "yarn.lock", Change: "modified", Role: bundle.RoleGenerated, StartLine: 10, LineCount: 400,
		Textless: true, TextlessReason: bundle.OmittedSizeReason,
	})
	merged, err = assemble.MergeGroup(context.Background(), "Group", []assemble.GroupMember{first, member(9, 20, 40)}, nil, bundle.Caps{MaxBytes: 1 << 20, MaxDiffIndexBytes: 2048}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.HunkMembers[fileID]; !slices.Equal(got, []int{7, 9}) {
		t.Fatalf("members of the collapsed, omitted file %v, want [7 9]; diff %+v", got, merged.Bundle.Diff)
	}
}

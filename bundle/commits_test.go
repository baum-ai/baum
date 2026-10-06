package bundle

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func at(minutes int) *time.Time {
	t := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
	return &t
}

type fakeCommitComparer struct {
	compared []string
	failing  string
}

func (f *fakeCommitComparer) Compare(_ context.Context, base, head, mode string) (Comparison, error) {
	f.compared = append(f.compared, base+".."+head+" "+mode)
	if head == f.failing {
		return Comparison{}, errors.New("object not found")
	}
	return Comparison{Files: []FileChange{
		{Additions: new(int64(5)), Deletions: new(int64(2))},
		{}, // a binary file reports no line counts
		{Additions: new(int64(1)), Deletions: new(int64(0))},
	}}, nil
}

func TestAttachCommitChangesReadsOnlyCommitsAfterOpening(t *testing.T) {
	pr := PullRequest{
		OpenedAt: at(10),
		Commits: []Commit{
			{SHA: "before", CommittedAt: at(0), Parents: []string{"p0"}},
			{SHA: "after", CommittedAt: at(20), Parents: []string{"before"}},
			{SHA: "merge", CommittedAt: at(30), Parents: []string{"after", "main"}},
			{SHA: "missing", CommittedAt: at(40), Parents: []string{"merge"}},
		},
	}
	comparer := &fakeCommitComparer{failing: "missing"}
	if err := AttachCommitChanges(t.Context(), comparer, &pr); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(comparer.compared, []string{"before..after direct", "merge..missing direct"}) {
		t.Errorf("compared = %q", comparer.compared)
	}
	c := pr.Commits
	if c[0].Additions != nil {
		t.Errorf("a commit before opening has counts %d", *c[0].Additions)
	}
	if c[1].Additions == nil || *c[1].Additions != 6 || *c[1].Deletions != 2 {
		t.Errorf("after = %v/%v, want 6/2", c[1].Additions, c[1].Deletions)
	}
	if c[2].Additions == nil || *c[2].Additions != 0 || *c[2].Deletions != 0 {
		t.Errorf("merge = %v/%v, want 0/0", c[2].Additions, c[2].Deletions)
	}
	if c[3].Additions != nil {
		t.Errorf("an uncompared commit has counts %d", *c[3].Additions)
	}
}

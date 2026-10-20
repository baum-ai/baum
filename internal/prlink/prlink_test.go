package prlink

import (
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
)

func TestFindFollowsBranchUpstreamThenCommit(t *testing.T) {
	snapshot := discovery.Snapshot{PullRequests: []discovery.PullRequest{
		{Number: 3, HeadRef: "feature", HeadSHA: "aaa"},
		{Number: 5, HeadRef: "feature", HeadSHA: "bbb"},
		{Number: 6, HeadRef: "pr-head", HeadSHA: "ccc"},
		{Number: 9, HeadRef: "local", HeadSHA: "ddd", Fork: true},
	}}
	for _, tc := range []struct {
		name string
		work Work
		want int
	}{
		{"branch, newest of several", Work{Branch: "feature", Upstream: "pr-head"}, 5},
		{"upstream name", Work{Branch: "local", Upstream: "pr-head"}, 6},
		{"a fork's branch of the same name is another", Work{Branch: "local"}, 0},
		{"commit", Work{Branch: "local", Head: "CCC"}, 6},
		{"detached", Work{Head: "aaa"}, 3},
		{"nothing", Work{}, 0},
	} {
		if got := Find(snapshot, tc.work); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

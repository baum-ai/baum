package prlink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/gitlocal"
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

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLocalUpstreamComparesTheHost(t *testing.T) {
	for _, tc := range []struct {
		name, repository, same, other string
	}{
		{"github.com", "acme/demo", "https://github.com/acme/demo", "https://ghe.acme.test/acme/demo"},
		{"enterprise", "octo.ghe.com/acme/demo", "git@octo.ghe.com:acme/demo.git", "https://github.com/acme/demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clone := t.TempDir()
			gitIn(t, clone, "init", "-q", "-b", "main")
			gitIn(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
			gitIn(t, clone, "checkout", "-q", "-b", "local")
			gitIn(t, clone, "remote", "add", "origin", tc.same)
			gitIn(t, clone, "remote", "add", "other", tc.other)
			gitIn(t, clone, "config", "branch.local.merge", "refs/heads/pushed-as")
			repo, err := gitlocal.Open(context.Background(), filepath.Clean(clone), gitlocal.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repo.Close() }()
			gitIn(t, clone, "config", "branch.local.remote", "origin")
			if got := Local(context.Background(), repo, tc.repository).Upstream; got != "pushed-as" {
				t.Fatalf("same host: %q", got)
			}
			gitIn(t, clone, "config", "branch.local.remote", "other")
			if got := Local(context.Background(), repo, tc.repository).Upstream; got != "" {
				t.Fatalf("other host: %q", got)
			}
		})
	}
}

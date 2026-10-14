package locallink

import (
	"strings"
	"testing"

	"github.com/baumai/baum/internal/locallink/linktest"
)

// A fork clone has the fork as origin and the pull request's repository as
// another remote.
func TestResolveLinksWhenAnyRemoteMatches(t *testing.T) {
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "remote", "set-url", "origin", "https://192.0.2.1/someone/widgets-fork.git")
	linktest.Git(t, f.Repo, "remote", "add", "upstream", linktest.UnreachableOrigin)
	linktest.Git(t, f.Repo, "remote", "add", "other", "git@github.com:else/where.git")
	result := resolve(t, request(f))
	if !result.Available || len(result.Sessions) != 1 || len(result.Commits[0].Links) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestResolveNamesOnlyOwnerAndNameWhenNoRemoteMatches(t *testing.T) {
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "remote", "set-url", "origin", "https://user:s3cret-token@192.0.2.1/someone/widgets-fork.git")
	linktest.Git(t, f.Repo, "remote", "add", "mirror", "/srv/mirrors/widgets.git")
	result := resolve(t, request(f))
	if result.Available || result.Code != CodeRemoteMismatch {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range []string{"no remote of this clone points at acme/widgets", "origin → someone/widgets-fork", "mirror → not a repository URL"} {
		if !strings.Contains(result.Reason, want) {
			t.Errorf("reason %q lacks %q", result.Reason, want)
		}
	}
	for _, leaked := range []string{"s3cret", "user:", "192.0.2.1", "/srv/mirrors"} {
		if strings.Contains(result.Reason, leaked) {
			t.Errorf("reason leaks %q: %q", leaked, result.Reason)
		}
	}
}

func TestResolveCapsTheRemotesItNames(t *testing.T) {
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "remote", "remove", "origin")
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		linktest.Git(t, f.Repo, "remote", "add", name, "https://github.com/x/"+name+".git")
	}
	result := resolve(t, request(f))
	if result.Code != CodeRemoteMismatch || !strings.Contains(result.Reason, "and 2 more") || strings.Contains(result.Reason, "g →") {
		t.Errorf("reason = %q", result.Reason)
	}
}

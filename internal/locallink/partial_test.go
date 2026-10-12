package locallink

import (
	"path/filepath"
	"testing"

	"github.com/lastpersonlabs/baum/internal/locallink/linktest"
)

// A partial clone that has the commits but not their trees cannot list the
// files a commit changed. That is its own answer, not "commits missing", and
// reading it downloads nothing.
func TestResolveInAPartialCloneWithoutTrees(t *testing.T) {
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	linktest.Git(t, t.TempDir(), "clone", "-q", "--filter=tree:0", "--no-checkout", "file://"+filepath.ToSlash(f.Repo), partial)
	linktest.Git(t, partial, "remote", "set-url", "origin", linktest.UnreachableOrigin)
	req := request(f)
	req.Dir = partial
	result := resolve(t, req)
	if result.Available || result.Code != CodePartialClone {
		t.Fatalf("result = %+v, want unavailable %s", result, CodePartialClone)
	}
}

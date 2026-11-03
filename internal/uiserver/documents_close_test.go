package uiserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// Closing the document cache, as the daemon does when it stops, closes the
// clones its Patcher keeps open for further patches of a range.
func TestDocumentCacheCloseClosesTheKeptClones(t *testing.T) {
	f := linktest.New(t)
	tmp := linktest.TempDir(t)
	cache := newDocumentCache()
	result, err := cache.patch(context.Background(), locallink.PatchRequest{Dir: f.Repo, Repository: linktest.Repository, Head: f.Head, Base: f.Base, Path: "c.txt"})
	if err != nil || !result.Available {
		t.Fatalf("patch: %+v %v", result, err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-git-*")); len(left) != 1 {
		t.Fatalf("kept clones before close: %v", left)
	}
	cache.close()
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-git-*")); len(left) != 0 {
		t.Fatalf("left behind after close: %v", left)
	}
}

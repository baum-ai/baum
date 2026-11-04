package reviewgroups

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// Close, which the daemon calls when it stops, closes the clones the API's
// own Patcher keeps open.
func TestAPICloseClosesTheKeptClones(t *testing.T) {
	f := linktest.New(t)
	tmp := linktest.TempDir(t)
	api := &API{}
	api.Handler()
	result, err := api.Patch(context.Background(), locallink.PatchRequest{Dir: f.Repo, Repository: linktest.Repository, Head: f.Head, Base: f.Base, Path: "c.txt"})
	if err != nil || !result.Available {
		t.Fatalf("patch: %+v %v", result, err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-git-*")); len(left) != 1 {
		t.Fatalf("kept clones before close: %v", left)
	}
	api.Close()
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-git-*")); len(left) != 0 {
		t.Fatalf("left behind after close: %v", left)
	}
}

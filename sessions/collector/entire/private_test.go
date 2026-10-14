package entire

import (
	"context"
	"testing"

	"github.com/baumai/baum/internal/gitlocal"
)

// privateRepo is the private repository List, Read and Touched read objects
// from, made for the clone at dir.
func privateRepo(t *testing.T, dir string) string {
	t.Helper()
	repo, err := gitlocal.Open(context.Background(), dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo.PrivateDir()
}

package locallink

import (
	"context"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// OpenClone opens the clone containing dir for reads, once one of its remotes
// is repository ("owner/name"). A clone that cannot answer is a code
// (CodeNotAClone, CodeRemoteMismatch, CodeNoHead for an empty repository),
// not an error. The caller closes the repo.
func OpenClone(ctx context.Context, dir, repository string) (*gitlocal.Repo, string, error) {
	return openFor(ctx, dir, repository)
}

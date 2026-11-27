package locallink

import (
	"context"
	"path/filepath"
	"slices"
	"sync"

	"github.com/baum-ai/baum/internal/fixdir"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions"
)

// fixRoot is where fixes check out; tests replace it.
var fixRoot = fixdir.Default

// commonDirs remembers each directory's clone git directory, so a refresh
// that reads sessions again runs no git command for it.
var commonDirs sync.Map

// WithFixes adds the clone's fixes (fixdir.ForClone) to what opts read. A fix
// agent's session is recorded in a checkout that is gone once the fix ends,
// and the commit baum made of its work is linked to it by the observation
// the fix recorded (commitattribution). Without the clone's git directory or
// a cache directory, opts are as they were.
func WithFixes(ctx context.Context, dir string, opts sessions.Options) sessions.Options {
	root, err := fixRoot()
	if err != nil {
		return opts
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return opts
	}
	common, ok := commonDirs.Load(abs)
	if !ok {
		found, err := gitlocal.CommonDir(ctx, abs)
		if err != nil {
			return opts
		}
		common, _ = commonDirs.LoadOrStore(abs, found)
	}
	opts.CheckoutParents = append(slices.Clone(opts.CheckoutParents), fixdir.ForClone(root, common.(string)))
	return opts
}

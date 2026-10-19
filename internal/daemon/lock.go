package daemon

import (
	"context"
	"time"

	"github.com/baum-ai/baum/internal/filelock"
)

var errLocked = filelock.ErrLocked

func tryLock(ctx context.Context, path string) (*filelock.Lock, error) {
	return filelock.TryLock(ctx, path)
}

func waitLock(ctx context.Context, path string, timeout time.Duration) (*filelock.Lock, error) {
	return filelock.Wait(ctx, path, timeout)
}

func held(ctx context.Context, path string) (bool, error) { return filelock.Held(ctx, path) }

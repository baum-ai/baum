package sessions

import (
	"context"

	"golang.org/x/sync/semaphore"
)

// DefaultDecodeBudgetBytes bounds the transcript bytes decoded at once across
// the process. A decode holds the whole transcript and its decoded lines, so
// this is a few times its resident cost. Sessions wait for budget; one larger
// than the whole budget runs alone. It sits beside DefaultMapCacheBytes, which
// bounds what is kept after decoding.
const DefaultDecodeBudgetBytes = 256 << 20

// BackgroundParallel is how many sessions a background pass decodes at once.
// A pass is not latency-sensitive, so it trades speed for memory.
const BackgroundParallel = 2

// checkpointReadBytes is the budget weight of reading one Entire checkpoint,
// whose size is not known before it is read.
const checkpointReadBytes = 32 << 20

type backgroundKey struct{}

// WithBackground marks ctx as a background pass: loads under it decode at
// most BackgroundParallel sessions at once. A context value is used because
// the pass reaches Discovery.Load through several packages that already pass
// the context down, so no signature changes.
func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// parallelFor is how many sessions a load under ctx decodes at once.
func parallelFor(ctx context.Context) int {
	if background, _ := ctx.Value(backgroundKey{}).(bool); background {
		return min(BackgroundParallel, maxParallel())
	}
	return maxParallel()
}

// decodeObserver, when set, is called as a decode starts and its result as it
// ends; tests count concurrent decodes with it.
var decodeObserver func(cost int64) (done func())

// decodeBudget is the process-wide budget; tests replace it.
var decodeBudget = newDecodeBudget(DefaultDecodeBudgetBytes)

type budget struct {
	size int64
	sem  *semaphore.Weighted
}

func newDecodeBudget(size int64) *budget {
	return &budget{size: size, sem: semaphore.NewWeighted(size)}
}

// acquire waits for n bytes of budget, clamped to the whole budget so a
// larger session runs alone instead of waiting forever. It returns the
// release, or ctx's error.
func (b *budget) acquire(ctx context.Context, n int64) (release func(), err error) {
	n = max(0, min(n, b.size))
	if n == 0 {
		return func() {}, ctx.Err()
	}
	if err := b.sem.Acquire(ctx, n); err != nil {
		return nil, err
	}
	return func() { b.sem.Release(n) }, nil
}

package discovery

import (
	"context"
	"time"
)

// WithIdleTimeout returns a context for a poll that is cancelled once idle
// passes without GitHub answering one of the poll's requests. A poll reads
// every open pull request, so how long it takes grows with the repository; a
// fixed deadline long enough for the busiest repository would also leave a
// stalled connection hanging that long.
func WithIdleTimeout(parent context.Context, idle time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &idleTimer{idle: idle, timer: time.AfterFunc(idle, func() { cancel(context.DeadlineExceeded) })}
	return context.WithValue(ctx, idleKey{}, w), func() {
		w.timer.Stop()
		cancel(context.Canceled)
	}
}

type idleKey struct{}

type idleTimer struct {
	idle  time.Duration
	timer *time.Timer
}

// answered restarts the idle timeout of ctx, if it has one: GitHub answered.
func answered(ctx context.Context) {
	if w, ok := ctx.Value(idleKey{}).(*idleTimer); ok {
		w.timer.Reset(w.idle)
	}
}

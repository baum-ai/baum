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
//
// The timeout is paused while the poll waits for its locks: a poll queued
// behind another one, which may itself take minutes, is not waiting on GitHub.
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

// pauseIdle stops the idle timeout of ctx, if it has one, until restartIdle.
func pauseIdle(ctx context.Context) {
	if w, ok := ctx.Value(idleKey{}).(*idleTimer); ok {
		w.timer.Stop()
	}
}

// restartIdle restarts the idle timeout of ctx, if it has one: the poll holds
// its locks, or GitHub answered.
func restartIdle(ctx context.Context) {
	if w, ok := ctx.Value(idleKey{}).(*idleTimer); ok {
		w.timer.Reset(w.idle)
	}
}

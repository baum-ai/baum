package github

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

type requestTimeoutKey struct{}

// WithRequestTimeout bounds each GraphQL request made with ctx, every retry
// on its own, rather than the whole read: a pull request with many pages
// takes as long as its pages do, while a request GitHub stops answering
// fails after d. A request that runs out of time fails with an error that
// is context.DeadlineExceeded, which fails a Fetch as a whole.
func WithRequestTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, requestTimeoutKey{}, d)
}

// requestContext is the context of one request attempt made with ctx.
func requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := ctx.Value(requestTimeoutKey{}).(time.Duration); ok && d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return ctx, func() {}
}

// requestTimeoutError is a request that ran out of the time
// WithRequestTimeout gave it.
type requestTimeoutError struct {
	operation string
	after     time.Duration
}

func (e *requestTimeoutError) Error() string {
	return fmt.Sprintf("github %s query: GitHub did not answer within %v", e.operation, e.after)
}

func (e *requestTimeoutError) Unwrap() error { return context.DeadlineExceeded }

type requestCountKey struct{}

// requestCount counts the GraphQL requests made with a context, and adds
// them to the count of the context it was made from too.
type requestCount struct {
	n      atomic.Int64
	parent *requestCount
}

// CountRequests returns a context whose GraphQL requests are counted, each
// retry included, and a function that reports the count so far. Each request
// costs GitHub at least one point of the token's hourly GraphQL budget.
func CountRequests(ctx context.Context) (context.Context, func() int) {
	parent, _ := ctx.Value(requestCountKey{}).(*requestCount)
	count := &requestCount{parent: parent}
	return context.WithValue(ctx, requestCountKey{}, count), func() int { return int(count.n.Load()) }
}

type requestPacerKey struct{}

// WithRequestPacer makes every GraphQL request made with ctx, each retry
// included, call pace first, which may wait or refuse; a refusal ends the
// read with that error. It lets a caller hold a read to a rate it chose.
func WithRequestPacer(ctx context.Context, pace func(context.Context) error) context.Context {
	return context.WithValue(ctx, requestPacerKey{}, pace)
}

// PaceRequest runs the pacer ctx carries, if any, before a request is sent.
func PaceRequest(ctx context.Context) error {
	if pace, ok := ctx.Value(requestPacerKey{}).(func(context.Context) error); ok && pace != nil {
		return pace(ctx)
	}
	return nil
}

func countRequest(ctx context.Context) {
	for count, _ := ctx.Value(requestCountKey{}).(*requestCount); count != nil; count = count.parent {
		count.n.Add(1)
	}
}

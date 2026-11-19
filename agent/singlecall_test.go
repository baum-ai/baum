package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

// fakeCompleter answers each call with the next reply, recording the
// requests.
type fakeCompleter struct {
	mu       sync.Mutex
	replies  []string
	errs     []error
	requests []agent.CompleteRequest
	// block waits for the call's context to end; cancel is called first.
	block  bool
	cancel func()
}

func (f *fakeCompleter) Complete(ctx context.Context, r agent.CompleteRequest) (agent.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.requests)
	f.requests = append(f.requests, r)
	out := agent.Completion{Model: "claude-test", Usage: agent.Usage{Input: 1000, Output: 200, CacheRead: 50}}
	if f.cancel != nil {
		f.cancel()
	}
	if f.block {
		<-ctx.Done()
		return out, ctx.Err()
	}
	if n < len(f.errs) && f.errs[n] != nil {
		return out, f.errs[n]
	}
	if n < len(f.replies) {
		out.Document = []byte(f.replies[n])
	}
	return out, nil
}

// converted is the schema's example review, normalised, and its tile
// document.
func converted(t *testing.T) ([]byte, *tiles.Document) {
	t.Helper()
	normalized, err := review.NormalizeStored(schema.Example)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return normalized, doc
}

var fixedNow = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

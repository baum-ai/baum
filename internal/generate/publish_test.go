package generate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/store"
)

func TestPublishCancellationDuringRepairKeepsStoreEmptyAndCleansTrees(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.backend.RunFunc = func(ctx context.Context, r agent.Request, n int) (agent.Result, error) {
		if n == 0 {
			return agent.Result{Document: []byte(`{}`), Calls: 1}, nil
		}
		cancel()
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	s, e := store.Open("", t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	key := store.Key{Repository: "example/project", Number: 1}
	_, e = generate.Publish(ctx, s, key, f.options(t), nil)
	if generate.CategoryOf(e) != generate.CategoryInterrupted {
		t.Fatal(e)
	}
	if _, e = s.Latest(context.Background(), key); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("partial version published", e)
	}
	snapshot, e := s.Snapshot(context.Background())
	if e != nil || len(snapshot) != 1 || snapshot[0].Run.Reason != "interrupted" {
		t.Fatal("missing interrupted run", snapshot, e)
	}
	f.requireClean(t)
}

package generate_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
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

type failingFinish struct{ *store.Store }

func (failingFinish) FinishRun(context.Context, int64, string) error {
	return errors.New("database is locked")
}

func TestPublishSucceedsWhenRecordingOutcomeFailsAfterPut(t *testing.T) {
	f := newFixture(t)
	s, e := store.Open("", t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, e := generate.Publish(context.Background(), failingFinish{s}, key, f.options(t), nil)
	if e != nil {
		t.Fatalf("published version reported as failed: %v", e)
	}
	if len(result.Document) == 0 {
		t.Fatal("published document not returned")
	}
	latest, e := s.Latest(context.Background(), key)
	if e != nil || !bytes.Equal(latest.Document, result.Document) {
		t.Fatal("returned document is not the stored version", e)
	}
	if !strings.Contains(f.progress.String(), "warning: record generation outcome: database is locked") {
		t.Fatalf("missing warning: %q", f.progress.String())
	}
}

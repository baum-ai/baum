package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
)

// A refresh cannot replace the document a generation is building on, so the
// generation publishes instead of conflicting with a background refresh.
func TestPublishKeepsPreviousDocumentFixedDuringGeneration(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	valid := func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, nil)
		return agent.Result{Document: document, Model: "stub-model", Calls: 1}, err
	}
	f.backend.RunFunc = valid
	s, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	if _, err = generate.Publish(ctx, s, key, f.options(t), nil); err != nil {
		t.Fatal(err)
	}
	first, err := s.Latest(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(first.Document, &doc); err != nil {
		t.Fatal(err)
	}
	doc["refreshed"] = json.RawMessage(`true`)
	refreshed, _ := json.Marshal(doc)
	var refreshErr error
	f.backend.RunFunc = func(ctx context.Context, request agent.Request, n int) (agent.Result, error) {
		refreshErr = s.RefreshCurrent(ctx, key, first, refreshed)
		return valid(ctx, request, n)
	}
	if _, err = generate.Publish(ctx, s, key, f.options(t), nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(refreshErr, store.ErrConflict) {
		t.Fatalf("refresh replaced the previous document during generation: %v", refreshErr)
	}
	if latest, err := s.Latest(ctx, key); err != nil || latest.Metadata.Version != 2 {
		t.Fatalf("generation not published: %+v %v", latest.Metadata, err)
	}
	if old, err := s.Get(ctx, key, 1); err != nil || !bytes.Equal(old.Document, first.Document) {
		t.Fatal("previous version changed", err)
	}
}

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

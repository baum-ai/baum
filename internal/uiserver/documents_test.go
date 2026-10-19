package uiserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
)

func TestDocumentCacheSharesLinkageAndInvalidatesRevision(t *testing.T) {
	cache := newDocumentCache()
	defer cache.close()
	var calls atomic.Int64
	cache.resolve = func(context.Context, locallink.Request) (locallink.Result, error) {
		calls.Add(1)
		return locallink.Result{Available: true}, nil
	}
	clone := repos.Clone{ID: "one", Repository: "owner/repo"}
	doc := []byte(`{"review":{"repository":"owner/repo","head":"aaaaaaaa","base":"bbbbbbbb"}}`)
	server, release, err := cache.get(clone, doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sessions", "/api/commits", "/api/sessions"} {
		w := httptest.NewRecorder()
		server.Mux().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	release()
	same, release, err := cache.get(clone, doc)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if same != server || calls.Load() != 1 {
		t.Fatal("linkage not reused", calls.Load())
	}
	changed, release, err := cache.get(clone, append(doc[:len(doc)-1], []byte(`,"summary":{}}`)...))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if changed == server {
		t.Fatal("changed document reused old evidence")
	}
	clone.ID = "two"
	other, release, err := cache.get(clone, doc)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if other == server {
		t.Fatal("different clone reused cached evidence")
	}
	for i := range maxCachedDocuments + 2 {
		clone.ID = fmt.Sprint(i)
		_, release, err := cache.get(clone, doc)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if len(cache.entries) != maxCachedDocuments {
		t.Fatal("cache unbounded", len(cache.entries))
	}
}
func TestDocumentCacheBoundsConcurrentLocalReadsAcrossDocuments(t *testing.T) {
	cache := newDocumentCache()
	defer cache.close()
	var active, maxActive atomic.Int64
	entered := make(chan struct{}, maxLocalReads)
	unblock := make(chan struct{})
	cache.resolve = func(ctx context.Context, _ locallink.Request) (locallink.Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maxActive.Load()
			if n <= previous || maxActive.CompareAndSwap(previous, n) {
				break
			}
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-unblock:
			return locallink.Result{Available: true}, nil
		case <-ctx.Done():
			return locallink.Result{}, ctx.Err()
		}
	}
	var workers sync.WaitGroup
	for i := range 12 {
		server, release, err := cache.get(repos.Clone{ID: fmt.Sprint(i)}, []byte(`{"review":{"repository":"owner/repo","head":"aaaaaaaa","base":"bbbbbbbb"}}`))
		if err != nil {
			t.Fatal(err)
		}
		workers.Go(func() {
			defer release()
			server.Mux().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/sessions", nil))
		})
	}
	for range maxLocalReads {
		<-entered
	}
	if got := maxActive.Load(); got != maxLocalReads {
		t.Fatal("local read bound", got)
	}
	close(unblock)
	workers.Wait()
	if got := maxActive.Load(); got > maxLocalReads {
		t.Fatal("unbounded concurrent reads", got)
	}
}

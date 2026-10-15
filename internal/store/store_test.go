package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func version(k Key, n int) Version {
	return Version{Metadata: Metadata{Version: n, Head: "revision", BundleDigest: "digest", Backend: "claude", PromptVersion: "prompt"}, Document: []byte(fmt.Sprintf(`{"review":{"repository":%q,"pull_request":%d,"version":%d}}`, k.Repository, k.Number, n))}
}
func opened(t *testing.T) *Store {
	t.Helper()
	s, err := Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestVersionsAreAtomicImmutableAndRepositoryScoped(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"one/repo", 7}
	first := version(key, 1)
	if _, err := s.Latest(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Put(ctx, key, 0, first); err != nil {
		t.Fatal(err)
	}
	second := version(key, 2)
	second.Metadata.Head = "new revision"
	if err := s.Put(ctx, key, 1, second); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Key{{"two/repo", 7}, {"enterprise.test/one/repo", 7}, {"one/repo", 8}} {
		if _, err := s.Latest(ctx, other); !errors.Is(err, ErrNotFound) {
			t.Fatalf("crossed key: %v", err)
		}
		if err := s.Put(ctx, other, 0, version(other, 1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, Key{"ONE/REPO", 7}, 1)
	if err != nil || string(got.Document) != string(first.Document) || got.Metadata != first.Metadata {
		t.Fatalf("first version changed: %+v %v", got, err)
	}
	all, err := s.Versions(ctx, key)
	if err != nil || len(all) != 2 || all[0].Version != 2 || all[1].Version != 1 {
		t.Fatalf("versions: %+v %v", all, err)
	}
	if err := s.Put(ctx, key, 0, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err := s.Get(ctx, key, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	bad := version(key, 3)
	bad.Document = []byte(`{}`)
	if err := s.Put(ctx, key, 2, bad); err == nil {
		t.Fatal("accepted mismatched document")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Put(ctx, key, 2, version(key, 3)); err == nil {
		t.Fatal("published after cancellation")
	}
	latest, err := s.Latest(context.Background(), key)
	if err != nil || latest.Metadata.Version != 2 {
		t.Fatalf("failed write changed latest: %+v %v", latest, err)
	}
}
func TestConcurrentGenerationComparesHistoryUnderWriterLock(t *testing.T) {
	s := opened(t)
	key := Key{"owner/repo", 7}
	ctx := context.Background()
	if err := s.Put(ctx, key, 0, version(key, 1)); err != nil {
		t.Fatal(err)
	}
	const count = 8
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() { <-start; results <- s.Put(ctx, key, 1, version(key, 2)) })
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Error(err)
		}
	}
	if success != 1 || conflict != count-1 {
		t.Fatalf("success %d conflict %d", success, conflict)
	}
	all, err := s.Versions(ctx, key)
	if err != nil || len(all) != 2 {
		t.Fatalf("history %+v: %v", all, err)
	}
}
func TestReadOnlyCloneFallbackIsStableAndReadsDoNotCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	common := t.TempDir()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	if err := os.Chmod(common, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(common, 0o700) })
	s, err := Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root() == filepath.Join(common, "baum", "reviews") {
		t.Fatal("did not use fallback")
	}
	key := Key{"owner/repo", 7}
	if err := s.Put(context.Background(), key, 0, version(key, 1)); err != nil {
		t.Fatal(err)
	}
	// Becoming writable must not lose the fallback's history.
	_ = os.Chmod(common, 0o700)
	again, err := Open(common, "", false)
	if err != nil || again.Root() != s.Root() {
		t.Fatalf("fallback lost: %+v %v", again, err)
	}
	if _, err := again.Latest(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	absent, err := Open(fresh, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := absent.Latest(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(fresh)
	if len(entries) != 0 {
		t.Fatal("read created files")
	}
}
func TestReadOnlyExistingStoreNeverHidesHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	common := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, err := Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"owner/repo", 7}
	if err := s.Put(context.Background(), key, 0, version(key, 1)); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(s.path(), 0o400)
	_ = os.Chmod(s.Root(), 0o500)
	t.Cleanup(func() { _ = os.Chmod(s.Root(), 0o700); _ = os.Chmod(s.path(), 0o600) })
	read, err := Open(common, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := read.Latest(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(common, "", true); err == nil {
		t.Fatal("write should fail for existing read-only store")
	}
	entries, _ := os.ReadDir(os.Getenv("XDG_DATA_HOME"))
	if len(entries) != 0 {
		t.Fatal("hid existing history in fallback")
	}
}
func TestExplicitStoreNeverFallsBack(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), filepath.Join(file, "store"), true); err == nil {
		t.Fatal("explicit failure fell back")
	}
	special := filepath.Join(t.TempDir(), "store?# name")
	s, err := Open("", special, true)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"owner/repo", 7}
	if err := s.Put(context.Background(), key, 0, version(key, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshFencesConcurrentWritersAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	s := opened(t)
	key := Key{"owner/repo", 7}
	first, second := version(key, 1), version(key, 2)
	if err := s.Put(ctx, key, 0, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, key, 1, second); err != nil {
		t.Fatal(err)
	}
	changed := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":2},"refreshed":true}`)
	if err := s.RefreshCurrent(ctx, key, first, first.Document); !errors.Is(err, ErrConflict) {
		t.Fatalf("refreshed older version: %v", err)
	}
	if err := s.RefreshCurrent(ctx, key, second, changed); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCurrent(ctx, key, second, second.Document); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrote concurrent refresh: %v", err)
	}
	got, err := s.Latest(ctx, key)
	if err != nil || string(got.Document) != string(changed) || got.Metadata != second.Metadata {
		t.Fatalf("latest: %+v %v", got, err)
	}
	old, err := s.Get(ctx, key, 1)
	if err != nil || string(old.Document) != string(first.Document) {
		t.Fatalf("history changed: %+v %v", old, err)
	}
	if err := s.Put(ctx, key, 2, version(key, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCurrent(ctx, key, got, second.Document); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrote new generation: %v", err)
	}
}

func TestConcurrentRefreshesCompareExactPreviousDocument(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	saved := version(key, 1)
	if err := s.Put(ctx, key, 0, saved); err != nil {
		t.Fatal(err)
	}
	const count = 8
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			<-start
			changed := []byte(fmt.Sprintf(`{"review":{"repository":"owner/repo","pull_request":7,"version":1},"refresh":%d}`, i))
			results <- s.RefreshCurrent(ctx, key, saved, changed)
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("published %d concurrent refreshes", successes)
	}
}

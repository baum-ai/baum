package uiserver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (s *Server) published() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence
}

// subscribe stands for a connected event stream.
func (s *Server) subscribe() func() {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	select {
	case s.joined <- struct{}{}:
	default:
	}
	return func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }
}

func TestIndexWatcherNotifiesOnlyChangedSnapshots(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.subscribe()()
	var value atomic.Int64
	reads := make(chan struct{}, 20)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.WatchIndex(ctx, time.Millisecond, IndexWatch{Snapshot: func(context.Context) (any, uint64, error) {
			v := value.Load()
			reads <- struct{}{}
			return v, 0, nil
		}})
	}()
	defer func() { stop(); <-done }()
	wait := func(want uint64) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			got := s.published()
			if got == want {
				return
			}
			select {
			case <-reads:
			case <-deadline:
				t.Fatalf("event count %d want %d", got, want)
			}
		}
	}
	wait(1)
	for range 3 {
		<-reads
	}
	if n := s.published(); n != 1 {
		t.Fatal("unchanged index emitted", n)
	}
	value.Store(2)
	wait(2)
}

// fakeIndex stands for reviewindex.Cache: a version that moves when the
// snapshot is rebuilt, and a fingerprint of files.
type fakeIndex struct {
	mu          sync.Mutex
	fingerprint string
	content     int
	version     uint64
	fresh       bool
	builds      int
	ticks       int
	invalidated int
}

func (f *fakeIndex) watch() IndexWatch {
	return IndexWatch{
		Fingerprint: func(context.Context) string {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.ticks++
			return f.fingerprint
		},
		Invalidate: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.invalidated++
			f.fresh = false
		},
		Fresh: func() (uint64, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.version, f.fresh
		},
		Snapshot: func(context.Context) (any, uint64, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.fresh {
				f.builds++
				f.version++
				f.fresh = true
			}
			return f.content, f.version, nil
		},
	}
}

func (f *fakeIndex) state() (builds, ticks, invalidated int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds, f.ticks, f.invalidated
}

func (f *fakeIndex) set(change func(*fakeIndex)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func TestIndexWatcherReadsOnlyWhenFingerprintOrCacheMoves(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.subscribe()()
	index := &fakeIndex{fingerprint: "a"}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.WatchIndex(ctx, time.Millisecond, index.watch())
	}()
	defer func() { stop(); <-done }()
	until := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	ticksAfter := func(n int) func() bool {
		return func() bool { _, ticks, _ := index.state(); return ticks >= n }
	}

	until("first tick publishes", func() bool { return s.published() == 1 })
	_, ticks, _ := index.state()
	until("ticks", ticksAfter(ticks+5))
	if builds, _, invalidated := index.state(); builds != 1 || invalidated != 0 {
		t.Fatalf("unchanged fingerprint: builds=%d invalidated=%d", builds, invalidated)
	}

	// A file write with the same content rebuilds but publishes nothing.
	index.set(func(f *fakeIndex) { f.fingerprint = "b" })
	until("rebuild after a write", func() bool { b, _, _ := index.state(); return b == 2 })
	_, ticks, _ = index.state()
	until("ticks", ticksAfter(ticks+5))
	if builds, _, invalidated := index.state(); builds != 2 || invalidated != 1 || s.published() != 1 {
		t.Fatalf("same content: builds=%d invalidated=%d events=%d", builds, invalidated, s.published())
	}

	// A write that changes the content publishes once.
	index.set(func(f *fakeIndex) { f.fingerprint, f.content = "c", 1 })
	until("publish after a change", func() bool { return s.published() == 2 })

	// An expired cache is read again without an invalidation.
	index.set(func(f *fakeIndex) { f.fresh, f.content = false, 2 })
	until("publish after expiry", func() bool { return s.published() == 3 })
	if _, _, invalidated := index.state(); invalidated != 2 {
		t.Fatal("expiry invalidated", invalidated)
	}

	// A snapshot another reader rebuilt is hashed without a build.
	index.set(func(f *fakeIndex) { f.version++; f.content = 3 })
	until("publish after another reader's rebuild", func() bool { return s.published() == 4 })
	if builds, _, _ := index.state(); builds != 4 {
		t.Fatal("builds", builds)
	}
}

func TestIndexWatcherBuildsNothingWithoutSubscribersUntilOneConnects(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	index := &fakeIndex{fingerprint: "a"}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.WatchIndex(ctx, time.Millisecond, index.watch())
	}()
	defer func() { stop(); <-done }()
	until := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	until("ticks", func() bool { _, ticks, _ := index.state(); return ticks >= 5 })
	if builds, _, _ := index.state(); builds != 0 || s.published() != 0 {
		t.Fatalf("idle watcher built %d times, published %d", builds, s.published())
	}
	// A change while idle invalidates, so the next reader builds, and builds
	// nothing itself.
	index.set(func(f *fakeIndex) { f.fingerprint = "b" })
	until("invalidated", func() bool { _, _, invalidated := index.state(); return invalidated == 1 })
	if builds, _, _ := index.state(); builds != 0 {
		t.Fatal("idle watcher built after a change", builds)
	}
	unsubscribe := s.subscribe()
	defer unsubscribe()
	until("published after a stream connects", func() bool { return s.published() == 1 })
	if builds, _, _ := index.state(); builds != 1 {
		t.Fatal("builds", builds)
	}
}

// A store written after the last idle tick and before the first stream
// connects must still invalidate, though nothing was published yet.
func TestIndexWatcherInvalidatesChangeBetweenIdleTickAndFirstSubscriber(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	index := &fakeIndex{fingerprint: "a", fresh: true, version: 1}
	// A long interval: only the connect wakes the watcher after the first tick.
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.WatchIndex(ctx, time.Hour, index.watch())
	}()
	defer func() { stop(); <-done }()
	until := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	until("idle tick", func() bool { _, ticks, _ := index.state(); return ticks >= 1 })
	index.set(func(f *fakeIndex) { f.fingerprint = "b" })
	unsubscribe := s.subscribe()
	defer unsubscribe()
	until("invalidated on connect", func() bool { _, _, invalidated := index.state(); return invalidated == 1 })
	until("published", func() bool { return s.published() == 1 })
}

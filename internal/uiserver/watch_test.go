package uiserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndexWatcherNotifiesOnlyChangedSnapshots(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	var value atomic.Int64
	reads := make(chan struct{}, 20)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.WatchIndex(ctx, time.Millisecond, func(context.Context) (any, error) { v := value.Load(); reads <- struct{}{}; return v, nil })
	}()
	defer func() { stop(); <-done }()
	wait := func(want uint64) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			s.mu.Lock()
			got := s.sequence
			s.mu.Unlock()
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
	s.mu.Lock()
	n := s.sequence
	s.mu.Unlock()
	if n != 1 {
		t.Fatal("unchanged index emitted", n)
	}
	value.Store(2)
	wait(2)
}

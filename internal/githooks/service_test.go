package githooks

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu    sync.Mutex
	roots []string
	seen  chan string
}

func (r *recorder) ensure(_ context.Context, root string) (Report, error) {
	r.mu.Lock()
	r.roots = append(r.roots, root)
	r.mu.Unlock()
	r.seen <- root
	if root == "/fails" {
		return Report{}, errors.New("boom")
	}
	return Report{Hooks: []HookResult{{Hook: "post-commit", Outcome: Installed}}}, nil
}

func next(t *testing.T, r *recorder) string {
	t.Helper()
	select {
	case root := <-r.seen:
		return root
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was ensured")
		return ""
	}
}

func TestServiceEnsuresAvailableClonesAtStartAndPeriodically(t *testing.T) {
	r := &recorder{seen: make(chan string, 16)}
	s := &Service{Paths: func(context.Context) ([]string, error) { return []string{"/a", "/fails", "/b"}, nil }, Ensure: r.ensure, Every: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go s.Run(ctx)
	var first []string
	for range 3 {
		first = append(first, next(t, r))
	}
	if !slices.Equal(first, []string{"/a", "/fails", "/b"}) {
		t.Fatalf("first sweep = %v (a failing clone must not stop the others)", first)
	}
	// The next sweep follows on its own.
	if got := next(t, r); got != "/a" {
		t.Fatalf("second sweep started with %q", got)
	}
}

func TestServiceEnsuresARegisteredCloneWithoutBlocking(t *testing.T) {
	r := &recorder{seen: make(chan string)}
	s := &Service{Paths: func(context.Context) ([]string, error) { return nil, nil }, Ensure: r.ensure}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go s.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		ready := s.run != nil
		s.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// The unbuffered fake blocks until read: Added must return first.
	done := make(chan struct{})
	go func() { s.Added("/new"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Added blocked on the ensure")
	}
	if got := next(t, r); got != "/new" {
		t.Fatalf("ensured %q", got)
	}
}

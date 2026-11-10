package githooks

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type recorder struct{ seen chan string }

func (r *recorder) ensure(_ context.Context, root string) (Report, error) {
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
		t.Fatal("nothing was handled")
		return ""
	}
}

func startService(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready := s.run != nil
		s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service did not start")
}

func TestServiceEnsuresAvailableClonesAtStartAndPeriodically(t *testing.T) {
	r := &recorder{seen: make(chan string, 16)}
	s := &Service{Clones: func(context.Context) ([]Clone, error) {
		return []Clone{{ID: "1", Path: "/a"}, {ID: "2", Path: "/fails"}, {ID: "3", Path: "/b"}}, nil
	}, Ensure: r.ensure, Every: 10 * time.Millisecond}
	startService(t, s)
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
	s := &Service{Clones: func(context.Context) ([]Clone, error) { return nil, nil }, Ensure: r.ensure}
	startService(t, s)
	// The unbuffered fake blocks until read: Added must return first.
	done := make(chan struct{})
	go func() { s.Added(Clone{ID: "9", Path: "/new"}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Added blocked on the ensure")
	}
	if got := next(t, r); got != "/new" {
		t.Fatalf("ensured %q", got)
	}
}

func TestServiceRestoresARemovedCloneUnlessAnotherSharesItsHooks(t *testing.T) {
	restored := make(chan string, 4)
	registered := []Clone{{ID: "linked", Path: "/linked", CommonDir: "/shared/.git"}}
	s := &Service{
		Clones:     func(context.Context) ([]Clone, error) { return nil, nil },
		Registered: func(context.Context) ([]Clone, error) { return registered, nil },
		Restore:    func(_ context.Context, root string) (Report, error) { restored <- root; return Report{}, nil },
	}
	startService(t, s)
	s.Removed(Clone{ID: "main", Path: "/main", CommonDir: "/shared/.git"})
	s.Removed(Clone{ID: "other", Path: "/other", CommonDir: "/other/.git"})
	select {
	case got := <-restored:
		if got != "/other" {
			t.Fatalf("restored %q, which a registered clone shares", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was restored")
	}
	select {
	case got := <-restored:
		t.Fatalf("also restored %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

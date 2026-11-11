package githooks

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeRegistry is a registry the test changes between calls.
type fakeRegistry struct {
	mu      sync.Mutex
	clones  []Clone
	handled chan string
}

func (f *fakeRegistry) set(clones ...Clone) {
	f.mu.Lock()
	f.clones = clones
	f.mu.Unlock()
}

func (f *fakeRegistry) list(context.Context) ([]Clone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Clone(nil), f.clones...), nil
}

func (f *fakeRegistry) service() *Service {
	return &Service{
		Clones:     f.list,
		Registered: f.list,
		Ensure: func(_ context.Context, root string) (Report, error) {
			f.handled <- "ensure " + root
			return Report{}, nil
		},
		Restore: func(_ context.Context, root string) (Report, error) {
			f.handled <- "restore " + root
			return Report{}, nil
		},
	}
}

func (f *fakeRegistry) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.handled:
		if got != want {
			t.Fatalf("handled %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing handled, want %q", want)
	}
}

func (f *fakeRegistry) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case got := <-f.handled:
		t.Fatalf("handled %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// A clone removed while the sweep waits for its turn is not ensured again.
func TestSweepSkipsACloneRemovedMeanwhile(t *testing.T) {
	f := &fakeRegistry{handled: make(chan string, 8)}
	f.set(Clone{ID: "a", Path: "/a", CommonDir: "/a/.git"})
	s := f.service()
	// The clone is removed right after the sweep listed it.
	s.Clones = func(ctx context.Context) ([]Clone, error) {
		clones, err := f.list(ctx)
		f.set()
		return clones, err
	}
	s.All(t.Context())
	f.expectNothing(t)
}

// Removing a clone and registering the same path again leaves its hooks.
func TestRemovedThenReaddedCloneKeepsItsHooks(t *testing.T) {
	f := &fakeRegistry{handled: make(chan string, 8)}
	s := f.service()
	startService(t, s)
	f.set(Clone{ID: "second", Path: "/a", CommonDir: "/a/.git"})
	s.Removed(Clone{ID: "first", Path: "/a", CommonDir: "/a/.git"})
	f.expectNothing(t)
	// Itself still registered, too.
	s.Removed(Clone{ID: "second", Path: "/a", CommonDir: "/a/.git"})
	f.expectNothing(t)
	f.set()
	s.Removed(Clone{ID: "second", Path: "/a", CommonDir: "/a/.git"})
	f.expect(t, "restore /a")
}

// A removal before Run is not lost.
func TestRemovedBeforeRunIsRestoredWhenItStarts(t *testing.T) {
	f := &fakeRegistry{handled: make(chan string, 8)}
	s := f.service()
	s.Removed(Clone{ID: "gone", Path: "/gone", CommonDir: "/gone/.git"})
	f.expectNothing(t)
	startService(t, s)
	f.expect(t, "restore /gone")
}

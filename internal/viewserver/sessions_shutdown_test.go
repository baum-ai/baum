package viewserver

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/locallink"
)

func TestFailedAnswersAreKeptBriefly(t *testing.T) {
	l := &linker{reply: func(int) (locallink.Result, error) { return locallink.Result{}, errors.New("slow store") }}
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Sessions: l.fn})
	for range 3 {
		if _, got := fetchSessions(t, base); got["code"] != "error" {
			t.Fatalf("response = %v", got)
		}
	}
	if n := l.calls.Load(); n != 1 {
		t.Errorf("calls = %d: a reload storm must not recompute a failed answer", n)
	}
}

// TestShutdownWaitsForARunningComputation checks that stopping the server
// during a computation does not return before the computation has cleaned up,
// as the real one does by removing its private repository.
func TestShutdownWaitsForARunningComputation(t *testing.T) {
	scratch := t.TempDir()
	started := make(chan struct{})
	fn := func(ctx context.Context, _ Review) (locallink.Result, error) {
		mirror := filepath.Join(scratch, "baum-git-test")
		if err := os.Mkdir(mirror, 0o700); err != nil {
			return locallink.Result{}, err
		}
		close(started)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond) // winding down takes a moment
		_ = os.Remove(mirror)
		return locallink.Result{}, ctx.Err()
	}
	server, err := New(Options{Document: []byte(reviewDocument(headA, "one")), Sessions: fn})
	if err != nil {
		t.Fatal(err)
	}
	url, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()

	go func() { _, _ = http.Get(url + "api/sessions") }()
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("the computation never started")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("Serve did not return")
	}
	if _, err := os.Stat(filepath.Join(scratch, "baum-git-test")); err == nil {
		t.Error("the computation's private repository was left behind")
	}
}

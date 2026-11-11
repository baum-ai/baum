package sessions

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// sizedSessions writes n Codex rollouts of the given size each.
func sizedSessions(t *testing.T, n int, size int) []collector.Session {
	t.Helper()
	dir := t.TempDir()
	var out []collector.Session
	for i := range n {
		path := filepath.Join(dir, string(rune('a'+i))+".jsonl")
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: filepath.Base(path), Kind: model.KindTranscript}}})
	}
	return out
}

// observer counts concurrent decodes and the bytes they hold. Each decode
// waits for a release, so the test controls how many are in flight.
type observer struct {
	mu         sync.Mutex
	running    int
	bytes      int64
	maxRunning int
	maxBytes   int64
	release    chan struct{}
}

func installObserver(t *testing.T) *observer {
	o := &observer{release: make(chan struct{})}
	decodeObserver = func(cost int64) func() {
		o.mu.Lock()
		o.running++
		o.bytes += cost
		o.maxRunning = max(o.maxRunning, o.running)
		o.maxBytes = max(o.maxBytes, o.bytes)
		o.mu.Unlock()
		<-o.release
		return func() {
			o.mu.Lock()
			o.running--
			o.bytes -= cost
			o.mu.Unlock()
		}
	}
	t.Cleanup(func() { decodeObserver = nil })
	return o
}

// drain lets decodes through until done is closed.
func (o *observer) drain(done <-chan struct{}) {
	for {
		select {
		case o.release <- struct{}{}:
		case <-done:
			return
		}
	}
}

func useBudget(t *testing.T, size int64) {
	t.Helper()
	old := decodeBudget
	decodeBudget = newDecodeBudget(size)
	t.Cleanup(func() { decodeBudget = old })
	useCache(t, 0)
}

func TestBackgroundLoadDecodesAtMostTwoSessions(t *testing.T) {
	useBudget(t, DefaultDecodeBudgetBytes)
	o := installObserver(t)
	done := make(chan struct{})
	go o.drain(done)
	_, err := mapAll(WithBackground(context.Background()), sizedSessions(t, 6, 10))
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if o.maxRunning > BackgroundParallel || o.maxRunning == 0 {
		t.Errorf("a background load ran %d decodes at once, want 1 to %d", o.maxRunning, BackgroundParallel)
	}
	if got := parallelFor(context.Background()); got != maxParallel() {
		t.Errorf("interactive bound = %d, want %d", got, maxParallel())
	}
}

func TestDecodeBudgetBoundsConcurrentBytes(t *testing.T) {
	useBudget(t, 100)
	o := installObserver(t)
	done := make(chan struct{})
	go o.drain(done)
	_, err := mapAll(context.Background(), sizedSessions(t, 6, 40))
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if o.maxBytes > 100 || o.maxBytes == 0 {
		t.Errorf("decoded %d bytes at once, budget is 100", o.maxBytes)
	}
}

func TestOversizedSessionMapsAlone(t *testing.T) {
	useBudget(t, 10)
	o := installObserver(t)
	done := make(chan struct{})
	go o.drain(done)
	outcomes, err := mapAll(context.Background(), sizedSessions(t, 3, 50))
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 || o.maxRunning != 1 {
		t.Errorf("%d outcomes, %d at once; oversized sessions run one at a time", len(outcomes), o.maxRunning)
	}
}

func TestCancelledWaitForBudgetReturnsPromptly(t *testing.T) {
	useBudget(t, 100)
	hold, err := decodeBudget.acquire(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	found := sizedSessions(t, 1, 40)
	go func() {
		_, err := mapAll(ctx, found)
		result <- err
	}()
	// Cancelling before or after the wait begins both end it.
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Error("a cancelled wait returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled wait for budget did not return")
	}
}

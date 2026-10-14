package locallink

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baumai/baum/internal/gitlocal"
)

type listing map[string]gitlocal.ChangedFile

// loader counts its calls and answers with next, after waiting for hold when
// it is set.
type loader struct {
	calls atomic.Int32
	hold  chan struct{}
	next  func(call int) (listing, error)
}

func (l *loader) load(context.Context) (map[string]gitlocal.ChangedFile, error) {
	n := int(l.calls.Add(1))
	if l.hold != nil {
		<-l.hold
	}
	if l.next != nil {
		return l.next(n)
	}
	return listing{"a.go": {Path: "a.go"}}, nil
}

func TestChangedFilesAreComputedOnceForConcurrentRequests(t *testing.T) {
	var p Patcher
	l := &loader{hold: make(chan struct{})}
	const askers = 8
	results := make(chan map[string]gitlocal.ChangedFile, askers)
	var started sync.WaitGroup
	for range askers {
		started.Add(1)
		go func() {
			started.Done()
			files, err := p.changedFiles(context.Background(), "b1", "h1", l.load)
			if err != nil {
				t.Error(err)
			}
			results <- files
		}()
	}
	started.Wait()
	time.Sleep(100 * time.Millisecond) // time for every asker to reach the wait
	close(l.hold)
	for range askers {
		if files := <-results; len(files) != 1 {
			t.Errorf("files = %v", files)
		}
	}
	if n := l.calls.Load(); n != 1 {
		t.Errorf("computed %d times, want 1", n)
	}
	if _, err := p.changedFiles(context.Background(), "b1", "h1", l.load); err != nil || l.calls.Load() != 1 {
		t.Errorf("a later request: err %v, computed %d times", err, l.calls.Load())
	}
}

func TestAFailedListingIsNotKept(t *testing.T) {
	var p Patcher
	l := &loader{next: func(call int) (listing, error) {
		if call == 1 {
			return nil, errors.New("git failed")
		}
		return listing{"a.go": {Path: "a.go"}}, nil
	}}
	if _, err := p.changedFiles(context.Background(), "b1", "h1", l.load); err == nil {
		t.Fatal("the failure was not reported")
	}
	files, err := p.changedFiles(context.Background(), "b1", "h1", l.load)
	if err != nil || len(files) != 1 || l.calls.Load() != 2 {
		t.Fatalf("retry: files %v, err %v, computed %d times", files, err, l.calls.Load())
	}
	if _, err := p.changedFiles(context.Background(), "b1", "h1", l.load); err != nil || l.calls.Load() != 2 {
		t.Errorf("the success was not kept: err %v, computed %d times", err, l.calls.Load())
	}
}

func TestANewBaseOrHeadIsComputedAgain(t *testing.T) {
	var p Patcher
	l := &loader{}
	for _, key := range [][2]string{{"b1", "h1"}, {"b1", "h1"}, {"b1", "h2"}, {"b2", "h2"}, {"b2", "h2"}} {
		if _, err := p.changedFiles(context.Background(), key[0], key[1], l.load); err != nil {
			t.Fatal(err)
		}
	}
	if n := l.calls.Load(); n != 3 {
		t.Errorf("computed %d times, want 3: once for each of the three ranges", n)
	}
}

package facts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// fakeGitHub answers builds with whatever head GitHub reports, counting them.
type fakeGitHub struct {
	mu     sync.Mutex
	head   string
	base   string
	err    error
	builds int
	gate   chan struct{}
}

func (f *fakeGitHub) build(ctx context.Context, clone repos.Clone, number int) (Built, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	if f.err != nil {
		return Built{}, f.err
	}
	document := fmt.Sprintf(`{"schema":"baum.facts/1","review":{"repository":%q,"pull_request":%d,"head":%q,"base":%q},"build":%d}`, clone.Repository, number, f.head, f.base, f.builds)
	return Built{Document: []byte(document), Head: f.head, Base: f.base}, nil
}

func (f *fakeGitHub) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.builds }

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

var clone = repos.Clone{ID: "c1", Repository: "acme/widgets", Path: "/src/widgets"}

func newService(gh *fakeGitHub, c *clock) *Service {
	return &Service{Build: gh.build, Window: time.Minute, Now: c.now}
}

func TestFactsAreCachedWithinTheWindowForTheSameHead(t *testing.T) {
	gh, c := &fakeGitHub{head: "aaa", base: "bbb"}, &clock{at: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := newService(gh, c)
	first, err := s.Facts(context.Background(), clone, 7, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	c.at = c.at.Add(59 * time.Second)
	again, err := s.Facts(context.Background(), clone, 7, "aaa")
	if err != nil || string(again) != string(first) || gh.count() != 1 {
		t.Fatalf("a request within the window read GitHub again: builds %d, %v", gh.count(), err)
	}
	if string(s.Cached(clone, 7)) != string(first) || s.Cached(clone, 8) != nil {
		t.Error("Cached does not answer the facts last built")
	}
	// Once the window passes, a page that keeps asking reads GitHub once more.
	c.at = c.at.Add(2 * time.Second)
	if _, err := s.Facts(context.Background(), clone, 7, "aaa"); err != nil || gh.count() != 2 {
		t.Fatalf("after the window: builds %d, %v", gh.count(), err)
	}
	// Another pull request or clone has facts of its own.
	if _, err := s.Facts(context.Background(), repos.Clone{ID: "c2", Repository: "acme/widgets"}, 7, "aaa"); err != nil || gh.count() != 3 {
		t.Fatalf("another clone shared the facts: builds %d, %v", gh.count(), err)
	}
}

func TestANewHeadRebuildsTheFactsAtOnce(t *testing.T) {
	gh, c := &fakeGitHub{head: "aaa", base: "bbb"}, &clock{at: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := newService(gh, c)
	if _, err := s.Facts(context.Background(), clone, 7, "aaa"); err != nil {
		t.Fatal(err)
	}
	gh.head = "ccc"
	c.at = c.at.Add(5 * time.Second)
	got, err := s.Facts(context.Background(), clone, 7, "ccc")
	if err != nil || gh.count() != 2 || string(s.Cached(clone, 7)) != string(got) {
		t.Fatalf("a new head did not rebuild: builds %d, %v", gh.count(), err)
	}
	// GitHub may lag discovery: the head asked about stands for the window
	// even when the facts describe another, so the page does not rebuild on
	// every request.
	gh.head = "ccc"
	if _, err := s.Facts(context.Background(), clone, 7, "ddd"); err != nil || gh.count() != 3 {
		t.Fatalf("builds %d, %v", gh.count(), err)
	}
	if _, err := s.Facts(context.Background(), clone, 7, "ddd"); err != nil || gh.count() != 3 {
		t.Fatalf("a lagging head rebuilt within the window: builds %d, %v", gh.count(), err)
	}
}

func TestAFailedBuildStandsForTheWindow(t *testing.T) {
	gh, c := &fakeGitHub{head: "aaa", base: "bbb"}, &clock{at: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := newService(gh, c)
	built, err := s.Facts(context.Background(), clone, 7, "aaa")
	if err != nil {
		t.Fatal(err)
	}
	gh.err = errors.New("GitHub is down")
	c.at = c.at.Add(2 * time.Minute)
	// The facts built before still answer for their head.
	if got, err := s.Facts(context.Background(), clone, 7, "aaa"); err != nil || string(got) != string(built) || gh.count() != 2 {
		t.Fatalf("after a failure: %s, %v, builds %d", got, err, gh.count())
	}
	if _, err := s.Facts(context.Background(), clone, 7, "bbb"); err == nil || gh.count() != 3 {
		t.Fatalf("a failed build for a new head answered: %v, builds %d", err, gh.count())
	}
	if _, err := s.Facts(context.Background(), clone, 7, "bbb"); err == nil || gh.count() != 3 {
		t.Fatalf("a failure was retried within the window: %v, builds %d", err, gh.count())
	}
}

func TestARateLimitPausesBuilds(t *testing.T) {
	gh, c := &fakeGitHub{head: "aaa", base: "bbb"}, &clock{at: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := newService(gh, c)
	var pause time.Time
	limited := map[string]time.Time{}
	s.Paused = func(host string) time.Time { return pause }
	s.Limited = func(host string, until time.Time) { limited[host] = until }
	gh.err = &github.RateLimitedError{ResumeAt: c.at.Add(time.Hour)}
	if _, err := s.Facts(context.Background(), clone, 7, "aaa"); !errors.As(err, new(*github.RateLimitedError)) {
		t.Fatalf("err = %v", err)
	}
	if limited["github.com"] != c.at.Add(time.Hour) {
		t.Errorf("the rate limit was not recorded for the background refresh: %v", limited)
	}
	gh.err = nil
	built, err := func() ([]byte, error) {
		c.at = c.at.Add(2 * time.Minute)
		return s.Facts(context.Background(), clone, 7, "aaa")
	}()
	if err != nil {
		t.Fatal(err)
	}
	// While paused, the facts held answer for their head and nothing else
	// reads GitHub.
	pause = c.at.Add(time.Hour)
	c.at = c.at.Add(2 * time.Minute)
	if got, err := s.Facts(context.Background(), clone, 7, "aaa"); err != nil || string(got) != string(built) {
		t.Fatalf("paused, same head: %s, %v", got, err)
	}
	if _, err := s.Facts(context.Background(), clone, 7, "ccc"); !errors.As(err, new(*github.RateLimitedError)) {
		t.Fatalf("paused, new head: %v", err)
	}
	if gh.count() != 2 {
		t.Errorf("builds = %d while paused", gh.count())
	}
}

func TestConcurrentRequestsShareOneBuild(t *testing.T) {
	gh, c := &fakeGitHub{head: "aaa", base: "bbb", gate: make(chan struct{})}, &clock{at: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	s := newService(gh, c)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := s.Facts(context.Background(), clone, 7, "aaa"); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(gh.gate)
	wg.Wait()
	if gh.count() != 1 {
		t.Errorf("builds = %d, want one shared", gh.count())
	}
	// A request that gives up does not cancel the build it shares.
	gh.gate = make(chan struct{})
	gh.head = "ccc"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Facts(ctx, clone, 7, "ccc"); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	close(gh.gate)
	deadline := time.Now().Add(5 * time.Second)
	for s.Cached(clone, 7) == nil || gh.count() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned build did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

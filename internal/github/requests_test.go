package github

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A request timeout bounds each request, not the read: a slow part fails
// the fetch once one of its requests runs out of time, and every request is
// counted, in the context it was made with and the ones it came from.
func TestRequestTimeoutFailsTheFetchAndRequestsAreCounted(t *testing.T) {
	routes := fixtureRoutes(t)
	threads := routes["PullRequestReviewThreads"]
	release := make(chan struct{})
	routes["PullRequestReviewThreads"] = func(request gqlRequest, call int) fakeResponse {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return threads(request, call)
	}
	fake := newFakeGitHub(t, routes)
	// Before the server closes, which waits for the held request.
	t.Cleanup(func() { close(release) })
	outer, total := CountRequests(context.Background())
	ctx, read := CountRequests(WithRequestTimeout(outer, 100*time.Millisecond))
	started := time.Now()
	_, err := fake.client(nil).Fetch(ctx, acme, 7, FetchOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a request timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("fetch took %v, want it to stop at the request timeout", elapsed)
	}
	if read() < 2 || read() != total() {
		t.Errorf("counted %d requests (outer %d), want the header and the parts", read(), total())
	}
}

// A fetch whose requests each answer within the request timeout completes,
// though together they take longer than it.
func TestRequestTimeoutIsPerRequest(t *testing.T) {
	routes := fixtureRoutes(t)
	for name, handler := range routes {
		routes[name] = func(request gqlRequest, call int) fakeResponse {
			time.Sleep(40 * time.Millisecond)
			return handler(request, call)
		}
	}
	fake := newFakeGitHub(t, routes)
	ctx := WithRequestTimeout(context.Background(), 200*time.Millisecond)
	started := time.Now()
	if _, err := fake.client(nil).Fetch(ctx, acme, 7, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Errorf("fetch took %v, want longer than one request's timeout", elapsed)
	}
}

// The parts after the header are read a few at a time.
func TestFetchReadsPartsConcurrently(t *testing.T) {
	routes := fixtureRoutes(t)
	var mu sync.Mutex
	inflight, most := 0, 0
	for name, handler := range routes {
		if name == "PullRequest" {
			continue
		}
		routes[name] = func(request gqlRequest, call int) fakeResponse {
			mu.Lock()
			inflight++
			most = max(most, inflight)
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			inflight--
			mu.Unlock()
			return handler(request, call)
		}
	}
	fake := newFakeGitHub(t, routes)
	evidence, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if most < 2 || most > partConcurrency {
		t.Errorf("at most %d requests at once, want 2 to %d", most, partConcurrency)
	}
	for _, source := range []string{SourceRequestedReviewers, SourceCommits, SourceComments, SourceReviews, SourceReviewThreads, SourceChecks} {
		if !evidence.Sources[source] {
			t.Errorf("source %s missing: %v", source, evidence.Sources)
		}
	}
}

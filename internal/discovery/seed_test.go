package discovery

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/github"
)

// A seeded cache is served, stale, under the credential that wrote it and
// under no other, and a poll that cannot reach GitHub keeps it.
func TestSeedIsServedUnderItsCredential(t *testing.T) {
	ctx := context.Background()
	dir := appdbtest.TempDir(t)
	client := github.New(func(context.Context, string) (string, error) { return "seeded", nil }, github.Options{})
	s := New(client, dir)
	snapshot := Snapshot{Repository: "Acme/App", Viewer: "dana", FetchedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NextPoll:     time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		PullRequests: []PullRequest{{Repository: "acme/app", Number: 7, Title: "Seeded", State: "OPEN"}}}
	if err := s.Seed(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	repo := github.Repository{Host: "github.com", Owner: "acme", Name: "app"}
	got, err := New(client, dir).Cached(ctx, repo)
	if err != nil || len(got.PullRequests) != 1 || got.PullRequests[0].Title != "Seeded" || got.Viewer != "dana" || !got.Stale {
		t.Fatalf("%+v %v", got, err)
	}
	// Before NextPoll a poll answers from the cache without a request.
	polled, err := New(client, dir).Poll(ctx, repo)
	if err != nil || len(polled.PullRequests) != 1 {
		t.Fatalf("%+v %v", polled, err)
	}
	other := github.New(func(context.Context, string) (string, error) { return "another", nil }, github.Options{})
	if _, err := New(other, dir).Cached(ctx, repo); err == nil {
		t.Fatal("another credential read the seeded cache")
	}
}

// failing refuses every request, as GitHub out of reach does.
type failing struct{ requests *int }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) {
	*f.requests++
	return nil, errors.New("offline")
}

// Offline, a forced poll and a probe fail, and the seeded pull requests
// stay, stale and with the error.
func TestSeedSurvivesFailedPolls(t *testing.T) {
	ctx := context.Background()
	dir := appdbtest.TempDir(t)
	requests := 0
	client := github.New(func(context.Context, string) (string, error) { return "seeded", nil }, github.Options{Transport: failing{&requests}})
	s := New(client, dir)
	repo := github.Repository{Host: "github.com", Owner: "acme", Name: "app"}
	if err := s.Seed(ctx, Snapshot{Repository: "acme/app", Viewer: "dana", NextPoll: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		PullRequests: []PullRequest{{Repository: "acme/app", Number: 7, Title: "Seeded", State: "OPEN"}, {Repository: "acme/app", Number: 9, Title: "Other", State: "OPEN"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		polled, err := s.PollNow(ctx, repo)
		if err == nil || len(polled.PullRequests) != 2 || !polled.Stale || polled.Error == "" {
			t.Fatalf("forced poll %d: %+v %v", i, polled, err)
		}
	}
	if _, err := s.Probe(ctx, []github.Repository{repo}); err == nil {
		t.Fatal("a probe with GitHub out of reach succeeded")
	}
	if requests == 0 {
		t.Fatal("no request was attempted")
	}
	for _, reader := range []*Service{s, New(client, dir)} {
		got, err := reader.Cached(ctx, repo)
		if err != nil || len(got.PullRequests) != 2 || got.PullRequests[0].Title != "Seeded" || !got.Stale || got.Error == "" {
			t.Fatalf("after the failed polls: %+v %v", got, err)
		}
	}
}

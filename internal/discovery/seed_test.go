package discovery

import (
	"context"
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

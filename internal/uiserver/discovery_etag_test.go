package uiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

type revisionedPRs struct {
	fakePRs
	writes   uint64
	cached   int
	nextPoll time.Time
}

func (f *revisionedPRs) Revision(context.Context, github.Repository) (discovery.Revision, error) {
	return discovery.Revision{Writes: f.writes}, nil
}

func (f *revisionedPRs) Cached(ctx context.Context, r github.Repository) (discovery.Snapshot, error) {
	f.cached++
	s, err := f.fakePRs.Cached(ctx, r)
	s.NextPoll = f.nextPoll
	s.PullRequests = []discovery.PullRequest{{Number: 7 + int(f.writes)}}
	return s, err
}

func TestDiscoveryHandlerEncodesPerRevision(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.Pick(context.Background(), "acme/bare", nil); err != nil {
		t.Fatal(err)
	}
	prs := &revisionedPRs{nextPoll: time.Now().Add(time.Hour)}
	h := DiscoveryHandler(registry, prs, nil)
	get := func(inm string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://local/api/prs", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		h.ServeHTTP(w, req)
		return w
	}
	first := get("")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", first.Code, first.Header())
	}
	if w := get(etag); w.Code != http.StatusNotModified {
		t.Fatalf("revalidate: %d", w.Code)
	}
	if prs.cached != 1 {
		t.Fatalf("Cached ran %d times for one revision", prs.cached)
	}
	prs.writes++
	if w := get(etag); w.Code != 200 || prs.cached != 2 {
		t.Fatalf("new revision: %d, %d reads", w.Code, prs.cached)
	}
	// A snapshot that reaches its next poll turns stale with no revision change.
	prs.nextPoll = time.Now().Add(-time.Second)
	prs.writes++
	get("")
	get("")
	if prs.cached != 4 {
		t.Fatalf("expired snapshot was reused: %d reads", prs.cached)
	}
}

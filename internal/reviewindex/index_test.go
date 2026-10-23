package reviewindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// registry lists its clones grouped by repository. A clone with neither ID nor
// store stands for a picked repository without a clone.
type registry []repos.Clone

func (r registry) Repositories(context.Context) ([]repos.Repository, error) {
	byName := map[string]int{}
	out := []repos.Repository{}
	for _, c := range r {
		i, ok := byName[c.Repository]
		if !ok {
			i = len(out)
			byName[c.Repository] = i
			out = append(out, repos.Repository{Repository: c.Repository})
		}
		if c.ID != "" || c.CommonDir != "" {
			out[i].Clones = append(out[i].Clones, c)
		}
	}
	return out, nil
}

type source map[string]discovery.Snapshot

func (s source) Cached(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	return s[r.String()], nil
}
func TestStatusesIsolationAndHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stores := map[string]*store.Store{}
	clones := registry{}
	discovered := source{}
	for _, repository := range []string{"acme/a", "acme/b"} {
		common := filepath.Join(root, repository)
		s, err := store.Open(common, "", true)
		if err != nil {
			t.Fatal(err)
		}
		stores[repository] = s
		clones = append(clones, repos.Clone{ID: repository, Repository: repository, CommonDir: common, Available: true})
		prs := []discovery.PullRequest{}
		for n := 1; n <= 6; n++ {
			prs = append(prs, discovery.PullRequest{Repository: repository, Number: n, HeadSHA: "head", State: "OPEN"})
		}
		discovered[repository] = discovery.Snapshot{PullRequests: prs}
	}
	put := func(repository string, n int, head, state string) {
		t.Helper()
		doc := map[string]any{"review": map[string]any{"repository": repository, "pull_request": n, "version": 1, "pr_state": state}, "summary": map[string]any{"risk_score": map[string]any{"score": 17}}}
		raw, _ := json.Marshal(doc)
		err := stores[repository].Put(ctx, store.Key{Repository: repository, Number: n}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: head, Backend: "claude", Model: "model", GeneratedAt: "2026-10-15T12:00:00Z"}, Document: raw})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("acme/a", 2, "head", "open")
	put("acme/a", 3, "old", "open")
	put("acme/a", 7, "old", "merged")
	put("acme/a", 8, "old", "open")
	for _, n := range []int{4, 5} {
		id, err := stores["acme/a"].BeginRun(ctx, store.Key{Repository: "acme/a", Number: n}, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if n == 5 {
			if err = stores["acme/a"].FinishRun(ctx, id, "budget"); err != nil {
				t.Fatal(err)
			}
		}
	}
	service := &Service{Registry: clones, Discovery: discovered}
	snapshot, err := service.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[int]string{1: "no_review", 2: "current", 3: "outdated", 4: "generating", 5: "failed", 6: "no_review", 7: "unknown", 8: "unknown"}
	for _, row := range snapshot.Entries {
		if row.PullRequest.Repository == "acme/b" {
			if row.Status != "no_review" {
				t.Fatalf("repository leaked %+v", row)
			}
			continue
		}
		if row.Status != expected[row.PullRequest.Number] {
			t.Fatalf("row=%+v", row)
		}
		if row.PullRequest.Number == 7 && (!row.Historical || row.PullRequest.State != "MERGED") {
			t.Fatal(row)
		}
		if row.PullRequest.Number == 8 && row.PullRequest.State != "UNKNOWN" {
			t.Fatal(row)
		}
		if row.PullRequest.Number == 2 && (row.Latest.RecordedCostUSD != nil || row.VersionCount != 1 || string(row.RiskScore) != `{"score":17}`) {
			t.Fatal(row)
		}
	}
	for _, query := range []string{"", "?history=1"} {
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/index"+query, nil))
		var got Snapshot
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := 12
		if query != "" {
			want = 14
		}
		if len(got.Entries) != want {
			t.Fatalf("query=%s entries=%d", query, len(got.Entries))
		}
	}
}

type broken struct{}

func (broken) Snapshot(context.Context) ([]store.Summary, error) {
	return nil, errors.New("private filesystem path")
}
func TestUnreadableStoreAndWorktreeDeduplication(t *testing.T) {
	clone := repos.Clone{ID: "one", Repository: "a/b", CommonDir: "same", Available: true}
	other := clone
	other.ID = "two"
	calls := 0
	s := &Service{Registry: registry{clone, other}, Discovery: source{"a/b": {PullRequests: []discovery.PullRequest{{Number: 7, HeadSHA: "head"}}}}, OpenStore: func(string) (Reader, error) { calls++; return broken{}, nil }}
	got, err := s.Build(context.Background())
	if err != nil || calls != 1 || len(got.Problems) != 1 || len(got.Entries) != 1 || got.Entries[0].Status != "unknown" || got.Entries[0].StoreError != "store_unreadable" {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}

type fakeReader []store.Summary

func (f fakeReader) Snapshot(context.Context) ([]store.Summary, error) { return f, nil }

func rowsByClone(entries []Entry) map[string]Entry {
	out := map[string]Entry{}
	for _, e := range entries {
		out[e.CloneID] = e
	}
	return out
}

func TestIndependentClonesGetOwnNoReviewRows(t *testing.T) {
	pr := discovery.PullRequest{Number: 7, HeadSHA: "head", State: "OPEN"}
	disc := source{"a/b": {PullRequests: []discovery.PullRequest{pr}}}
	clones := registry{
		{ID: "A", Repository: "a/b", CommonDir: "dirA", Available: true},
		{ID: "B", Repository: "a/b", CommonDir: "dirB", Available: true},
	}
	reviewed := fakeReader{{Key: store.Key{Repository: "a/b", Number: 7}, VersionCount: 1, Latest: &store.Metadata{Version: 1, Head: "head"}}}
	open := func(readers map[string]Reader) func(string) (Reader, error) {
		return func(dir string) (Reader, error) { return readers[dir], nil }
	}
	t.Run("reviewed in one", func(t *testing.T) {
		s := &Service{Registry: clones, Discovery: disc, OpenStore: open(map[string]Reader{"dirA": reviewed, "dirB": fakeReader{}})}
		got, err := s.Build(context.Background())
		rows := rowsByClone(got.Entries)
		if err != nil || len(got.Entries) != 2 || rows["A"].Status != "current" || rows["B"].Status != "no_review" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("both empty", func(t *testing.T) {
		s := &Service{Registry: clones, Discovery: disc, OpenStore: open(map[string]Reader{"dirA": fakeReader{}, "dirB": fakeReader{}})}
		got, err := s.Build(context.Background())
		rows := rowsByClone(got.Entries)
		if err != nil || len(got.Entries) != 2 || rows["A"].Status != "no_review" || rows["B"].Status != "no_review" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("linked worktrees share a store", func(t *testing.T) {
		shared := registry{
			{ID: "A", Repository: "a/b", CommonDir: "dirA", Available: true},
			{ID: "A-wt", Repository: "a/b", CommonDir: "dirA", Available: true},
		}
		s := &Service{Registry: shared, Discovery: disc, OpenStore: open(map[string]Reader{"dirA": fakeReader{}})}
		got, err := s.Build(context.Background())
		if err != nil || len(got.Entries) != 1 || got.Entries[0].CloneID != "A" || got.Entries[0].Status != "no_review" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func BenchmarkBuildManyRepositories(b *testing.B) {
	root := b.TempDir()
	clones := registry{}
	discovered := source{}
	ctx := context.Background()
	for repository := 0; repository < 20; repository++ {
		name := fmt.Sprintf("acme/repo-%d", repository)
		common := filepath.Join(root, name)
		versions, err := store.Open(common, "", true)
		if err != nil {
			b.Fatal(err)
		}
		clones = append(clones, repos.Clone{ID: name, Repository: name, CommonDir: common, Available: true})
		prs := []discovery.PullRequest{}
		for pr := 1; pr <= 10; pr++ {
			prs = append(prs, discovery.PullRequest{Repository: name, Number: pr, HeadSHA: "head", State: "OPEN"})
			var previous store.Version
			for version := 1; version <= 5; version++ {
				document := []byte(fmt.Sprintf(`{"review":{"repository":%q,"pull_request":%d,"version":%d},"padding":%q}`, name, pr, version, strings.Repeat("x", 32<<10)))
				next := store.Version{Metadata: store.Metadata{Version: version, Head: "head"}, Document: document}
				if err = versions.Put(ctx, store.Key{Repository: name, Number: pr}, previous, next); err != nil {
					b.Fatal(err)
				}
				previous = next
			}
		}
		discovered[name] = discovery.Snapshot{PullRequests: prs}
	}
	service := &Service{Registry: clones, Discovery: discovered}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot, err := service.Build(ctx)
		if err != nil || len(snapshot.Entries) != 200 {
			b.Fatalf("entries=%d err=%v", len(snapshot.Entries), err)
		}
	}
}

func TestCorruptStoreDoesNotHideOtherRepositoryHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	good := filepath.Join(root, "good")
	bad := filepath.Join(root, "bad")
	versions, err := store.Open(good, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = versions.Put(ctx, store.Key{Repository: "a/good", Number: 7}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: "head"}, Document: []byte(`{"review":{"repository":"a/good","pull_request":7,"version":1,"pr_state":"closed"}}`)}); err != nil {
		t.Fatal(err)
	}
	corrupt, err := store.Open(bad, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(corrupt.Root(), "versions.sqlite"), []byte("corrupt database"), 0600); err != nil {
		t.Fatal(err)
	}
	service := &Service{Registry: registry{{ID: "good", Repository: "a/good", CommonDir: good, Available: false}, {ID: "bad", Repository: "a/bad", CommonDir: bad, Available: true}}, Discovery: source{"a/bad": {PullRequests: []discovery.PullRequest{{Number: 7, HeadSHA: "head", State: "OPEN"}}}}}
	got, err := service.Build(ctx)
	if err != nil || len(got.Entries) != 2 || len(got.Problems) != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got.Entries[0].PullRequest.Repository != "a/bad" || got.Entries[0].StoreError != "store_unreadable" || got.Entries[1].PullRequest.State != "CLOSED" || !got.Entries[1].Historical {
		t.Fatal(got)
	}
}

func TestFreshMetadataOverridesDiscoveryAndRetainsHistoricalPRs(t *testing.T) {
	ctx := context.Background()
	common := t.TempDir()
	versions, err := store.Open(common, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 9} {
		doc, _ := json.Marshal(map[string]any{"review": map[string]any{"repository": "acme/a", "pull_request": n, "version": 1, "pr_state": "open"}})
		if err := versions.Put(ctx, store.Key{Repository: "acme/a", Number: n}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1, Head: "old"}, Document: doc}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	snapshot := discovery.Snapshot{FetchedAt: now.Add(-time.Second), PullRequests: []discovery.PullRequest{{Number: 1, HeadSHA: "old", State: "OPEN"}}, Observations: []discovery.Observation{{Number: 1, HeadSHA: "new", State: "OPEN", Checks: "failure", FetchedAt: now}, {Number: 9, HeadSHA: "old", State: "MERGED", Checks: "success", FetchedAt: now}}}
	s := Service{Registry: registry{{ID: "clone", Repository: "acme/a", CommonDir: common, Available: true}}, Discovery: source{"acme/a": snapshot}}
	got, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[0].Status != "outdated" || got.Entries[0].Checks != "failing" || got.Entries[1].PullRequest.State != "MERGED" || !got.Entries[1].Historical || got.Entries[1].Status != "current" || got.Entries[1].Stale {
		t.Fatalf("entries %+v", got.Entries)
	}
	snapshot.FetchedAt = now.Add(time.Second)
	snapshot.PullRequests[0].HeadSHA = "latest"
	s.Discovery = source{"acme/a": snapshot}
	got, err = s.Build(ctx)
	if err != nil || got.Entries[0].PullRequest.HeadSHA != "latest" || got.Entries[0].Checks != "" {
		t.Fatalf("old observation overwrote discovery %+v %v", got, err)
	}
}

func TestInboxStatesForEveryStoreOutcomeAndObservationPrecedence(t *testing.T) {
	count, minutes := 2, 15
	now := time.Now()
	pr := discovery.PullRequest{Number: 1, HeadSHA: "head", State: "OPEN", Review: "approved", Checks: "passing", Merge: "ready", Unresolved: &count, UnresolvedCapped: true}
	for _, tc := range []struct {
		name        string
		reader      Reader
		wantMinutes bool
	}{
		{"no review", fakeReader{}, false},
		{"saved", fakeReader{{Key: store.Key{Repository: "a/b", Number: 1}, Latest: &store.Metadata{Version: 1, Head: "head"}, ReviewMinutes: &minutes}}, true},
		{"unreadable", broken{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := discovery.Snapshot{PullRequests: []discovery.PullRequest{pr}, FetchedAt: now}
			service := Service{Registry: registry{{ID: "clone", Repository: "a/b", CommonDir: "store", Available: true}}, Discovery: source{"a/b": snap}, OpenStore: func(string) (Reader, error) { return tc.reader, nil }}
			got, err := service.Build(context.Background())
			if err != nil || len(got.Entries) != 1 {
				t.Fatal(got, err)
			}
			row := got.Entries[0]
			if row.Review != "approved" || row.Checks != "passing" || row.Merge != "ready" || row.Unresolved == nil || *row.Unresolved != 2 || !row.UnresolvedCapped || (row.ReviewMinutes != nil) != tc.wantMinutes {
				t.Fatalf("row=%+v", row)
			}
			snap.Observations = []discovery.Observation{{Number: 1, HeadSHA: "head", Checks: "failure", FetchedAt: now.Add(-time.Second)}}
			service.Discovery = source{"a/b": snap}
			got, err = service.Build(context.Background())
			if err != nil || got.Entries[0].Checks != "passing" {
				t.Fatal(got, err)
			}
			snap.Observations[0].FetchedAt = now.Add(time.Second)
			service.Discovery = source{"a/b": snap}
			got, err = service.Build(context.Background())
			if err != nil || got.Entries[0].Checks != "failing" || got.Entries[0].Review != "approved" {
				t.Fatal(got, err)
			}
			snap.Observations[0].HeadSHA = "new"
			service.Discovery = source{"a/b": snap}
			got, err = service.Build(context.Background())
			if err != nil || got.Entries[0].Review != "" || got.Entries[0].Merge != "" || got.Entries[0].Unresolved != nil || got.Entries[0].UnresolvedCapped {
				t.Fatal(got, err)
			}
		})
	}
}

// A picked repository without a clone lists its discovered pull requests,
// marked as needing a clone; repositories with an available clone are not.
func TestRepositoryWithoutACloneListsPullRequestsNeedingOne(t *testing.T) {
	pr := func(n int) []discovery.PullRequest {
		return []discovery.PullRequest{{Number: n, HeadSHA: "head", State: "OPEN"}}
	}
	service := &Service{
		Registry:  registry{{Repository: "acme/bare"}, {ID: "clone", Repository: "acme/app", CommonDir: "dir", Available: true}},
		Discovery: source{"acme/bare": {PullRequests: pr(3)}, "acme/app": {PullRequests: pr(4)}},
		OpenStore: func(string) (Reader, error) { return fakeReader{}, nil },
	}
	snapshot, err := service.Build(context.Background())
	if err != nil || len(snapshot.Problems) != 0 || len(snapshot.Entries) != 2 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	app, bare := snapshot.Entries[0], snapshot.Entries[1]
	if app.PullRequest.Repository != "acme/app" || app.CloneID != "clone" || app.NeedsClone || app.Status != "no_review" {
		t.Fatalf("repository with a clone: %+v", app)
	}
	if bare.PullRequest.Repository != "acme/bare" || bare.PullRequest.Number != 3 || bare.CloneID != "" || !bare.NeedsClone || bare.Status != "no_review" {
		t.Fatalf("repository without a clone: %+v", bare)
	}
	raw, _ := json.Marshal(snapshot)
	if !strings.Contains(string(raw), `"needs_clone":true`) || strings.Count(string(raw), "needs_clone") != 1 {
		t.Fatalf("%s", raw)
	}
}

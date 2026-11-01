package githubrepos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

type controlledLister struct {
	*fakeLister
	top    func(context.Context, string, int, *string) (github.ViewerRepositoryList, error)
	search func(context.Context, string, string) (github.ViewerRepositoryList, error)
	page   func(context.Context, string, *string) (github.ViewerRepositoryList, error)
}

func (l controlledLister) ViewerRepositories(ctx context.Context, host string, first int, cursor *string) (github.ViewerRepositoryList, error) {
	if l.top != nil {
		return l.top(ctx, host, first, cursor)
	}
	return l.fakeLister.ViewerRepositories(ctx, host, first, cursor)
}
func (l controlledLister) SearchRepositories(ctx context.Context, host, query string, scope github.SearchScope, cursor *string) (github.ViewerRepositoryList, error) {
	if l.page != nil {
		return l.page(ctx, query, cursor)
	}
	if l.search != nil {
		return l.search(ctx, host, query)
	}
	return l.fakeLister.SearchRepositories(ctx, host, query, scope, cursor)
}
func (h *harness) search(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := h.do("GET", path, "")
		if rec.Code != http.StatusOK || !decode[Listing](h.t, rec).Searching {
			return rec
		}
		if time.Now().After(deadline) {
			h.t.Fatal("repository search did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInitialEightReturnBeforeTheNextHundredAndUseTheirCursor(t *testing.T) {
	h := newHarness(t)
	requested := make(chan struct{})
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer released()
	var calls atomic.Int32
	h.service.Lister = controlledLister{fakeLister: h.lister, top: func(ctx context.Context, _ string, first int, cursor *string) (github.ViewerRepositoryList, error) {
		n := calls.Add(1)
		if n == 1 {
			if first != 8 || cursor != nil {
				t.Errorf("first request = %d, %v", first, cursor)
			}
			out := []github.ViewerRepository{}
			for i := range 8 {
				out = append(out, github.ViewerRepository{Repository: fmt.Sprintf("acme/top-%03d", i)})
			}
			next := "after-eight"
			return github.ViewerRepositoryList{Repositories: out, TotalCount: 500, NextCursor: &next}, nil
		}
		if n != 2 || first != 100 || cursor == nil || *cursor != "after-eight" {
			t.Errorf("next request = %d, %d, %v", n, first, cursor)
		}
		close(requested)
		select {
		case <-release:
		case <-ctx.Done():
			return github.ViewerRepositoryList{}, ctx.Err()
		}
		out := []github.ViewerRepository{}
		for i := 8; i < 108; i++ {
			out = append(out, github.ViewerRepository{Repository: fmt.Sprintf("acme/top-%03d", i)})
		}
		next := "do-not-fetch-again"
		return github.ViewerRepositoryList{Repositories: out, TotalCount: 500, NextCursor: &next}, nil
	}}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- h.do("GET", "/api/github/repositories", "") }()
	select {
	case rec := <-response:
		got := decode[Listing](t, rec)
		if rec.Code != 200 || len(got.Repositories) != 8 {
			t.Fatalf("initial response = %d %+v", rec.Code, got)
		}
	case <-time.After(time.Second):
		t.Fatal("initial rows waited for the next hundred")
	}
	<-requested
	released()
	h.service.running.Wait()
	got := decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if len(got.Repositories) != 108 || calls.Load() != 2 {
		t.Fatalf("top cache = %d repositories in %d requests", len(got.Repositories), calls.Load())
	}
	seen := map[string]bool{}
	for _, r := range got.Repositories {
		if seen[r.Repository] || !r.Top {
			t.Fatalf("duplicate or non-top row: %+v", r)
		}
		seen[r.Repository] = true
	}
}

func TestCachedTopMatchesReturnBeforeGitHubSearch(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer released()
	var calls atomic.Int32
	h.service.Lister = controlledLister{fakeLister: h.lister, search: func(ctx context.Context, _ string, query string) (github.ViewerRepositoryList, error) {
		calls.Add(1)
		if query != "busy" {
			t.Errorf("search query = %s", query)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return github.ViewerRepositoryList{}, ctx.Err()
		}
		return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "other/busy"}}}, nil
	}}
	h.do("GET", "/api/github/repositories", "")
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- h.do("GET", "/api/github/repositories?query=busy", "") }()
	select {
	case rec := <-response:
		got := decode[Listing](t, rec)
		if !got.Searching || len(got.Repositories) != 2 || got.Repositories[0].Repository != "acme/busy" || len(got.TopRepositories) != 5 {
			t.Fatalf("cached matches = %+v", got)
		}
		if _, err := h.registry.Pick(context.Background(), "acme/busy", nil); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cached top matches waited for GitHub search")
	}
	released()
	got := decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	if len(got.Repositories) != 3 || !got.Repositories[0].Picked || calls.Load() != 1 {
		t.Fatalf("merged search = %+v, calls %d", got, calls.Load())
	}
}

func TestFailedTopPrefetchKeepsInitialRowsAndDoesNotBlockSearch(t *testing.T) {
	h := newHarness(t)
	next := "after-first"
	h.lister.next = &next
	h.service.Lister = controlledLister{fakeLister: h.lister, top: func(ctx context.Context, host string, first int, cursor *string) (github.ViewerRepositoryList, error) {
		if cursor != nil {
			return github.ViewerRepositoryList{}, errors.New("SECRET prefetch failure")
		}
		return h.lister.ViewerRepositories(ctx, host, first, cursor)
	}}
	h.do("GET", "/api/github/repositories", "")
	h.service.running.Wait()
	rec := h.do("GET", "/api/github/repositories", "")
	got := decode[Listing](t, rec)
	if rec.Code != 200 || len(got.Repositories) != 5 || got.Searching || got.SearchError == "" || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("failed prefetch = %d %s", rec.Code, rec.Body)
	}
	got = decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	if len(got.Repositories) != 2 || got.SearchError != "" || got.Searching {
		t.Fatalf("search after failed prefetch = %+v", got)
	}
}

func TestSearchSortsExactThenTopThenLatestPushAndDeduplicates(t *testing.T) {
	h := newHarness(t)
	h.lister.list = []github.ViewerRepository{
		{Repository: "acme/widget-tools", PushedAt: pushed.Add(-time.Hour)},
		{Repository: "acme/widget", PushedAt: pushed.Add(-100 * time.Hour)},
		{Repository: "acme/widget-other", PushedAt: pushed.Add(-2 * time.Hour)},
	}
	h.lister.allList = []github.ViewerRepository{
		{Repository: "external/widget-latest", PushedAt: pushed.Add(10 * time.Hour)},
		{Repository: "external/widget", PushedAt: pushed.Add(-200 * time.Hour)},
		{Repository: "acme/widget-tools", PushedAt: pushed},
	}
	h.do("GET", "/api/github/repositories", "")
	got := decode[Listing](t, h.search("/api/github/repositories?query=widget"))
	if want := "acme/widget external/widget acme/widget-tools acme/widget-other external/widget-latest"; strings.Join(names(got), " ") != want {
		t.Fatalf("order = %v, want %s", names(got), want)
	}
	if !got.Repositories[0].Top || got.Repositories[1].Top || !got.Repositories[2].PushedAt.Equal(pushed) {
		t.Fatalf("classification or fresh metadata = %+v", got)
	}
}

func TestSearchCacheIsPerNormalizedQueryAndExpiresOrRefreshes(t *testing.T) {
	h := newHarness(t)
	now := pushed
	h.service.Now = func() time.Time { return now }
	h.do("GET", "/api/github/repositories", "")
	for _, query := range []string{"busy", "%20BUSY%20", "busy"} {
		h.search("/api/github/repositories?query=" + query)
	}
	if h.lister.searchCalls.Load() != 1 {
		t.Fatalf("same query made %d searches", h.lister.searchCalls.Load())
	}
	h.search("/api/github/repositories?query=quiet")
	if h.lister.searchCalls.Load() != 2 {
		t.Fatal("different query did not use GitHub search")
	}
	now = now.Add(CacheTTL + time.Second)
	h.search("/api/github/repositories?query=busy")
	h.do("GET", "/api/github/repositories?query=busy&refresh=1", "")
	h.search("/api/github/repositories?query=busy")
	if h.lister.searchCalls.Load() != 4 {
		t.Fatalf("expiry/refresh made %d searches", h.lister.searchCalls.Load())
	}
}

func TestSearchFailureKeepsCachedMatchesAndCanRetry(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.service.Lister = controlledLister{fakeLister: h.lister, search: func(context.Context, string, string) (github.ViewerRepositoryList, error) {
		if calls.Add(1) == 1 {
			return github.ViewerRepositoryList{}, errors.New("SECRET upstream failure")
		}
		return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "other/busy"}}}, nil
	}}
	h.do("GET", "/api/github/repositories", "")
	rec := h.search("/api/github/repositories?query=busy")
	got := decode[Listing](t, rec)
	if rec.Code != 200 || len(got.Repositories) != 2 || got.SearchError == "" || strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("failed search = %d %s", rec.Code, rec.Body)
	}
	h.do("GET", "/api/github/repositories?query=busy&refresh=1", "")
	got = decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	if got.SearchError != "" || len(got.Repositories) != 3 || calls.Load() != 2 {
		t.Fatalf("retry = %+v", got)
	}
}

func TestAccountChangeCancelsSearchAndRejectsLateResults(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer released()
	var calls atomic.Int32
	h.service.Lister = controlledLister{fakeLister: h.lister, search: func(ctx context.Context, _ string, _ string) (github.ViewerRepositoryList, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			<-release
			return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "old/private"}}}, nil
		}
		return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "new/private"}}}, nil
	}}
	h.do("GET", "/api/github/repositories?query=private", "")
	<-started
	h.service.ResetIdentity()
	got := decode[Listing](t, h.search("/api/github/repositories?query=private"))
	released()
	h.service.running.Wait()
	got = decode[Listing](t, h.search("/api/github/repositories?query=private"))
	if len(got.Repositories) != 1 || got.Repositories[0].Repository != "new/private" || calls.Load() != 2 {
		t.Fatalf("new account got %+v", got)
	}
}

func TestShutdownCancelsAnUnfinishedRepositorySearch(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{})
	h.service.Lister = controlledLister{fakeLister: h.lister, search: func(ctx context.Context, _ string, _ string) (github.ViewerRepositoryList, error) {
		close(started)
		<-ctx.Done()
		return github.ViewerRepositoryList{}, ctx.Err()
	}}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() { h.service.Run(ctx); close(done) }()
	h.do("GET", "/api/github/repositories?query=early", "")
	<-started
	stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the search")
	}
}

func TestExpiredTopListKeepsExtraRowsUntilTheNewPageReplacesThem(t *testing.T) {
	h := newHarness(t)
	now := pushed
	h.service.Now = func() time.Time { return now }
	page := func(prefix string, from, to int) []github.ViewerRepository {
		out := []github.ViewerRepository{}
		for i := from; i < to; i++ {
			out = append(out, github.ViewerRepository{Repository: fmt.Sprintf("acme/%s-%03d", prefix, i)})
		}
		return out
	}
	round := atomic.Int32{}
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer released()
	h.service.Lister = controlledLister{fakeLister: h.lister, top: func(ctx context.Context, _ string, _ int, cursor *string) (github.ViewerRepositoryList, error) {
		next := "after-eight"
		if cursor == nil {
			if round.Load() == 0 {
				return github.ViewerRepositoryList{Repositories: page("top", 0, 8), TotalCount: 300, NextCursor: &next}, nil
			}
			// The second listing moves top-007 out of the first page.
			return github.ViewerRepositoryList{Repositories: append(page("top", 0, 7), github.ViewerRepository{Repository: "acme/new"}), TotalCount: 300, NextCursor: &next}, nil
		}
		if round.Add(1) == 1 {
			return github.ViewerRepositoryList{Repositories: page("top", 8, 108), TotalCount: 300}, nil
		}
		select {
		case <-release:
		case <-ctx.Done():
			return github.ViewerRepositoryList{}, ctx.Err()
		}
		return github.ViewerRepositoryList{Repositories: page("top", 7, 100), TotalCount: 300}, nil
	}}
	h.do("GET", "/api/github/repositories", "")
	h.service.running.Wait()
	if got := decode[Listing](t, h.do("GET", "/api/github/repositories", "")); len(got.Repositories) != 108 {
		t.Fatalf("first listing = %d rows", len(got.Repositories))
	}
	now = now.Add(CacheTTL + time.Second)
	got := decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if len(got.Repositories) != 109 || got.Repositories[7].Repository != "acme/new" || !got.Searching {
		t.Fatalf("reloading listing = %d rows, eighth %s, searching %v", len(got.Repositories), got.Repositories[7].Repository, got.Searching)
	}
	released()
	h.service.running.Wait()
	got = decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if len(got.Repositories) != 101 || got.Repositories[7].Repository != "acme/new" || got.Repositories[8].Repository != "acme/top-007" || got.Repositories[100].Repository != "acme/top-099" {
		t.Fatalf("reloaded listing = %v", names(got))
	}
}

func TestSearchBeforeTheTopListLoadsIt(t *testing.T) {
	h := newHarness(t)
	h.service.Lister = controlledLister{fakeLister: h.lister, search: func(context.Context, string, string) (github.ViewerRepositoryList, error) {
		return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "other/busy", PushedAt: pushed.Add(time.Hour)}}}, nil
	}}
	got := decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	// Exact names first (the top one ahead), then the other top match.
	if want := "acme/busy other/busy acme/also-busy"; len(got.TopRepositories) != 5 || strings.Join(names(got), " ") != want || !got.Repositories[0].Top || got.Repositories[1].Top || !got.Repositories[2].Top {
		t.Fatalf("search before top list = %+v", got)
	}
}

func TestSearchCoversOnlyRepositoriesTheViewerHasPermissionOn(t *testing.T) {
	h := newHarness(t)
	scope := github.SearchScope{Owners: []string{"me", "acme-org"}, Repositories: []string{"friend/shared"}}
	h.lister.scope = scope
	// The viewer contributed to stranger/project, which gives no permission on
	// stranger's other repositories, so stranger is not searched.
	h.lister.list = []github.ViewerRepository{{Repository: "stranger/project"}, {Repository: "friend/shared"}, {Repository: "me/own"}}
	h.do("GET", "/api/github/repositories", "")
	h.search("/api/github/repositories?query=busy")
	h.lister.mu.Lock()
	defer h.lister.mu.Unlock()
	if fmt.Sprint(h.lister.searched) != fmt.Sprint(scope) {
		t.Fatalf("search scope = %v, want %v", h.lister.searched, scope)
	}
}

func TestMoreLoadsTheNextTopPageWithTheLastCursor(t *testing.T) {
	h := newHarness(t)
	var cursors []string
	var mu sync.Mutex
	h.service.Lister = controlledLister{fakeLister: h.lister, top: func(_ context.Context, _ string, _ int, cursor *string) (github.ViewerRepositoryList, error) {
		mu.Lock()
		defer mu.Unlock()
		page := map[string]struct {
			rows []string
			next string
		}{"": {[]string{"acme/a", "acme/b"}, "c1"}, "c1": {[]string{"acme/b", "acme/c"}, "c2"}, "c2": {[]string{"acme/d"}, ""}}
		key := ""
		if cursor != nil {
			key = *cursor
		}
		cursors = append(cursors, key)
		out := github.ViewerRepositoryList{TotalCount: 4}
		for _, r := range page[key].rows {
			out.Repositories = append(out.Repositories, github.ViewerRepository{Repository: r})
		}
		if next := page[key].next; next != "" {
			out.NextCursor = &next
		}
		return out, nil
	}}
	h.do("GET", "/api/github/repositories", "")
	h.service.running.Wait()
	got := decode[Listing](t, h.do("GET", "/api/github/repositories", ""))
	if strings.Join(names(got), " ") != "acme/a acme/b acme/c" || !got.HasMore {
		t.Fatalf("after the background page = %v, more %v", names(got), got.HasMore)
	}
	if got = decode[Listing](t, h.do("GET", "/api/github/repositories?more=1", "")); !got.Searching {
		t.Fatal("more did not report loading")
	}
	h.service.running.Wait()
	got = decode[Listing](t, h.do("GET", "/api/github/repositories?more=1", ""))
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(names(got), " ") != "acme/a acme/b acme/c acme/d" || got.HasMore || got.Searching || strings.Join(cursors, ",") != ",c1,c2" {
		t.Fatalf("after more = %v, more %v, cursors %v", names(got), got.HasMore, cursors)
	}
}

func TestMoreAppendsTheNextSearchPageAndCountsEveryMatch(t *testing.T) {
	h := newHarness(t)
	var cursors atomic.Value
	cursors.Store("")
	h.service.Lister = controlledLister{fakeLister: h.lister, page: func(_ context.Context, _ string, cursor *string) (github.ViewerRepositoryList, error) {
		if cursor == nil {
			next := "s1"
			return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "other/busy-1"}, {Repository: "acme/busy"}}, TotalCount: 3, NextCursor: &next}, nil
		}
		cursors.Store(*cursor)
		return github.ViewerRepositoryList{Repositories: []github.ViewerRepository{{Repository: "other/busy-2"}}, TotalCount: 3}, nil
	}}
	h.do("GET", "/api/github/repositories", "")
	got := decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	// acme/busy, acme/also-busy and other/busy-1 loaded; other/busy-2 not yet.
	if len(got.Repositories) != 3 || got.TotalCount != 4 || !got.HasMore {
		t.Fatalf("first page = %v, total %d, more %v", names(got), got.TotalCount, got.HasMore)
	}
	h.do("GET", "/api/github/repositories?query=busy&more=1", "")
	got = decode[Listing](t, h.search("/api/github/repositories?query=busy"))
	if want := "acme/busy acme/also-busy other/busy-1 other/busy-2"; strings.Join(names(got), " ") != want || got.TotalCount != 4 || got.HasMore || cursors.Load() != "s1" {
		t.Fatalf("second page = %v, total %d, more %v, cursor %v", names(got), got.TotalCount, got.HasMore, cursors.Load())
	}
}

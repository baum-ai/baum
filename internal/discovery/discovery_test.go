package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixture struct {
	mu           sync.Mutex
	requests     []*http.Request
	offline      bool
	limited      bool
	status       map[string]int
	pages        map[string]string
	conditional  bool
	secondary    bool
	pollInterval string
}

func (f *fixture) roundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Clone(r.Context()))
	if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "token synthetic" {
		return nil, fmt.Errorf("wrong endpoint or credential")
	}
	if f.offline {
		return nil, errors.New("network unreachable")
	}
	h := http.Header{"Etag": []string{`"recorded-v1"`}, "X-Ratelimit-Remaining": []string{"100"}, "X-Ratelimit-Reset": []string{"2000000000"}}
	h.Set("Link", `<https://outside.example/steal>; rel="next"`)
	if f.pollInterval != "" {
		h.Set("X-Poll-Interval", f.pollInterval)
	}
	status := 200
	body := f.pages[r.URL.RequestURI()]
	if r.URL.Path == "/graphql" {
		var request struct{ Variables struct{ Cursor *string } }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		page := 1
		if request.Variables.Cursor != nil {
			if _, err := fmt.Sscanf(*request.Variables.Cursor, "page-%d", &page); err != nil {
				return nil, err
			}
		}
		var rows []apiPR
		if err := json.Unmarshal([]byte(f.pages[fmt.Sprintf("/repos/acme/app/pulls?state=open&per_page=100&page=%d", page)]), &rows); err != nil {
			return nil, err
		}
		nodes := []map[string]any{}
		for _, pr := range rows {
			var head any
			if pr.Head.Repo != nil {
				head = map[string]any{"databaseId": pr.Head.Repo.ID, "nameWithOwner": pr.Head.Repo.FullName}
			}
			requests := []any{}
			for _, user := range pr.RequestedReviewers {
				requests = append(requests, map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": user.Login}})
			}
			for _, team := range pr.RequestedTeams {
				requests = append(requests, map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "databaseId": team.ID}})
			}
			nodes = append(nodes, map[string]any{"number": pr.Number, "title": pr.Title, "isDraft": pr.Draft, "author": pr.User, "headRefOid": pr.Head.SHA, "headRefName": pr.Head.Ref, "headRepository": head, "baseRepository": map[string]any{"databaseId": pr.Base.Repo.ID, "nameWithOwner": pr.Base.Repo.FullName}, "reviewRequests": map[string]any{"nodes": requests}})
		}
		encoded, _ := json.Marshal(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "viewer"}, "rateLimit": map[string]any{"cost": 1, "remaining": 4900, "resetAt": "2033-05-18T03:33:20Z"}, "repository": map[string]any{"pullRequests": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": len(rows) == 100, "endCursor": fmt.Sprintf("page-%d", page+1)}}}}})
		body = string(encoded)
	}
	if body == "" {
		body = `{}`
	}
	if configured := f.status[r.URL.Path]; configured != 0 {
		status = configured
	}
	if r.URL.Path != "/graphql" && f.conditional && r.Header.Get("If-None-Match") != "" {
		status = 304
		body = ""
	}
	if f.limited {
		status = 403
		h.Set("X-RateLimit-Remaining", "0")
		body = `{"message":"API rate limit exceeded"}`
	}
	if f.secondary {
		status = 403
		h.Set("Retry-After", "90")
		body = `{"message":"secondary rate limit"}`
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func recordedFixture() *fixture {
	return &fixture{pages: map[string]string{
		"/user":                           `{"login":"viewer"}`,
		"/user/teams?per_page=100&page=1": `[{"id":42}]`,
		"/repos/acme/app/pulls?state=open&per_page=100&page=1": `[
 {"number":1,"title":"My draft","html_url":"https://github.com/acme/app/pull/1","draft":true,"user":{"login":"VIEWER"},"head":{"sha":"head1","ref":"my-draft","repo":{"id":1,"full_name":"acme/app"}},"base":{"repo":{"id":1,"full_name":"acme/app"}},"requested_reviewers":[]},
 {"number":2,"title":"Team request","user":{"login":"writer"},"head":{"sha":"head2","repo":{"id":2,"full_name":"fork/app"}},"base":{"repo":{"id":1,"full_name":"acme/app"}},"requested_teams":[{"id":42}]},
 {"number":3,"title":"Direct request","user":{"login":"reader"},"head":{"sha":"head3","repo":null},"base":{"repo":{"id":1,"full_name":"acme/app"}},"requested_reviewers":[{"login":"viewer"}]},
 {"number":4,"title":"Unknown permissions","user":{"login":"unknown"},"head":{"sha":"head4","repo":{"id":1,"full_name":"acme/app"}},"base":{"repo":{"id":1,"full_name":"acme/app"}}}
 ]`,
		"/repos/acme/app/collaborators/VIEWER/permission": `{"permission":"admin"}`,
		"/repos/acme/app/collaborators/writer/permission": `{"permission":"write"}`,
		"/repos/acme/app/collaborators/reader/permission": `{"permission":"read"}`,
	}, status: map[string]int{"/repos/acme/app/collaborators/unknown/permission": 403}}
}

var testRepo = github.Repository{Host: "github.com", Owner: "acme", Name: "app"}

func service(t *testing.T, f *fixture) (*Service, *time.Time) {
	t.Helper()
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(f.roundTrip)})
	s := New(client, t.TempDir())
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}
func TestFlagsAndPrivateOfflineCache(t *testing.T) {
	f := recordedFixture()
	s, now := service(t, f)
	snapshot, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stale || snapshot.Viewer != "viewer" || len(snapshot.PullRequests) != 4 {
		t.Fatalf("snapshot %#v", snapshot)
	}
	prs := snapshot.PullRequests
	if !prs[0].AuthoredByMe || !prs[0].Draft || prs[0].Fork || prs[0].HeadSHA != "head1" || prs[0].HeadRef != "my-draft" || prs[0].State != "OPEN" || prs[0].AuthorHasWrite == nil || !*prs[0].AuthorHasWrite {
		t.Fatalf("authored flags %#v", prs[0])
	}
	if !prs[1].ReviewRequestedFromMe || !prs[1].Fork || prs[1].AuthorHasWrite == nil || !*prs[1].AuthorHasWrite {
		t.Fatalf("team/fork flags %#v", prs[1])
	}
	if !prs[2].ReviewRequestedFromMe || !prs[2].Fork || prs[2].AuthorHasWrite == nil || *prs[2].AuthorHasWrite {
		t.Fatalf("direct/read flags %#v", prs[2])
	}
	if prs[3].AuthorHasWrite != nil {
		t.Fatal("unreadable permission treated as known")
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("cache entries %v %v", entries, err)
	}
	info, err := os.Stat(repositoryCachePath(t, s))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits; Stat reports 0666/0777 there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode %v", info.Mode())
	}
	before := len(f.requests)
	cached, err := s.Cached(context.Background(), testRepo)
	if err != nil || cached.Stale || len(cached.PullRequests) != 4 || len(f.requests) != before {
		t.Fatalf("cache-only result %#v %v", cached, err)
	}
	f.offline = true
	*now = now.Add(time.Hour)
	offline, err := s.Poll(context.Background(), testRepo)
	if err == nil || !offline.Stale || len(offline.PullRequests) != 4 || offline.Error == "" {
		t.Fatalf("offline %#v %v", offline, err)
	}
	other := New(github.New(func(context.Context, string) (string, error) { return "other-account-token", nil }, github.Options{Transport: roundTrip(f.roundTrip)}), s.directory)
	if _, err := other.Cached(context.Background(), testRepo); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other account reused cache: %v", err)
	}
}
func TestConditionalBackoffAndRateLimit(t *testing.T) {
	f := recordedFixture()
	s, now := service(t, f)
	first, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.requests)
	if _, err := s.Poll(context.Background(), testRepo); err != nil || len(f.requests) != before {
		t.Fatal("poll before deadline made request")
	}
	f.conditional = true
	*now = first.NextPoll
	second, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if second.NextPoll.Sub(*now) != time.Minute || second.Stale || len(second.PullRequests) != 4 {
		t.Fatalf("conditional result %#v", second)
	}
	for _, request := range f.requests[before:] {
		if request.URL.Path != "/graphql" && request.URL.Path != "/repos/acme/app/collaborators/unknown/permission" && request.Header.Get("If-None-Match") == "" {
			t.Errorf("missing ETag %s", request.URL)
		}
	}
	*now = second.NextPoll
	f.conditional = false
	f.limited = true
	limited, err := s.Poll(context.Background(), testRepo)
	if !errors.Is(err, errRateLimit) || !limited.Stale || limited.Rate.Remaining == nil || *limited.Rate.Remaining != 0 || limited.Rate.Reset.Unix() != 2000000000 || !limited.NextPoll.Equal(limited.Rate.Reset) || len(limited.PullRequests) != 4 {
		t.Fatalf("rate result %#v %v", limited, err)
	}
	before = len(f.requests)
	if _, err := s.Poll(context.Background(), testRepo); !errors.Is(err, errRateLimit) || len(f.requests) != before {
		t.Fatal("exhausted rate limit retried early")
	}
}

// A cache written before pull requests kept their head branch reads every
// response in full once, so a 304 cannot keep the branch unknown.
func TestOlderCacheFormatIsReadInFullOnce(t *testing.T) {
	f := recordedFixture()
	s, now := service(t, f)
	first, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if first.PullRequests[0].HeadRef != "my-draft" || first.PullRequests[1].HeadRef != "" {
		t.Fatalf("head branches %#v", first.PullRequests)
	}
	path := repositoryCachePath(t, s)
	cache, err := load(path)
	if err != nil || cache.Format != cacheFormat {
		t.Fatalf("format %d %v", cache.Format, err)
	}
	cache.Format = 0
	for key, response := range cache.Responses {
		response.Body = json.RawMessage(strings.ReplaceAll(string(response.Body), `"ref":"my-draft",`, ""))
		cache.Responses[key] = response
	}
	if err := save(path, cache); err != nil {
		t.Fatal(err)
	}
	f.conditional = true
	*now = first.NextPoll
	before := len(f.requests)
	got, err := s.Poll(context.Background(), testRepo)
	if err != nil || got.PullRequests[0].HeadRef != "my-draft" {
		t.Fatalf("upgraded poll %#v %v", got, err)
	}
	for _, request := range f.requests[before:] {
		if request.Header.Get("If-None-Match") != "" {
			t.Errorf("older format reused %s", request.URL)
		}
	}
	*now = got.NextPoll
	before = len(f.requests)
	if _, err := s.Poll(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	conditional := 0
	for _, request := range f.requests[before:] {
		if request.Header.Get("If-None-Match") != "" {
			conditional++
		}
	}
	if conditional == 0 {
		t.Fatal("upgraded cache not conditional")
	}
}
func TestMissingTeamScopeDoesNotHideDirectRequest(t *testing.T) {
	f := recordedFixture()
	f.status["/user/teams"] = 403
	s, _ := service(t, f)
	got, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequests[1].ReviewRequestedFromMe || got.PullRequests[1].TeamMembershipKnown || !got.PullRequests[2].ReviewRequestedFromMe {
		t.Fatalf("team scope result %#v", got.PullRequests)
	}
}
func TestPaginationReadsEveryPage(t *testing.T) {
	f := recordedFixture()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=1"]), &rows); err != nil {
		t.Fatal(err)
	}
	first := make([]map[string]any, 100)
	for i := range first {
		first[i] = rows[0]
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=1"] = string(encoded)
	f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=2"] = `[` + strings.TrimSpace(string(mustJSON(t, rows[1]))) + `]`
	s, _ := service(t, f)
	got, err := s.Poll(context.Background(), testRepo)
	if err != nil || len(got.PullRequests) != 101 {
		t.Fatalf("pagination length %d err %v", len(got.PullRequests), err)
	}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCacheStripsUnusedPrivateFieldsAndIgnoresLinkURLs(t *testing.T) {
	f := recordedFixture()
	f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=1"] = strings.Replace(f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=1"], `"number":1,`, `"number":1,"body":"https://private-user-images.githubusercontent.com/a?jwt=private-signed-value","unused":{"secret":"unused-secret"},`, 1)
	s, _ := service(t, f)
	if _, err := s.Poll(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(repositoryCachePath(t, s))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-signed-value", "unused-secret", "synthetic", "private-user-images"} {
		if strings.Contains(string(contents), secret) {
			t.Errorf("unused private data persisted: %s", secret)
		}
	}
}
func TestMalformedCacheAndMalformedAPI(t *testing.T) {
	f := recordedFixture()
	s, _ := service(t, f)
	reader, err := s.client.REST(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(reader.CacheIdentity(), testRepo.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cached(context.Background(), testRepo); err == nil {
		t.Fatal("accepted malformed cache")
	}
	if _, err := s.Poll(context.Background(), testRepo); err == nil {
		t.Fatal("ignored unreadable cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f.pages["/repos/acme/app/pulls?state=open&per_page=100&page=1"] = `[{"number":1}]`
	got, err := s.Poll(context.Background(), testRepo)
	if err == nil || !got.Stale || len(got.PullRequests) != 0 {
		t.Fatalf("partial/incomplete response published %#v %v", got, err)
	}
}

func TestSecondaryRateAndServerPollingInterval(t *testing.T) {
	f := recordedFixture()
	f.pollInterval = "300"
	s, now := service(t, f)
	first, err := s.Poll(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextPoll.Sub(*now) != 5*time.Minute {
		t.Fatalf("ignored server interval %v", first.NextPoll.Sub(*now))
	}
	*now = first.NextPoll
	f.secondary = true
	limited, err := s.Poll(context.Background(), testRepo)
	if !errors.Is(err, errRateLimit) || limited.NextPoll.Sub(*now) != 90*time.Second || limited.Rate.Remaining == nil || *limited.Rate.Remaining != 100 || !limited.Rate.RetryAt.Equal(limited.NextPoll) {
		t.Fatalf("secondary budget %#v %v", limited, err)
	}
}

func repositoryCachePath(t *testing.T, s *Service) string {
	t.Helper()
	reader, err := s.client.REST(context.Background(), testRepo)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(reader.CacheIdentity(), testRepo.String())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBudgetSharedAcrossRepositoriesAndRestart(t *testing.T) {
	for _, secondary := range []bool{false, true} {
		t.Run(fmt.Sprintf("secondary=%t", secondary), func(t *testing.T) {
			f := recordedFixture()
			f.limited = !secondary
			f.secondary = secondary
			s, now := service(t, f)
			first, err := s.Poll(context.Background(), testRepo)
			if !errors.Is(err, errRateLimit) {
				t.Fatalf("first poll: %#v %v", first, err)
			}
			before := len(f.requests)
			other := github.Repository{Host: testRepo.Host, Owner: testRepo.Owner, Name: "other"}
			blocked, err := s.Poll(context.Background(), other)
			if !errors.Is(err, errRateLimit) || len(f.requests) != before || !blocked.Stale || !blocked.NextPoll.Equal(first.NextPoll) {
				t.Fatalf("same credential sent request %#v %v requests=%d", blocked, err, len(f.requests)-before)
			}
			restarted := New(s.client, s.directory)
			restarted.now = s.now
			if _, err := restarted.Poll(context.Background(), other); !errors.Is(err, errRateLimit) || len(f.requests) != before {
				t.Fatalf("restart ignored budget %v", err)
			}
			// Another account on the same host has a different budget.
			accountRequests := 0
			isolated := New(github.New(func(context.Context, string) (string, error) { return "another-account", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { accountRequests++; return nil, errors.New("offline") })}), s.directory)
			isolated.now = s.now
			_, _ = isolated.Poll(context.Background(), other)
			if accountRequests != 1 {
				t.Fatalf("other account inherited exhausted budget: %d requests", accountRequests)
			}
			// The same credential string on another host also has a different budget.
			t.Setenv("GH_HOST", "github.enterprise.example")
			hostRequests := 0
			hostService := New(github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { hostRequests++; return nil, errors.New("offline") })}), s.directory)
			hostService.now = s.now
			_, _ = hostService.Poll(context.Background(), github.Repository{Host: "github.enterprise.example", Owner: "acme", Name: "app"})
			if hostRequests != 1 {
				t.Fatalf("other host inherited exhausted budget: %d requests", hostRequests)
			}
			*now = first.NextPoll
			f.limited = false
			f.secondary = false
			_, _ = restarted.Poll(context.Background(), other)
			if len(f.requests) == before {
				t.Fatal("deadline expiry did not permit a request")
			}
		})
	}
}

func TestLiveFreshnessAndRestart(t *testing.T) {
	f := recordedFixture()
	s, now := service(t, f)
	ctx := context.Background()
	if _, err := s.Poll(ctx, testRepo); err != nil {
		t.Fatal(err)
	}
	got, err := s.Cached(ctx, testRepo)
	if err != nil || got.Stale {
		t.Fatalf("live cache stale: %+v %v", got, err)
	}
	restarted := New(s.client, s.directory)
	restarted.now = s.now
	got, err = restarted.Cached(ctx, testRepo)
	if err != nil || !got.Stale {
		t.Fatalf("disk cache fresh on restart: %+v %v", got, err)
	}
	*now = now.Add(time.Minute)
	got, err = s.Cached(ctx, testRepo)
	if err != nil || !got.Stale {
		t.Fatalf("expired cache fresh: %+v %v", got, err)
	}
	if err := s.Observe(ctx, testRepo, Observation{Number: 9, HeadSHA: "moved", State: "MERGED", FetchedAt: *now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Poll(ctx, testRepo); err != nil {
		t.Fatal(err)
	}
	got, err = s.Cached(ctx, testRepo)
	if err != nil || got.Stale || len(got.Observations) != 1 || got.Observations[0].State != "MERGED" {
		t.Fatalf("observation lost %+v %v", got, err)
	}
	f.offline = true
	*now = now.Add(time.Hour)
	if _, err := s.Poll(ctx, testRepo); err == nil {
		t.Fatal("expected offline error")
	}
	got, _ = s.Cached(ctx, testRepo)
	if !got.Stale {
		t.Fatal("offline cache fresh")
	}
}

func TestCachedNeverWaitsForNetworkPoll(t *testing.T) {
	f := recordedFixture()
	blocked := false
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if blocked {
			once.Do(func() { close(started) })
			<-release
		}
		return f.roundTrip(r)
	})})
	s := New(client, t.TempDir())
	now := time.Now()
	s.now = func() time.Time { return now }
	if _, err := s.Poll(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	blocked = true
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.Poll(context.Background(), testRepo) }()
	<-started
	returned := make(chan error, 1)
	go func() { _, err := s.Cached(context.Background(), testRepo); returned <- err }()
	select {
	case err := <-returned:
		close(release)
		<-done
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("cached inbox blocked on network poll")
	}
}

func TestPollNowSkipsTheDeadlineButNotTheBudget(t *testing.T) {
	f := recordedFixture()
	s, _ := service(t, f)
	if _, err := s.Poll(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	f.conditional = true
	before := len(f.requests)
	got, err := s.PollNow(context.Background(), testRepo)
	if err != nil || len(f.requests) == before || len(got.PullRequests) != 4 || got.Stale {
		t.Fatalf("poll now %#v %v requests=%d", got, err, len(f.requests)-before)
	}
	for _, request := range f.requests[before:] {
		if request.URL.Path != "/graphql" && request.URL.Path != "/repos/acme/app/collaborators/unknown/permission" && request.Header.Get("If-None-Match") == "" {
			t.Errorf("unconditional %s", request.URL)
		}
	}
	f.conditional = false
	f.limited = true
	if _, err := s.PollNow(context.Background(), testRepo); !errors.Is(err, errRateLimit) {
		t.Fatalf("limited poll: %v", err)
	}
	before = len(f.requests)
	if _, err := s.PollNow(context.Background(), testRepo); !errors.Is(err, errRateLimit) || len(f.requests) != before {
		t.Fatalf("exhausted budget polled again: %v requests=%d", err, len(f.requests)-before)
	}
}

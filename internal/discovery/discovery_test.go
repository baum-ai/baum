package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixture struct {
	mu          sync.Mutex
	requests    []*http.Request
	offline     bool
	limited     bool
	status      map[string]int
	pages       map[string]string
	conditional bool
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
	status := 200
	body := f.pages[r.URL.RequestURI()]
	if body == "" {
		body = `{}`
	}
	if configured := f.status[r.URL.Path]; configured != 0 {
		status = configured
	}
	if f.conditional && r.Header.Get("If-None-Match") != "" {
		status = 304
		body = ""
	}
	if f.limited {
		status = 403
		h.Set("X-RateLimit-Remaining", "0")
		body = `{"message":"API rate limit exceeded"}`
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func recordedFixture() *fixture {
	return &fixture{pages: map[string]string{
		"/user":                           `{"login":"viewer"}`,
		"/user/teams?per_page=100&page=1": `[{"id":42}]`,
		"/repos/acme/app/pulls?state=open&per_page=100&page=1": `[
 {"number":1,"title":"My draft","html_url":"https://github.com/acme/app/pull/1","draft":true,"user":{"login":"VIEWER"},"head":{"sha":"head1","repo":{"id":1,"full_name":"acme/app"}},"base":{"repo":{"id":1,"full_name":"acme/app"}},"requested_reviewers":[]},
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
	if !prs[0].AuthoredByMe || !prs[0].Draft || prs[0].Fork || prs[0].HeadSHA != "head1" || prs[0].State != "OPEN" || prs[0].AuthorHasWrite == nil || !*prs[0].AuthorHasWrite {
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
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries %v %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(s.directory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("cache mode %v", info.Mode())
	}
	before := len(f.requests)
	cached, err := s.Cached(context.Background(), testRepo)
	if err != nil || !cached.Stale || len(cached.PullRequests) != 4 || len(f.requests) != before {
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
		if request.URL.Path != "/repos/acme/app/collaborators/unknown/permission" && request.Header.Get("If-None-Match") == "" {
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
	if _, err := s.Poll(context.Background(), testRepo); err != nil || len(f.requests) != before {
		t.Fatal("exhausted rate limit retried early")
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

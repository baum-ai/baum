package githubreads

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/prupdate"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var repo = github.Repository{Host: "github.com", Owner: "acme", Name: "app"}

const threadsReply = `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
	{"id":"RT_1","path":"a.go","line":1,"isResolved":false,"viewerCanResolve":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"**Rename** this","author":{"__typename":"User","login":"alice"}}]}},
	{"id":"RT_2","path":"b.go","line":2,"isResolved":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"done","author":{"__typename":"User","login":"bob"}}]}}]}}}}}`

// service answers every GraphQL request with reply and counts them.
func service(reply string, token *atomic.Value, calls *atomic.Int32) *Service {
	client := github.New(func(context.Context, string) (string, error) { return token.Load().(string), nil }, github.Options{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply)), Request: r}, nil
	})})
	return &Service{Client: client}
}

func TestThreadsFilterByState(t *testing.T) {
	var token atomic.Value
	token.Store("t")
	var calls atomic.Int32
	s := service(threadsReply, &token, &calls)
	for state, want := range map[string]int{"": 1, Unresolved: 1, Resolved: 1, All: 2} {
		got, err := s.Threads(context.Background(), repo, 7, state)
		if err != nil || len(got.Threads) != want {
			t.Fatalf("%q: %+v %v", state, got, err)
		}
	}
	got, _ := s.Threads(context.Background(), repo, 7, Resolved)
	if got.State != Resolved || got.Threads[0].ID != "RT_2" || !got.Threads[0].Resolved {
		t.Fatalf("resolved %+v", got)
	}
	open, _ := s.Threads(context.Background(), repo, 7, "")
	if open.State != Unresolved || open.Threads[0].Excerpt != "Rename this" || !open.Threads[0].CanResolve || len(open.Threads[0].Messages) != 1 {
		t.Fatalf("open %+v", open)
	}
	if calls.Load() != 1 {
		t.Errorf("one read serves every state, GitHub was asked %d times", calls.Load())
	}
}

func TestCacheHitMissAndIdentity(t *testing.T) {
	var token atomic.Value
	token.Store("alice")
	var calls atomic.Int32
	s := service(threadsReply, &token, &calls)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	read := func() {
		t.Helper()
		if _, err := s.Threads(context.Background(), repo, 7, All); err != nil {
			t.Fatal(err)
		}
	}
	read()
	read()
	if calls.Load() != 1 {
		t.Fatalf("hit within TTL: %d calls", calls.Load())
	}
	advance(59 * time.Second)
	read()
	if calls.Load() != 1 {
		t.Fatalf("still within TTL: %d calls", calls.Load())
	}
	advance(2 * time.Second)
	read()
	if calls.Load() != 2 {
		t.Fatalf("miss after TTL: %d calls", calls.Load())
	}
	token.Store("bob")
	read()
	if calls.Load() != 3 {
		t.Fatalf("another account must not be served alice's answer: %d calls", calls.Load())
	}
	if _, err := s.Threads(context.Background(), repo, 8, All); err != nil || calls.Load() != 4 {
		t.Fatalf("another pull request is another entry: %d calls", calls.Load())
	}
	advance(2 * time.Minute)
	read()
	if len(s.entries) != 1 {
		t.Errorf("expired entries are dropped on store: %d entries", len(s.entries))
	}
}

func TestIssueTTLIsLonger(t *testing.T) {
	var token atomic.Value
	token.Store("t")
	var calls atomic.Int32
	reply := `{"data":{"repository":{"issue":{"number":3,"title":"Bug","state":"OPEN","url":"u","createdAt":"2026-01-01T00:00:00Z","body":"**x**","author":{"login":"a"},"labels":{"nodes":[]},"comments":{"totalCount":0,"nodes":[]},"closedByPullRequestsReferences":{"nodes":[]},"timelineItems":{"nodes":[]}}}}}`
	s := service(reply, &token, &calls)
	now := time.Now()
	s.Now = func() time.Time { return now }
	for range 2 {
		issue, err := s.Issue(context.Background(), repo, 3)
		if err != nil || issue.Body != "x" || issue.ClosedAt != "" || issue.Labels == nil || issue.LinkedPullRequests == nil {
			t.Fatalf("issue %+v %v", issue, err)
		}
		now = now.Add(2 * time.Minute)
	}
	if calls.Load() != 1 {
		t.Fatalf("issue cached for 5 minutes: %d calls", calls.Load())
	}
}

func TestIssueNotFoundMentionsPullRequests(t *testing.T) {
	var token atomic.Value
	token.Store("t")
	var calls atomic.Int32
	s := service(`{"data":{"repository":{"issue":null}},"errors":[{"type":"NOT_FOUND","message":"x"}]}`, &token, &calls)
	_, err := s.Issue(context.Background(), repo, 3)
	var refusal *prupdate.Error
	if !errors.As(err, &refusal) || refusal.Code != prupdate.NotFound || !strings.Contains(refusal.Message, "pull request") {
		t.Fatalf("err %v", err)
	}
}

func TestTimelineShapes(t *testing.T) {
	var token atomic.Value
	token.Store("t")
	var calls atomic.Int32
	reply := `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
	{"__typename":"PullRequestCommit","prCommit":{"oid":"c1","messageHeadline":"Add","committedDate":"2026-01-01T10:00:00Z","author":null}},
	{"__typename":"MergedEvent","createdAt":"2026-01-01T21:00:00Z","actor":{"login":"alice"},"mergeCommit":{"oid":"m1"}},
	{"__typename":"CrossReferencedEvent","createdAt":"2026-01-01T19:00:00Z","willCloseTarget":false,"source":{"__typename":"Issue","number":3,"title":"Bug","issueState":"OPEN","url":"u","repository":{"nameWithOwner":"acme/lib"}}}]}}}}}`
	s := service(reply, &token, &calls)
	got, err := s.Timeline(context.Background(), repo, 7)
	if err != nil || len(got.Events) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if c, ok := got.Events[0].Commit.(Commit); !ok || c.OID != "c1" || got.Events[0].At != "2026-01-01T10:00:00Z" {
		t.Errorf("committed %+v", got.Events[0])
	}
	if got.Events[1].Commit != "m1" || got.Events[2].WillClose == nil || *got.Events[2].WillClose || got.Events[2].Source.Kind != "issue" {
		t.Errorf("events %+v %+v", got.Events[1], got.Events[2])
	}
}

func TestFailureWording(t *testing.T) {
	limited := &prupdate.Error{Code: prupdate.Failed, Message: rateLimited}
	for _, c := range []struct {
		err  error
		want *prupdate.Error
	}{
		{&github.RateLimitedError{}, limited},
		{&github.MergeError{Code: github.MergeFailed, Message: "GitHub is limiting requests right now. Try again in a minute."}, limited},
		{&github.MergeError{Code: github.MergeForbidden, Message: "x"}, &prupdate.Error{Code: prupdate.Forbidden, Message: reconnect}},
		{github.ErrNotFound, &prupdate.Error{Code: prupdate.NotFound, Message: "GitHub does not show this to your account."}},
		{errors.New("boom"), &prupdate.Error{Code: prupdate.Failed, Message: "baum could not read this from GitHub."}},
	} {
		if got := prupdate.AsError(failure(c.err)); *got != *c.want {
			t.Errorf("%v: %+v", c.err, got)
		}
	}
}

func TestBounds(t *testing.T) {
	if got := bounded("a\xffb"+strings.Repeat("é", 5000), maxMessage); len([]rune(got)) != maxMessage || !strings.HasSuffix(got, "…") || strings.Contains(got, "\xff") {
		t.Errorf("bounded %d", len([]rune(got)))
	}
	if got := boundedBytes(strings.Repeat("é", 40000), maxBody); len(got) > maxBody+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("boundedBytes %d", len(got))
	}
}

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

func TestGraphQLPaginationEndpointsStatesAndIndependentBudgets(t *testing.T) {
	for _, host := range []string{"github.com", "tenant.ghe.com", "git.enterprise.example"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("GH_HOST", host)
			calls := 0
			failPage := false
			remaining := 4998
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			reset := now.Add(time.Hour)
			client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				wantHost, wantPath := "api."+host, "/graphql"
				if host == "git.enterprise.example" {
					wantHost, wantPath = host, "/api/graphql"
				}
				if r.URL.Host != wantHost || r.Header.Get("Authorization") != "token synthetic" {
					t.Fatalf("endpoint/auth %s", r.URL)
				}
				headers := http.Header{"X-Ratelimit-Remaining": []string{"77"}}
				body := `[]`
				if r.Method == http.MethodPost {
					calls++
					if r.URL.Path != wantPath || r.Header.Get("If-None-Match") != "" {
						t.Fatalf("GraphQL endpoint/ETag %s", r.URL)
					}
					var request struct {
						Query     string
						Variables struct {
							Owner, Name string
							Cursor      *string
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(request.Query, "pullRequests(states: OPEN, first: 50, after: $cursor)") || request.Variables.Owner != "acme" || request.Variables.Name != "app" {
						t.Fatalf("query %#v", request)
					}
					node := `{"number":1,"title":"First","headRefOid":"head1","headRefName":"branch","author":{"login":"viewer"},"headRepository":{"databaseId":1,"nameWithOwner":"acme/app"},"baseRepository":{"databaseId":1,"nameWithOwner":"acme/app"},"reviewDecision":"APPROVED","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewThreads":{"nodes":[{"isResolved":false},{"isResolved":true},{"isResolved":false}],"pageInfo":{"hasNextPage":true}}}`
					hasNext, cursor := "true", "next"
					if request.Variables.Cursor != nil {
						if *request.Variables.Cursor != "next" {
							t.Fatalf("cursor %s", *request.Variables.Cursor)
						}
						if failPage {
							body = `{"errors":[{"type":"FORBIDDEN","message":"private secret"}]}`
						} else {
							node = `{"number":2,"title":"Second","headRefOid":"head2","author":{"login":"viewer"},"isDraft":true,"reviewDecision":"REVIEW_REQUIRED","latestOpinionatedReviews":{"nodes":[]},"latestReviews":{"nodes":[{"state":"COMMENTED"}]},"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]},"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`
						}
						hasNext, cursor = "false", ""
					}
					if !failPage || request.Variables.Cursor == nil {
						body = fmt.Sprintf(`{"data":{"viewer":{"login":"viewer"},"rateLimit":{"cost":2,"remaining":%d,"resetAt":%q},"repository":{"pullRequests":{"nodes":[%s],"pageInfo":{"hasNextPage":%s,"endCursor":%q}}}}}`, remaining, reset.Format(time.RFC3339), node, hasNext, cursor)
					}
				} else if strings.Contains(r.URL.Path, "/permission") {
					body = `{"permission":"write"}`
				} else if !strings.HasSuffix(r.URL.Path, "/user/teams") {
					t.Fatalf("unexpected REST request %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			s := New(client, t.TempDir())
			s.now = func() time.Time { return now }
			repo := github.Repository{Host: host, Owner: "acme", Name: "app"}
			got, err := s.Poll(context.Background(), repo)
			if err != nil || calls != 2 || len(got.PullRequests) != 2 {
				t.Fatalf("snapshot=%+v calls=%d err=%v", got, calls, err)
			}
			first, second := got.PullRequests[0], got.PullRequests[1]
			if first.Review != "approved" || first.Checks != "passing" || first.Merge != "ready" || first.Unresolved == nil || *first.Unresolved != 2 || !first.UnresolvedCapped {
				t.Fatalf("first=%+v", first)
			}
			if second.Review != "commented" || second.Checks != "none" || second.Merge != "draft" || second.Unresolved == nil || *second.Unresolved != 0 || second.UnresolvedCapped {
				t.Fatalf("second=%+v", second)
			}
			if got.Rate.Cost == nil || *got.Rate.Cost != 2 || got.Rate.Remaining == nil || *got.Rate.Remaining != 4998 || !got.Rate.Reset.Equal(reset) {
				t.Fatalf("rate=%+v", got.Rate)
			}
			now = got.NextPoll
			failPage = true
			cached, err := s.Poll(context.Background(), repo)
			if err == nil || !cached.Stale || len(cached.PullRequests) != 2 || cached.PullRequests[0].Review != "approved" || strings.Contains(cached.Error, "private secret") {
				t.Fatalf("failed refresh=%+v %v", cached, err)
			}
			failPage = false
			remaining = 0
			now = cached.NextPoll
			// The first page exhausts GraphQL points: it cannot fetch page two and
			// REST's positive remaining header must not restore those points.
			limited, err := s.Poll(context.Background(), repo)
			if err != errRateLimit || limited.Rate.Remaining == nil || *limited.Rate.Remaining != 0 || !limited.NextPoll.Equal(reset) || len(limited.PullRequests) != 2 {
				t.Fatalf("limited=%+v %v", limited, err)
			}
			before := calls
			restarted := New(client, s.directory)
			restarted.now = s.now
			if _, err := restarted.PollNow(context.Background(), repo); err != errRateLimit || calls != before {
				t.Fatalf("shared GraphQL budget not honored: %v calls=%d", err, calls)
			}
		})
	}
}

func TestGraphQLStateMappingsAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		raw, review, checks, merge string
		count                      int
		capped                     bool
	}{
		{`{}`, "", "", "", -1, false},
		{`{"reviewDecision":"CHANGES_REQUESTED","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]},"mergeable":"CONFLICTING"}`, "changes_requested", "failing", "conflict", -1, false},
		{`{"latestOpinionatedReviews":{"nodes":[{"state":"APPROVED"}]},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]},"mergeStateStatus":"BLOCKED"}`, "approved", "running", "blocked", -1, false},
		{`{"latestOpinionatedReviews":{"nodes":[{"state":"APPROVED"},{"state":"CHANGES_REQUESTED"}]},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"ERROR"}}}]},"mergeStateStatus":"BEHIND"}`, "changes_requested", "failing", "blocked", -1, false},
		{`{"latestOpinionatedReviews":{"nodes":[]},"reviewThreads":{"nodes":[]}}`, "unreviewed", "", "", 0, false},
		{`{"latestOpinionatedReviews":{"nodes":[{"state":"APPROVED"}],"pageInfo":{"hasNextPage":true}},"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FUTURE"}}}]},"mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN"}`, "", "", "", -1, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var pr gqlPR
			if err := json.Unmarshal([]byte(tc.raw), &pr); err != nil {
				t.Fatal(err)
			}
			review, checks, merge, count, capped := pr.states()
			if review != tc.review || checks != tc.checks || merge != tc.merge || capped != tc.capped || tc.count < 0 && count != nil || tc.count >= 0 && (count == nil || *count != tc.count) {
				t.Fatalf("states=%s,%s,%s,%v,%t", review, checks, merge, count, capped)
			}
		})
	}
}

func TestGraphQLRequestCapAndRateErrorPreserveCache(t *testing.T) {
	for _, response := range []string{
		`{"data":{"viewer":{"login":"viewer"},"repository":{"pullRequests":{"nodes":[{"number":1,"headRefOid":"head","reviewRequests":{"pageInfo":{"hasNextPage":true}}}]}}}}`,
		`{"errors":[{"type":"RATE_LIMITED"}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			f := recordedFixture()
			s, now := service(t, f)
			if _, err := s.Poll(context.Background(), testRepo); err != nil {
				t.Fatal(err)
			}
			client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/graphql" {
					t.Fatalf("unexpected request %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})})
			s.client = client
			*now = now.Add(time.Hour)
			got, err := s.Poll(context.Background(), testRepo)
			if err == nil || !got.Stale || len(got.PullRequests) != 4 || !got.PullRequests[1].ReviewRequestedFromMe {
				t.Fatalf("snapshot=%+v %v", got, err)
			}
			if strings.Contains(response, "RATE_LIMITED") && (err != errRateLimit || !got.Rate.RetryAt.Equal(now.Add(time.Minute))) {
				t.Fatalf("rate=%+v %v", got.Rate, err)
			}
		})
	}
}

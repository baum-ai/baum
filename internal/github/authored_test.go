package github

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAuthoredPullRequestsSearchesTheViewersOwnAndPages(t *testing.T) {
	node := func(number int, extra map[string]any) map[string]any {
		n := map[string]any{
			"number": number, "isDraft": false, "state": "OPEN", "createdAt": "2026-10-20T10:00:00Z", "mergedAt": nil,
			"mergeable": "MERGEABLE", "author": map[string]any{"login": "me"},
			"reviews": map[string]any{"nodes": []any{}},
			"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}},
		}
		for k, v := range extra {
			n[k] = v
		}
		return n
	}
	fake := newFakeGitHub(t, map[string]route{
		"AuthoredPullRequests": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return data(map[string]any{"search": page([]any{
					node(3, map[string]any{"state": "MERGED", "mergedAt": "2026-10-21T10:00:00Z", "reviews": map[string]any{"nodes": []any{
						map[string]any{"submittedAt": "2026-10-20T09:00:00Z", "author": map[string]any{"login": "me"}},
						map[string]any{"submittedAt": "2026-10-20T12:00:00Z", "author": map[string]any{"login": "reviewer"}},
						map[string]any{"submittedAt": nil, "author": map[string]any{"login": "pending"}},
						map[string]any{"submittedAt": "2026-10-20T11:00:00Z", "author": map[string]any{"login": "other"}},
					}}}),
					node(2, map[string]any{"mergeable": "CONFLICTING"}),
				}, "next")})
			},
			"next": func() fakeResponse {
				return data(map[string]any{"search": page([]any{
					node(1, map[string]any{"isDraft": true, "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "FAILURE"}}}}}}),
					map[string]any{},
				}, "")})
			},
		}),
	})
	since := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	got, err := fake.client(nil).AuthoredPullRequests(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "demo"}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Number != 3 || got[1].Number != 2 || got[2].Number != 1 {
		t.Fatalf("pull requests = %+v", got)
	}
	if got[0].MergedAt == nil || got[0].FirstReviewAt == nil || !got[0].FirstReviewAt.Equal(time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("merged = %+v, want the earliest review by someone else", got[0])
	}
	if !got[1].Conflict || got[1].ChecksFailing {
		t.Errorf("conflicting = %+v", got[1])
	}
	if !got[2].Draft || !got[2].ChecksFailing || got[2].FirstReviewAt != nil {
		t.Errorf("draft = %+v", got[2])
	}
	search, _ := fake.recorded()[0].Variables["search"].(string)
	if want := "repo:acme/demo is:pr author:@me created:>=2026-07-29 sort:created-desc"; search != want {
		t.Errorf("search = %q, want %q", search, want)
	}
	if !strings.Contains(fake.recorded()[0].Query, "query AuthoredPullRequests(") {
		t.Errorf("query = %s", fake.recorded()[0].Query)
	}
}

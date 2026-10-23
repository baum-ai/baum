package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func viewerNode(name string, pushed time.Time, extra map[string]any) map[string]any {
	node := map[string]any{
		"nameWithOwner": name, "isPrivate": false, "isFork": false, "isArchived": false,
		"pushedAt": pushed.Format(time.RFC3339), "description": "about " + name,
	}
	for k, v := range extra {
		node[k] = v
	}
	return node
}

func viewerData(connection map[string]any) fakeResponse {
	return data(map[string]any{"viewer": map[string]any{"repositories": connection}})
}

func searchData(repositories ...string) fakeResponse {
	nodes := []any{}
	for _, r := range repositories {
		nodes = append(nodes, map[string]any{"repository": map[string]any{"nameWithOwner": r}})
	}
	return data(map[string]any{"search": map[string]any{"nodes": nodes}})
}

func TestViewerRepositoriesMapsRankingInputs(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	fake := newFakeGitHub(t, map[string]route{
		"ViewerRepositories": func(gqlRequest, int) fakeResponse {
			return viewerData(page([]any{
				viewerNode("Acme/Widgets", now, map[string]any{"isPrivate": true}),
				viewerNode("acme/old", now.Add(-48*time.Hour), map[string]any{"isFork": true}),
				viewerNode("acme/archived", now.Add(-time.Hour), map[string]any{"isArchived": true}),
				viewerNode("acme/empty", now, map[string]any{"pushedAt": nil, "description": nil}),
				viewerNode("ACME/widgets", now, nil),
			}, ""))
		},
		"ViewerOpenPullRequests": func(gqlRequest, int) fakeResponse {
			return searchData("Acme/Old", "acme/old", "acme/old", "acme/widgets", "other/elsewhere")
		},
	})
	got, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Repository)
	}
	if want := "acme/widgets acme/old acme/empty"; strings.Join(names, " ") != want {
		t.Fatalf("repositories = %v, want %s (newest push first, lowercase, archived and duplicates dropped)", names, want)
	}
	widgets, old, empty := got[0], got[1], got[2]
	if !widgets.Private || widgets.Fork || widgets.OpenPullRequests != 1 || !widgets.PushedAt.Equal(now) || widgets.Description != "about Acme/Widgets" {
		t.Errorf("widgets = %+v", widgets)
	}
	if !old.Fork || old.OpenPullRequests != 3 {
		t.Errorf("old = %+v, want a fork with 3 open pull requests", old)
	}
	if !empty.PushedAt.IsZero() || empty.Description != "" || empty.OpenPullRequests != 0 {
		t.Errorf("empty = %+v", empty)
	}

	requests := fake.recorded()
	for _, r := range requests {
		if r.Authorization != "token test-token" {
			t.Errorf("%s sent authorization %q", r.OperationName, r.Authorization)
		}
	}
	list := requests[0].Query
	for _, want := range []string{"viewer{repositories(first: 100, after: $cursor, affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], orderBy: {field: PUSHED_AT, direction: DESC}, isArchived: false)", "nameWithOwner", "isPrivate", "isFork", "pushedAt", "description"} {
		if !strings.Contains(list, want) {
			t.Errorf("listing query lacks %q:\n%s", want, list)
		}
	}
	if search := requests[1]; !strings.Contains(search.Query, "search(query: $search, type: ISSUE, first: 100)") || search.Variables["search"] != "is:pr is:open involves:@me archived:false" {
		t.Errorf("search = %s %v", search.Query, search.Variables)
	}
}

func TestViewerRepositoriesPagesUpToTheCap(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pageOf := func(first int, next string) fakeResponse {
		nodes := make([]any, 0, 100)
		for i := first; i < first+100; i++ {
			nodes = append(nodes, viewerNode(fmt.Sprintf("acme/r%03d", i), now.Add(-time.Duration(i)*time.Minute), nil))
		}
		return viewerData(page(nodes, next))
	}
	fake := newFakeGitHub(t, map[string]route{
		"ViewerRepositories": pages(t, map[string]func() fakeResponse{
			"":   func() fakeResponse { return pageOf(0, "c1") },
			"c1": func() fakeResponse { return pageOf(100, "c2") },
			"c2": func() fakeResponse { return pageOf(200, "c3") },
			"c3": func() fakeResponse { return pageOf(300, "") },
		}),
		"ViewerOpenPullRequests": func(gqlRequest, int) fakeResponse { return searchData() },
	})
	got, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxViewerRepositories || got[0].Repository != "acme/r000" || got[299].Repository != "acme/r299" {
		t.Fatalf("got %d repositories, first %s last %s", len(got), got[0].Repository, got[len(got)-1].Repository)
	}
	if n := fake.callCount("ViewerRepositories"); n != 3 {
		t.Errorf("read %d pages, want 3", n)
	}
}

func TestViewerRepositoriesKeepsListingWhenTheSearchFails(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{
		"ViewerRepositories":     func(gqlRequest, int) fakeResponse { return viewerData(page([]any{viewerNode("a/b", time.Now(), nil)}, "")) },
		"ViewerOpenPullRequests": func(gqlRequest, int) fakeResponse { return graphQLError(nil, "FORBIDDEN", "SSO") },
	})
	got, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	if err != nil || len(got) != 1 || got[0].OpenPullRequests != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestViewerRepositoriesReportsARateLimit(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{
		"ViewerRepositories": func(gqlRequest, int) fakeResponse {
			return fakeResponse{status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"3600"}}}
		},
	})
	_, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	var limited *RateLimitedError
	if !errors.As(err, &limited) || time.Until(limited.ResumeAt) < 59*time.Minute {
		t.Fatalf("err = %v, want a rate limit resuming in an hour", err)
	}
	if fake.callCount("ViewerOpenPullRequests") != 0 {
		t.Error("searched after the listing failed")
	}
}

func TestViewerRepositoriesFeedsAuthFailuresToTheAuthHook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
	}{
		{"revoked", http.StatusUnauthorized, http.Header{}},
		{"sso", http.StatusForbidden, http.Header{"X-Github-Sso": {"required; url=https://github.com/orgs/acme/sso"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeGitHub(t, map[string]route{
				"ViewerRepositories": func(gqlRequest, int) fakeResponse {
					return fakeResponse{status: tc.status, header: tc.header, body: map[string]any{"message": "Bad credentials"}}
				},
			})
			var seen []int
			var sso string
			c := New(fake.testTokens, Options{Transport: fake.transport(), AuthResult: func(host, token string, status int, h http.Header) {
				if host != "github.com" || token != "test-token" {
					t.Errorf("hook got host %q token %q", host, token)
				}
				seen = append(seen, status)
				sso = h.Get("X-Github-Sso")
			}})
			t.Setenv("GH_CONFIG_DIR", t.TempDir())
			_, err := c.ViewerRepositories(context.Background(), "github.com")
			if err == nil || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("err = %v", err)
			}
			if len(seen) == 0 || seen[0] != tc.status || sso != tc.header.Get("X-Github-Sso") {
				t.Errorf("hook saw statuses %v sso %q", seen, sso)
			}
		})
	}
}

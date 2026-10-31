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
	return data(map[string]any{"viewer": map[string]any{
		"topRepositories": map[string]any{"nodes": connection["nodes"]},
		"repositories":    map[string]any{"totalCount": 147},
	}})
}

func TestViewerRepositoriesReadsTopRepositoriesInOneRequest(t *testing.T) {
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
	})
	got, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got.Repositories {
		names = append(names, r.Repository)
	}
	if want := "acme/widgets acme/old acme/empty"; strings.Join(names, " ") != want {
		t.Fatalf("repositories = %v, want %s (GitHub order, lowercase, archived and duplicates dropped)", names, want)
	}
	if got.TotalCount != 147 {
		t.Errorf("total = %d, want the full accessible count of 147", got.TotalCount)
	}
	widgets, old, empty := got.Repositories[0], got.Repositories[1], got.Repositories[2]
	if !widgets.Private || widgets.Fork || !widgets.PushedAt.Equal(now) || widgets.Description != "about Acme/Widgets" {
		t.Errorf("widgets = %+v", widgets)
	}
	if !old.Fork {
		t.Errorf("old = %+v, want a fork", old)
	}
	if !empty.PushedAt.IsZero() || empty.Description != "" {
		t.Errorf("empty = %+v", empty)
	}

	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("made %d requests, want one top repositories request", len(requests))
	}
	for _, r := range requests {
		if r.Authorization != "token test-token" {
			t.Errorf("%s sent authorization %q", r.OperationName, r.Authorization)
		}
	}
	list := requests[0].Query
	for _, want := range []string{"viewer{topRepositories(first: 20, orderBy: {field: PUSHED_AT, direction: DESC})", "nameWithOwner", "isPrivate", "isFork", "pushedAt", "description", "repositories(first: 1, affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], isArchived: false){totalCount}"} {
		if !strings.Contains(list, want) {
			t.Errorf("listing query lacks %q:\n%s", want, list)
		}
	}
}

func TestViewerRepositoriesFetchesTwentyInOneRequest(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	fake := newFakeGitHub(t, map[string]route{
		"ViewerRepositories": func(gqlRequest, int) fakeResponse {
			nodes := make([]any, 0, MaxViewerRepositories)
			for i := 0; i < MaxViewerRepositories; i++ {
				nodes = append(nodes, viewerNode(fmt.Sprintf("acme/r%03d", i), now.Add(-time.Duration(i)*time.Minute), nil))
			}
			return viewerData(page(nodes, "more"))
		},
	})
	got, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repositories) != MaxViewerRepositories {
		t.Fatalf("got %d repositories, want %d", len(got.Repositories), MaxViewerRepositories)
	}
	if got.Repositories[0].Repository != "acme/r000" || got.Repositories[len(got.Repositories)-1].Repository != "acme/r019" {
		t.Fatalf("repositories = %+v", got.Repositories)
	}
	if n := len(fake.recorded()); n != 1 {
		t.Errorf("made %d requests, want one", n)
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

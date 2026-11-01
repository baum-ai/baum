package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func viewerNode(name string, pushed time.Time, extra map[string]any) map[string]any {
	node := map[string]any{"nameWithOwner": name, "isPrivate": false, "isFork": false, "isArchived": false, "pushedAt": pushed.Format(time.RFC3339), "description": "about " + name}
	for k, v := range extra {
		node[k] = v
	}
	return node
}

func viewerSuggestionsData(connection map[string]any) fakeResponse {
	return data(map[string]any{"viewer": map[string]any{"login": "Me", "organizations": map[string]any{"nodes": []any{map[string]any{"login": "Acme"}, map[string]any{"login": "me"}, map[string]any{"login": "bad owner"}}}, "collaborations": map[string]any{"nodes": []any{map[string]any{"nameWithOwner": "Friend/Shared"}, map[string]any{"nameWithOwner": "acme/inside-an-org"}}}, "topRepositories": connection, "repositories": map[string]any{"totalCount": 147}}})
}

func TestViewerRepositoriesFetchesEightThenOneHundredUsingCursor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fake := newFakeGitHub(t, map[string]route{"ViewerRepositories": func(r gqlRequest, call int) fakeResponse {
		if call == 1 {
			if r.Variables["first"] != float64(8) || r.cursor() != "" {
				t.Errorf("initial request = %v", r.Variables)
			}
			return viewerSuggestionsData(page([]any{viewerNode("Acme/Widgets", now, map[string]any{"isPrivate": true}), viewerNode("acme/archive", now, map[string]any{"isArchived": true})}, "after-eight"))
		}
		if r.Variables["first"] != float64(100) || r.cursor() != "after-eight" {
			t.Errorf("next request = %v", r.Variables)
		}
		return viewerSuggestionsData(page([]any{viewerNode("acme/next", now, map[string]any{"isFork": true})}, "still-more"))
	}})
	c := fake.client(nil)
	first, err := c.ViewerRepositories(context.Background(), "github.com", InitialViewerRepositories, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Repositories) != 1 || first.Repositories[0].Repository != "acme/widgets" || !first.Repositories[0].Private || first.TotalCount != 147 || first.NextCursor == nil || *first.NextCursor != "after-eight" || strings.Join(first.Scope.Owners, " ") != "me acme" || strings.Join(first.Scope.Repositories, " ") != "friend/shared" {
		t.Fatalf("first = %+v", first)
	}
	more, err := c.ViewerRepositories(context.Background(), "github.com", AdditionalViewerRepositories, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(more.Repositories) != 1 || !more.Repositories[0].Fork || fake.callCount("ViewerRepositories") != 2 {
		t.Fatalf("more = %+v", more)
	}
	query := fake.recorded()[0].Query
	for _, want := range []string{"login", "organizations(first: 100)", "collaborations: repositories(first: 100, affiliations: [COLLABORATOR]", "topRepositories(first: $first, after: $cursor", "field: PUSHED_AT", "pageInfo", "repositories(first: 1", "totalCount"} {
		if !strings.Contains(query, want) {
			t.Errorf("query lacks %s: %s", want, query)
		}
	}
}

func TestSearchRepositoriesUsesGitHubSearchWithoutEnumeratingRepositories(t *testing.T) {
	for _, tc := range []struct{ input, search string }{
		{"widgets", "widgets user:me user:acme repo:friend/shared in:name archived:false fork:true"},
		// A named owner narrows the scope; acme's repositories are all in it.
		{"acme/widgets", "widgets user:acme in:name archived:false fork:true"},
		{"org:acme widgets", "widgets user:acme in:name archived:false fork:true"},
		// Only the collaboration, never the friend's other repositories.
		{"friend/shared", "shared repo:friend/shared in:name archived:false fork:true"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			fake := newFakeGitHub(t, map[string]route{"SearchRepositories": func(r gqlRequest, _ int) fakeResponse {
				if r.Variables["search"] != tc.search {
					t.Errorf("search = %v, want %s", r.Variables["search"], tc.search)
				}
				return data(map[string]any{"search": map[string]any{"repositoryCount": 200, "nodes": []any{viewerNode("New-Org/Widgets", time.Now(), map[string]any{"isPrivate": true, "isFork": true}), viewerNode("archived/widgets", time.Now(), map[string]any{"isArchived": true}), viewerNode("NEW-ORG/widgets", time.Now(), nil)}}})
			}})
			got, err := fake.client(nil).SearchRepositories(context.Background(), "github.com", tc.input, SearchScope{Owners: []string{"Me", "acme", "me", "not an owner"}, Repositories: []string{"Friend/Shared", "acme/inside-acme", "bad"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Repositories) != 1 || got.Repositories[0].Repository != "new-org/widgets" || !got.Repositories[0].Private || !got.Repositories[0].Fork || got.TotalCount != 200 {
				t.Fatalf("results = %+v", got)
			}
			if fake.callCount("ViewerRepositories") != 0 || len(fake.recorded()) != 1 || !strings.Contains(fake.recorded()[0].Query, "search(query: $search, type: REPOSITORY, first: 100, after: $cursor)") {
				t.Fatal("search must make exactly one repository search request")
			}
		})
	}
}

func TestViewerRepositoriesRejectsInvalidCursor(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"ViewerRepositories": func(gqlRequest, int) fakeResponse {
		return viewerSuggestionsData(map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": nil}})
	}})
	_, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com", 8, nil)
	if !errors.Is(err, errInvalidPagination) {
		t.Fatalf("err = %v", err)
	}
}

func TestViewerRepositoriesReportsARateLimit(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"ViewerRepositories": func(gqlRequest, int) fakeResponse {
		return fakeResponse{status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"3600"}}}
	}})
	_, err := fake.client(nil).ViewerRepositories(context.Background(), "github.com", 8, nil)
	var limited *RateLimitedError
	if !errors.As(err, &limited) || time.Until(limited.ResumeAt) < 59*time.Minute {
		t.Fatalf("err = %v", err)
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
			_, err := c.ViewerRepositories(context.Background(), "github.com", InitialViewerRepositories, nil)
			if err == nil || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("err = %v", err)
			}
			if len(seen) == 0 || seen[0] != tc.status || sso != tc.header.Get("X-Github-Sso") {
				t.Errorf("hook saw statuses %v sso %q", seen, sso)
			}
		})
	}
}

func TestSearchRepositoriesOutsideTheScopeNeverSearchesAllOfGitHub(t *testing.T) {
	scope := SearchScope{Owners: []string{"me"}, Repositories: []string{"friend/shared"}}
	for _, tc := range []struct {
		query string
		scope SearchScope
	}{{"python", SearchScope{}}, {"stranger/python", scope}, {"user:stranger python", scope}} {
		fake := newFakeGitHub(t, map[string]route{})
		got, err := fake.client(nil).SearchRepositories(context.Background(), "github.com", tc.query, tc.scope, nil)
		if err != nil || len(got.Repositories) != 0 || len(fake.recorded()) != 0 {
			t.Fatalf("%s: search = %+v, %v, %d requests", tc.query, got, err, len(fake.recorded()))
		}
	}
}

func TestSearchRepositoriesPagesWithItsCursor(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"SearchRepositories": func(r gqlRequest, call int) fakeResponse {
		connection := page([]any{viewerNode("acme/one", time.Now(), nil)}, "after-one")
		if call == 2 {
			if r.cursor() != "after-one" {
				t.Errorf("second cursor = %q", r.cursor())
			}
			connection = page([]any{viewerNode("acme/two", time.Now(), nil)}, "")
		}
		connection["repositoryCount"] = 2
		return data(map[string]any{"search": connection})
	}})
	c := fake.client(nil)
	first, err := c.SearchRepositories(context.Background(), "github.com", "acme", SearchScope{Owners: []string{"acme"}}, nil)
	if err != nil || first.NextCursor == nil || *first.NextCursor != "after-one" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := c.SearchRepositories(context.Background(), "github.com", "acme", SearchScope{Owners: []string{"acme"}}, first.NextCursor)
	if err != nil || second.NextCursor != nil || second.Repositories[0].Repository != "acme/two" {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadIssue(t *testing.T) {
	reply := `{"data":{"repository":{"issue":{"number":3,"title":"Bug","state":"CLOSED","stateReason":"COMPLETED","url":"https://x/3","createdAt":"2026-01-01T00:00:00Z","closedAt":"2026-01-02T00:00:00Z","body":"It breaks.","author":{"login":"alice"},
	"labels":{"nodes":[{"name":"bug"},{"name":"p1"}]},
	"comments":{"totalCount":60,"nodes":[{"author":{"login":"bob"},"body":"me too","createdAt":"2026-01-01T01:00:00Z","url":"https://x/3#c"},{"author":null,"body":"ghost","createdAt":"2026-01-01T02:00:00Z","url":"u"}]},
	"closedByPullRequestsReferences":{"nodes":[{"number":9,"title":"Fix","prState":"MERGED","url":"https://x/9","repository":{"nameWithOwner":"acme/app"}}]},
	"timelineItems":{"nodes":[
		{"__typename":"ConnectedEvent","subject":{"__typename":"PullRequest","number":9,"title":"Fix","prState":"MERGED","url":"https://x/9","repository":{"nameWithOwner":"acme/app"}}},
		{"__typename":"CrossReferencedEvent","source":{"__typename":"PullRequest","number":10,"title":"Try","prState":"OPEN","url":"https://x/10","repository":{"nameWithOwner":"acme/app"}}},
		{"__typename":"CrossReferencedEvent","source":{"__typename":"Issue","number":4,"title":"Dup","issueState":"OPEN","url":"https://x/4","repository":{"nameWithOwner":"acme/app"}}}]}}}}}`
	var sent []string
	client := threadsClient(func(string, map[string]any) string { return reply }, &sent)
	issue, err := client.ReadIssue(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Repository != "acme/app" || issue.Title != "Bug" || issue.State != "CLOSED" || issue.StateReason != "COMPLETED" || issue.Author != "alice" || issue.ClosedAt == nil || issue.Body != "It breaks." || strings.Join(issue.Labels, ",") != "bug,p1" {
		t.Fatalf("issue %+v", issue)
	}
	if issue.CommentsTotal != 60 || len(issue.Comments) != 2 || issue.Comments[0].Author != "bob" || issue.Comments[1].Author != "" {
		t.Fatalf("comments %+v", issue.Comments)
	}
	if len(issue.LinkedPullRequests) != 2 || issue.LinkedPullRequests[0].Number != 9 || issue.LinkedPullRequests[0].State != "MERGED" || issue.LinkedPullRequests[1].Number != 10 {
		t.Fatalf("linked %+v", issue.LinkedPullRequests)
	}
}

func TestReadIssueNotFound(t *testing.T) {
	for _, reply := range []string{
		`{"data":{"repository":{"issue":null}}}`,
		`{"data":{"repository":{"issue":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to an Issue"}]}`,
	} {
		var sent []string
		client := threadsClient(func(string, map[string]any) string { return reply }, &sent)
		_, err := client.ReadIssue(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 3)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err %v", reply, err)
		}
	}
}

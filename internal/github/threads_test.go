package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// threadsClient answers each GraphQL document with reply(query, variables)
// and records the documents it was sent.
func threadsClient(reply func(query string, variables map[string]any) string, sent *[]string) *Client {
	return New(func(context.Context, string) (string, error) { return "token", nil }, Options{Transport: restTransportFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*sent = append(*sent, body.Query)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply(body.Query, body.Variables))), Request: r}, nil
	})})
}

func TestUnresolvedThreadsReadsEveryPage(t *testing.T) {
	repo := Repository{Host: "github.com", Owner: "acme", Name: "app"}
	pages := []string{
		`{"data":{"repository":{"pullRequest":{"headRefOid":"abc","viewerDidAuthor":true,"reviewThreads":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
			{"id":"RT_1","path":"a.go","line":12,"isResolved":false,"isOutdated":false,"viewerCanReply":true,"viewerCanResolve":true,"comments":{"totalCount":3,"nodes":[
				{"state":"SUBMITTED","authorAssociation":"MEMBER","body":"Rename this.","author":{"__typename":"User","login":"alice"}},
				{"state":"PENDING","authorAssociation":"OWNER","body":"draft","author":{"__typename":"User","login":"me"}},
				{"state":"SUBMITTED","authorAssociation":"NONE","body":"nit","author":{"__typename":"Bot","login":"reviewbot"}}]}},
			{"id":"RT_2","path":"b.go","line":3,"isResolved":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"done"}]}}]}}}}}`,
		`{"data":{"repository":{"pullRequest":{"headRefOid":"abc","viewerDidAuthor":true,"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
			{"id":"RT_3","path":"c.go","line":null,"originalLine":7,"isResolved":false,"isOutdated":true,"viewerCanReply":true,"viewerCanResolve":false,"comments":{"totalCount":1,"nodes":[
				{"state":"SUBMITTED","authorAssociation":"COLLABORATOR","body":"Old.","author":null}]}}]}}}}}`,
	}
	var sent []string
	page := 0
	client := threadsClient(func(query string, variables map[string]any) string {
		if !strings.Contains(query, "query FixReviewThreads(") {
			t.Errorf("query %s", query)
		}
		if (page == 0) != (variables["cursor"] == nil) {
			t.Errorf("page %d cursor %v", page, variables["cursor"])
		}
		page++
		return pages[page-1]
	}, &sent)
	threads, err := client.UnresolvedThreads(context.Background(), repo, 7)
	if err != nil {
		t.Fatal(err)
	}
	if threads.HeadSHA != "abc" || !threads.ViewerDidAuthor || threads.Capped || len(threads.Unresolved) != 2 {
		t.Fatalf("threads %+v", threads)
	}
	first, outdated := threads.Unresolved[0], threads.Unresolved[1]
	if first.ID != "RT_1" || first.Line != 12 || first.Total != 2 || len(first.Comments) != 2 || first.Comments[0].Author != "alice" || first.Comments[0].Association != "MEMBER" || !first.Comments[1].Bot {
		t.Fatalf("first thread %+v", first)
	}
	if outdated.Line != 7 || !outdated.Outdated || outdated.CanResolve || outdated.Comments[0].Author != "" {
		t.Fatalf("outdated thread %+v", outdated)
	}
}

func TestUnresolvedThreadsRefusesAMovedHead(t *testing.T) {
	var sent []string
	page := 0
	client := threadsClient(func(string, map[string]any) string {
		page++
		head, next := "abc", "true"
		if page == 2 {
			head, next = "def", "false"
		}
		return `{"data":{"repository":{"pullRequest":{"headRefOid":"` + head + `","reviewThreads":{"pageInfo":{"hasNextPage":` + next + `,"endCursor":"c` + head + `"},"nodes":[]}}}}}`
	}, &sent)
	if _, err := client.UnresolvedThreads(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err %v", err)
	}
}

func TestThreadWrites(t *testing.T) {
	repo := Repository{Host: "github.com", Owner: "acme", Name: "app"}
	var variables []map[string]any
	var sent []string
	replies := map[string]string{
		"query ReviewThreadState(":      `{"data":{"node":{"id":"RT_1","isResolved":false,"viewerCanReply":true,"viewerCanResolve":true,"pullRequest":{"number":7,"repository":{"nameWithOwner":"acme/app"}}}}}`,
		"mutation ReplyToReviewThread(": `{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"C_1"}}}}`,
		"mutation ResolveReviewThread(": `{"data":{"resolveReviewThread":{"thread":{"id":"RT_1","isResolved":true}}}}`,
	}
	client := threadsClient(func(query string, v map[string]any) string {
		variables = append(variables, v)
		for prefix, reply := range replies {
			if strings.Contains(query, prefix) {
				return reply
			}
		}
		t.Errorf("unexpected document %s", query)
		return `{}`
	}, &sent)
	ctx := context.Background()
	state, err := client.ThreadState(ctx, repo, "RT_1")
	if err != nil || state != (ThreadState{Repository: "acme/app", Number: 7, CanReply: true, CanResolve: true}) {
		t.Fatalf("state %+v err %v", state, err)
	}
	if err := client.ReplyToThread(ctx, repo, "RT_1", "Addressed in abc1234: Renamed it."); err != nil {
		t.Fatal(err)
	}
	if err := client.ResolveThread(ctx, repo, "RT_1"); err != nil {
		t.Fatal(err)
	}
	if variables[1]["thread"] != "RT_1" || variables[1]["body"] != "Addressed in abc1234: Renamed it." || variables[2]["thread"] != "RT_1" {
		t.Fatalf("variables %v", variables)
	}
	// GitHub's own error text is not repeated.
	replies["mutation ReplyToReviewThread("] = `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token <script>"}]}`
	if err := client.ReplyToThread(ctx, repo, "RT_1", "x"); err == nil || strings.Contains(err.Error(), "script") || !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Fatalf("err %v", err)
	}
	replies["mutation ResolveReviewThread("] = `{"data":{"resolveReviewThread":{"thread":{"id":"RT_1","isResolved":false}}}}`
	if err := client.ResolveThread(ctx, repo, "RT_1"); err == nil {
		t.Fatal("an unconfirmed resolution passed")
	}
	replies["query ReviewThreadState("] = `{"data":{"node":null}}`
	if _, err := client.ThreadState(ctx, repo, "RT_1"); err == nil {
		t.Fatal("a missing thread passed")
	}
}

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

func reviewClient(status int, reply string, requests *[]*http.Request, bodies *[]map[string]any) *Client {
	return New(func(context.Context, string) (string, error) { return "token", nil }, Options{Transport: restTransportFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*requests = append(*requests, r)
		*bodies = append(*bodies, body)
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply)), Request: r}, nil
	})})
}

func TestSubmitReviewSendsOnePayloadAndReturnsTheURL(t *testing.T) {
	var requests []*http.Request
	var bodies []map[string]any
	client := reviewClient(200, `{"id":9,"html_url":"https://github.com/acme/app/pull/7#pullrequestreview-9"}`, &requests, &bodies)
	result, err := client.SubmitReview(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7, ReviewSubmission{
		CommitID: "abc", Body: "Looks good", Event: ReviewEventRequestChanges,
		Comments: []ReviewComment{
			{Path: "a/b.go", Line: 42, Side: SideRight, Body: "single"},
			{Path: "c.go", Line: 12, Side: SideLeft, StartLine: 10, StartSide: SideLeft, Body: "range"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "https://github.com/acme/app/pull/7#pullrequestreview-9" {
		t.Fatalf("result: %+v", result)
	}
	if len(requests) != 1 || requests[0].Method != "POST" || requests[0].URL.Path != "/repos/acme/app/pulls/7/reviews" {
		t.Fatalf("requests: %v", requests)
	}
	body := bodies[0]
	comments := body["comments"].([]any)
	first, second := comments[0].(map[string]any), comments[1].(map[string]any)
	if body["commit_id"] != "abc" || body["event"] != "REQUEST_CHANGES" || body["body"] != "Looks good" || len(comments) != 2 {
		t.Fatalf("body: %v", body)
	}
	if first["path"] != "a/b.go" || first["line"] != float64(42) || first["side"] != "RIGHT" || first["body"] != "single" {
		t.Fatalf("first comment: %v", first)
	}
	if _, ok := first["start_line"]; ok {
		t.Fatalf("single-line comment has a range: %v", first)
	}
	if second["start_line"] != float64(10) || second["start_side"] != "LEFT" || second["side"] != "LEFT" {
		t.Fatalf("range comment: %v", second)
	}
}

func TestSubmitReviewOmitsEmptyBodyAndComments(t *testing.T) {
	var requests []*http.Request
	var bodies []map[string]any
	client := reviewClient(200, `{"html_url":"u"}`, &requests, &bodies)
	if _, err := client.SubmitReview(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7, ReviewSubmission{CommitID: "abc", Event: ReviewEventApprove}); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["comments"]; ok {
		t.Fatalf("body: %v", bodies[0])
	}
}

func TestSubmitReviewErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reply  string
		code   string
		text   string
	}{
		{"unauthorized", 401, `{"message":"Bad credentials"}`, ReviewForbidden, "Reconnect"},
		{"forbidden", 403, `{"message":"Resource not accessible by personal access token"}`, ReviewForbidden, "not allowed"},
		{"rate limited", 403, `{"message":"API rate limit exceeded"}`, ReviewFailed, "limiting"},
		{"not found", 404, `{"message":"Not Found"}`, ReviewNotFound, "could not find"},
		{"own approve", 422, `{"message":"Unprocessable Entity","errors":["Can not approve your own pull request"]}`, ReviewOwnPull, "own pull request"},
		{"own request changes", 422, `{"message":"Unprocessable Entity","errors":["Can not request changes on your own pull request"]}`, ReviewOwnPull, "own pull request"},
		{"line", 422, `{"message":"Unprocessable Entity","errors":["Line could not be resolved"]}`, ReviewInvalidLine, "diff"},
		{"position", 422, `{"message":"Unprocessable Entity","errors":[{"resource":"PullRequestReviewComment","code":"custom","field":"pull_request_review_thread.line","message":"pull_request_review_thread.line must be part of the diff"}]}`, ReviewInvalidLine, "diff"},
		{"path", 422, `{"message":"Unprocessable Entity","errors":["Path could not be resolved"]}`, ReviewInvalidLine, "diff"},
		{"other 422", 422, `{"message":"Validation Failed","errors":["Something odd"]}`, ReviewFailed, "Something odd"},
		{"server", 500, `not json`, ReviewFailed, "HTTP 500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var requests []*http.Request
			var bodies []map[string]any
			_, err := reviewClient(c.status, c.reply, &requests, &bodies).SubmitReview(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7, ReviewSubmission{CommitID: "abc", Event: ReviewEventComment, Body: "x"})
			var refusal *ReviewError
			if !errors.As(err, &refusal) || refusal.Code != c.code || !strings.Contains(refusal.Message, c.text) {
				t.Fatalf("error %v, want code %s containing %q", err, c.code, c.text)
			}
			if len(requests) != 1 {
				t.Fatalf("%d requests, want exactly one", len(requests))
			}
		})
	}
}

func TestSubmitReviewNetworkFailure(t *testing.T) {
	client := New(func(context.Context, string) (string, error) { return "token", nil }, Options{Transport: restTransportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})})
	_, err := client.SubmitReview(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7, ReviewSubmission{CommitID: "abc", Event: ReviewEventApprove})
	var refusal *ReviewError
	if !errors.As(err, &refusal) || refusal.Code != ReviewFailed || !strings.Contains(refusal.Message, "could not reach GitHub") {
		t.Fatalf("error: %v", err)
	}
}

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

// mergeClient answers the options query for a pull request in the given state
// and head, and the mutation with mutationReply, a raw GraphQL body.
func mergeClient(head, state, mutationReply string, mutations *[]map[string]any) *Client {
	return New(func(context.Context, string) (string, error) { return "token", nil }, Options{Transport: restTransportFunc(func(r *http.Request) (*http.Response, error) {
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply := `{"data":{"repository":{"mergeCommitAllowed":true,"squashMergeAllowed":true,"rebaseMergeAllowed":false,"viewerDefaultMergeMethod":"SQUASH","pullRequest":{"id":"PR_1","state":"` + state + `","isDraft":false,"headRefOid":"` + head + `","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}}}}`
		if strings.Contains(body.Query, "mutation MergePullRequest") {
			*mutations = append(*mutations, body.Variables["input"].(map[string]any))
			reply = mutationReply
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply)), Request: r}, nil
	})})
}

func TestMergeOptionsListsAllowedMethods(t *testing.T) {
	var mutations []map[string]any
	options, err := mergeClient("abc", "OPEN", "", &mutations).MergeOptions(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(options.Methods, ",") != "MERGE,SQUASH" || options.Default != "SQUASH" || options.HeadSHA != "abc" || options.MergeStateStatus != "CLEAN" {
		t.Fatalf("options: %+v", options)
	}
}

func TestMergePullRequest(t *testing.T) {
	repo := Repository{Host: "github.com", Owner: "acme", Name: "app"}
	merged := `{"data":{"mergePullRequest":{"pullRequest":{"merged":true,"state":"MERGED"}}}}`
	cases := []struct {
		name, head, state, method, reply, code string
		mutations                              int
	}{
		{"success", "abc", "OPEN", "SQUASH", merged, "", 1},
		{"head moved before the request", "new", "OPEN", "SQUASH", merged, MergeHeadMoved, 0},
		{"closed", "abc", "CLOSED", "SQUASH", merged, MergeNotOpen, 0},
		{"method not allowed", "abc", "OPEN", "REBASE", merged, MergeMethod, 0},
		{"head moved at GitHub", "abc", "OPEN", "MERGE", `{"errors":[{"type":"UNPROCESSABLE","message":"Head branch was modified. Review and try the merge again."}]}`, MergeHeadMoved, 1},
		{"conflicts", "abc", "OPEN", "MERGE", `{"errors":[{"type":"UNPROCESSABLE","message":"Pull request is not mergeable"}]}`, MergeConflict, 1},
		{"checks", "abc", "OPEN", "MERGE", `{"errors":[{"type":"UNPROCESSABLE","message":"Required status check \"build\" is expected."}]}`, MergeBlocked, 1},
		{"branch protection", "abc", "OPEN", "MERGE", `{"errors":[{"type":"UNPROCESSABLE","message":"At least 1 approving review is required by reviewers with write access."}]}`, MergeBlocked, 1},
		{"permission", "abc", "OPEN", "MERGE", `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`, MergeForbidden, 1},
		{"unknown", "abc", "OPEN", "MERGE", `{"errors":[{"type":"INTERNAL","message":"Something odd"}]}`, MergeFailed, 1},
		{"not confirmed", "abc", "OPEN", "MERGE", `{"data":{"mergePullRequest":{"pullRequest":{"merged":false,"state":"OPEN"}}}}`, MergeFailed, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mutations []map[string]any
			err := mergeClient(c.head, c.state, c.reply, &mutations).MergePullRequest(context.Background(), repo, 7, c.method, "abc")
			var refusal *MergeError
			switch {
			case c.code == "" && err != nil:
				t.Fatal(err)
			case c.code != "" && (!errors.As(err, &refusal) || refusal.Code != c.code):
				t.Fatalf("error %v, want code %s", err, c.code)
			}
			if len(mutations) != c.mutations {
				t.Fatalf("%d mutations, want %d", len(mutations), c.mutations)
			}
			if c.mutations == 1 {
				input := mutations[0]
				if input["pullRequestId"] != "PR_1" || input["mergeMethod"] != c.method || input["expectedHeadOid"] != "abc" {
					t.Fatalf("input: %v", input)
				}
			}
		})
	}
}

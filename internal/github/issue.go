package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// IssueComments is how many of an issue's first comments are read.
const IssueComments = 50

const issueQuery = `query Issue($owner: String!, $name: String!, $number: Int!) {
 repository(owner: $owner, name: $name) {
  issue(number: $number) {
   number title state stateReason url createdAt closedAt body
   author { login }
   labels(first: 30) { nodes { name } }
   comments(first: 50) { totalCount nodes { author { login } body createdAt url } }
   closedByPullRequestsReferences(first: 10) { nodes { ` + issueLinkSelection + ` } }
   timelineItems(first: 50, itemTypes: [CONNECTED_EVENT, CROSS_REFERENCED_EVENT]) {
    nodes {
     __typename
     ... on ConnectedEvent { subject { ` + referenceSelection + ` } }
     ... on CrossReferencedEvent { source { ` + referenceSelection + ` } }
    }
   }
  }
 }
}`

const issueLinkSelection = `number title prState: state url repository { nameWithOwner }`

// IssueComment is one comment on an issue; Body is untrusted.
type IssueComment struct {
	Author    string
	Body      string
	CreatedAt time.Time
	URL       string
}

// Issue is a GitHub issue as read. Title, Body and the comments are
// untrusted.
type Issue struct {
	Repository  string
	Number      int
	Title       string
	State       string
	StateReason string
	URL         string
	Author      string
	CreatedAt   time.Time
	ClosedAt    *time.Time
	Body        string
	Labels      []string
	// CommentsTotal counts all the comments; Comments are the first
	// IssueComments.
	CommentsTotal int
	Comments      []IssueComment
	// LinkedPullRequests are the pull requests that close or mention the
	// issue.
	LinkedPullRequests []IssueReference
}

// ReadIssue reads one issue. A number that is a pull request, or no issue,
// fails with ErrNotFound.
func (c *Client) ReadIssue(ctx context.Context, repository Repository, number int) (Issue, error) {
	reply, err := c.post(ctx, repository, issueQuery, map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number})
	if err != nil {
		return Issue{}, err
	}
	if len(reply.Errors) > 0 {
		if reply.Errors[0].Type == "NOT_FOUND" {
			return Issue{}, fmt.Errorf("%w: issue %s#%d", ErrNotFound, repository, number)
		}
		return Issue{}, fmt.Errorf("reading the issue: GitHub refused the request (%s)", errorType(reply.Errors[0].Type))
	}
	var data struct {
		Repository *struct {
			Issue *struct {
				Number                         int
				Title, State, StateReason, URL string
				CreatedAt                      time.Time
				ClosedAt                       *time.Time
				Body                           string
				Author                         *loginNode
				Labels                         struct{ Nodes []struct{ Name string } }
				Comments                       struct {
					TotalCount int
					Nodes      []struct {
						Author    *loginNode
						Body, URL string
						CreatedAt time.Time
					}
				}
				ClosedByPullRequestsReferences struct{ Nodes []referenceNode }
				TimelineItems                  struct {
					Nodes []struct {
						Typename string `json:"__typename"`
						Subject  *referenceNode
						Source   *referenceNode
					}
				}
			}
		}
	}
	if json.Unmarshal(reply.Data, &data) != nil {
		return Issue{}, errors.New("GitHub's answer could not be read")
	}
	if data.Repository == nil || data.Repository.Issue == nil {
		return Issue{}, fmt.Errorf("%w: issue %s#%d", ErrNotFound, repository, number)
	}
	i := data.Repository.Issue
	out := Issue{Repository: repository.String(), Number: i.Number, Title: i.Title, State: i.State, StateReason: i.StateReason, URL: i.URL, CreatedAt: i.CreatedAt, ClosedAt: i.ClosedAt, Body: i.Body, CommentsTotal: i.Comments.TotalCount}
	if i.Author != nil {
		out.Author = i.Author.Login
	}
	for _, label := range i.Labels.Nodes {
		out.Labels = append(out.Labels, label.Name)
	}
	for _, comment := range i.Comments.Nodes {
		entry := IssueComment{Body: comment.Body, CreatedAt: comment.CreatedAt, URL: comment.URL}
		if comment.Author != nil {
			entry.Author = comment.Author.Login
		}
		out.Comments = append(out.Comments, entry)
	}
	seen := map[string]bool{}
	link := func(r *referenceNode) {
		if r == nil || r.Typename != "PullRequest" {
			return
		}
		ref := r.reference()
		key := fmt.Sprintf("%s#%d", ref.Repository, ref.Number)
		if !seen[key] {
			seen[key] = true
			out.LinkedPullRequests = append(out.LinkedPullRequests, *ref)
		}
	}
	for _, r := range i.ClosedByPullRequestsReferences.Nodes {
		r.Typename = "PullRequest"
		link(&r)
	}
	for _, node := range i.TimelineItems.Nodes {
		link(node.Subject)
		link(node.Source)
	}
	return out, nil
}

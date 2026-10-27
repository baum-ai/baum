package github

import (
	"context"
	"strings"
	"time"

	graphql "github.com/cli/shurcooL-graphql"
)

// MaxAuthoredPullRequests caps the pull requests AuthoredPullRequests reads
// for one repository: five pages of the search.
const MaxAuthoredPullRequests = 250

// AuthoredPullRequest is one pull request the signed-in account opened, with
// what delivery figures need of it.
type AuthoredPullRequest struct {
	Number    int
	Draft     bool
	State     string
	CreatedAt time.Time
	MergedAt  *time.Time
	// FirstReviewAt is the earliest submitted review by someone other than
	// the author, among the first twenty reviews; nil when there is none.
	FirstReviewAt *time.Time
	// Conflict is GitHub reporting an open pull request as conflicting now.
	Conflict bool
	// ChecksFailing is the head commit's combined check state being a failure
	// or an error now.
	ChecksFailing bool
}

type authoredPullRequestNode struct {
	Number    int
	IsDraft   bool
	State     string
	CreatedAt time.Time
	MergedAt  *time.Time
	Mergeable string
	Author    *struct{ Login string }
	Reviews   struct {
		Nodes []struct {
			SubmittedAt *time.Time
			Author      *struct{ Login string }
		}
	} `graphql:"reviews(first: 20)"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	} `graphql:"commits(last: 1)"`
}

type authoredPullRequestsQuery struct {
	Search struct {
		Nodes []struct {
			PullRequest authoredPullRequestNode `graphql:"... on PullRequest"`
		}
		PageInfo pageInfo
	} `graphql:"search(query: $search, type: ISSUE, first: 50, after: $cursor)"`
}

// AuthoredPullRequests lists the pull requests the token's account opened in
// repository since the given day, newest first, with one search query a page,
// up to MaxAuthoredPullRequests. It reads only; the conflict and check states
// are what GitHub reports for the head now, not their history.
func (c *Client) AuthoredPullRequests(ctx context.Context, repository Repository, since time.Time) ([]AuthoredPullRequest, error) {
	search := "repo:" + repository.Owner + "/" + repository.Name + " is:pr author:@me created:>=" + since.UTC().Format(time.DateOnly) + " sort:created-desc"
	var out []AuthoredPullRequest
	var cursor *string
	for len(out) < MaxAuthoredPullRequests {
		var q authoredPullRequestsQuery
		if err := c.query(ctx, repository.Host, "AuthoredPullRequests", &q, map[string]any{"search": graphql.String(search), "cursor": (*graphql.String)(cursor)}); err != nil {
			return nil, err
		}
		for _, node := range q.Search.Nodes {
			if pr := node.PullRequest; pr.Number > 0 {
				out = append(out, authoredOf(pr))
			}
		}
		info := q.Search.PageInfo
		if !info.HasNextPage {
			break
		}
		if info.EndCursor == nil || (cursor != nil && *cursor == *info.EndCursor) {
			return nil, errInvalidPagination
		}
		cursor = info.EndCursor
	}
	if len(out) > MaxAuthoredPullRequests {
		out = out[:MaxAuthoredPullRequests]
	}
	return out, nil
}

func authoredOf(n authoredPullRequestNode) AuthoredPullRequest {
	pr := AuthoredPullRequest{
		Number: n.Number, Draft: n.IsDraft, State: n.State, CreatedAt: n.CreatedAt, MergedAt: n.MergedAt,
		Conflict: n.State == "OPEN" && n.Mergeable == "CONFLICTING",
	}
	author := ""
	if n.Author != nil {
		author = n.Author.Login
	}
	for _, review := range n.Reviews.Nodes {
		if review.SubmittedAt == nil || (review.Author != nil && author != "" && strings.EqualFold(review.Author.Login, author)) {
			continue
		}
		if pr.FirstReviewAt == nil || review.SubmittedAt.Before(*pr.FirstReviewAt) {
			at := *review.SubmittedAt
			pr.FirstReviewAt = &at
		}
	}
	if len(n.Commits.Nodes) > 0 {
		if rollup := n.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
			pr.ChecksFailing = rollup.State == "FAILURE" || rollup.State == "ERROR"
		}
	}
	return pr
}

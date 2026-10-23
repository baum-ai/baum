package github

import (
	"context"
	"sort"
	"strings"
	"time"

	graphql "github.com/cli/shurcooL-graphql"
)

// MaxViewerRepositories caps the repositories ViewerRepositories reads.
const MaxViewerRepositories = 300

// ViewerRepository is a repository the signed-in account can reach, with what
// ranks it for picking.
type ViewerRepository struct {
	// Repository is the lowercase owner/name.
	Repository  string
	Private     bool
	Fork        bool
	PushedAt    time.Time
	Description string
	// OpenPullRequests counts the open pull requests that involve the account
	// in this repository, within the newest hundred such pull requests.
	OpenPullRequests int
}

type viewerRepositoryNode struct {
	NameWithOwner string
	IsPrivate     bool
	IsFork        bool
	IsArchived    bool
	PushedAt      *time.Time
	Description   string
}

type viewerRepositoriesQuery struct {
	Viewer struct {
		Repositories struct {
			Nodes    []viewerRepositoryNode
			PageInfo pageInfo
		} `graphql:"repositories(first: 100, after: $cursor, affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], orderBy: {field: PUSHED_AT, direction: DESC}, isArchived: false)"`
	}
}

type viewerPullRequestsQuery struct {
	Search struct {
		Nodes []struct {
			PullRequest struct {
				Repository struct {
					NameWithOwner string
				}
			} `graphql:"... on PullRequest"`
		}
	} `graphql:"search(query: $search, type: ISSUE, first: 100)"`
}

const viewerPullRequestSearch = "is:pr is:open involves:@me archived:false"

// ViewerRepositories lists the repositories the token's account owns,
// collaborates on or reaches through an organization, newest push first, up to
// MaxViewerRepositories. Archived repositories are left out. Open pull request
// counts come from one search; if it fails the counts are zero, since ranking
// is a convenience.
func (c *Client) ViewerRepositories(ctx context.Context, host string) ([]ViewerRepository, error) {
	var nodes []viewerRepositoryNode
	var cursor *string
	for range maxPages {
		var q viewerRepositoriesQuery
		if err := c.query(ctx, host, "ViewerRepositories", &q, map[string]any{"cursor": (*graphql.String)(cursor)}); err != nil {
			return nil, err
		}
		nodes = append(nodes, q.Viewer.Repositories.Nodes...)
		info := q.Viewer.Repositories.PageInfo
		if !info.HasNextPage || len(nodes) >= MaxViewerRepositories {
			break
		}
		if info.EndCursor == nil || (cursor != nil && *cursor == *info.EndCursor) {
			return nil, errInvalidPagination
		}
		cursor = info.EndCursor
	}
	counts := map[string]int{}
	var pulls viewerPullRequestsQuery
	switch err := c.query(ctx, host, "ViewerOpenPullRequests", &pulls, map[string]any{"search": graphql.String(viewerPullRequestSearch)}); {
	case err == nil:
		for _, n := range pulls.Search.Nodes {
			if name := strings.ToLower(n.PullRequest.Repository.NameWithOwner); name != "" {
				counts[name]++
			}
		}
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	seen := map[string]bool{}
	out := make([]ViewerRepository, 0, min(len(nodes), MaxViewerRepositories))
	for _, n := range nodes {
		name := strings.ToLower(n.NameWithOwner)
		if n.IsArchived || name == "" || seen[name] || len(out) >= MaxViewerRepositories {
			continue
		}
		seen[name] = true
		r := ViewerRepository{Repository: name, Private: n.IsPrivate, Fork: n.IsFork, Description: n.Description, OpenPullRequests: counts[name]}
		if n.PushedAt != nil {
			r.PushedAt = *n.PushedAt
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PushedAt.After(out[j].PushedAt) })
	return out, nil
}

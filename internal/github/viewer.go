package github

import (
	"context"
	"strings"
	"time"
)

// MaxViewerRepositories caps the repositories ViewerRepositories reads.
const MaxViewerRepositories = 20

// ViewerRepository is one of the signed-in account's top repositories.
type ViewerRepository struct {
	// Repository is the lowercase owner/name.
	Repository  string
	Private     bool
	Fork        bool
	PushedAt    time.Time
	Description string
}

// ViewerRepositoryList pairs the top repositories with the full accessible total.
type ViewerRepositoryList struct {
	Repositories []ViewerRepository
	TotalCount   int
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
		TopRepositories struct {
			Nodes []viewerRepositoryNode
		} `graphql:"topRepositories(first: 20, orderBy: {field: PUSHED_AT, direction: DESC})"`
		Repositories struct {
			TotalCount int
		} `graphql:"repositories(first: 1, affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], isArchived: false)"`
	}
}

// ViewerRepositories reads one page of the account's top repositories, ordered
// by latest push. Archived repositories are left out without fetching more pages.
func (c *Client) ViewerRepositories(ctx context.Context, host string) (ViewerRepositoryList, error) {
	var q viewerRepositoriesQuery
	if err := c.query(ctx, host, "ViewerRepositories", &q, nil); err != nil {
		return ViewerRepositoryList{}, err
	}
	seen := map[string]bool{}
	out := make([]ViewerRepository, 0, min(len(q.Viewer.TopRepositories.Nodes), MaxViewerRepositories))
	for _, n := range q.Viewer.TopRepositories.Nodes {
		name := strings.ToLower(n.NameWithOwner)
		if n.IsArchived || name == "" || seen[name] || len(out) >= MaxViewerRepositories {
			continue
		}
		seen[name] = true
		r := ViewerRepository{Repository: name, Private: n.IsPrivate, Fork: n.IsFork, Description: n.Description}
		if n.PushedAt != nil {
			r.PushedAt = *n.PushedAt
		}
		out = append(out, r)
	}
	return ViewerRepositoryList{Repositories: out, TotalCount: q.Viewer.Repositories.TotalCount}, nil
}

package github

import (
	"context"
	"strings"

	graphql "github.com/cli/shurcooL-graphql"
)

type currentNameQuery struct {
	Repository *struct {
		NameWithOwner string
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// CurrentName is the lowercase owner/name repo has now. GitHub answers a
// renamed or transferred repository's earlier name with the current one, as it
// forwards git remotes that still use it. It fails with ErrNotFound when the
// token cannot see the repository.
func (c *Client) CurrentName(ctx context.Context, repo Repository) (string, error) {
	var q currentNameQuery
	if err := c.query(ctx, repo.Host, "CurrentName", &q, map[string]any{"owner": graphql.String(repo.Owner), "name": graphql.String(repo.Name)}); err != nil {
		return "", err
	}
	if q.Repository == nil || q.Repository.NameWithOwner == "" {
		return "", ErrNotFound
	}
	return strings.ToLower(q.Repository.NameWithOwner), nil
}

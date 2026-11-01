package github

import (
	"context"
	"strings"
	"time"

	graphql "github.com/cli/shurcooL-graphql"
)

// InitialViewerRepositories matches the number of rows in the picker.
const InitialViewerRepositories = 8
const AdditionalViewerRepositories = 100

// MaxSearchQualifiers bounds the owner and repository qualifiers one search
// sends. GitHub's GraphQL search accepts at least 200 in one query.
const MaxSearchQualifiers = 300

// ViewerRepository is a GitHub repository available to the signed-in account.
type ViewerRepository struct {
	Repository  string
	Private     bool
	Fork        bool
	PushedAt    time.Time
	Description string
}

// ViewerRepositoryList contains one bounded GitHub response.
type ViewerRepositoryList struct {
	Repositories []ViewerRepository
	TotalCount   int
	NextCursor   *string
	// Scope is what the viewer may search, read with the top repositories.
	Scope SearchScope
}

// SearchScope is what a repository search may return: every repository of
// Owners (the viewer's login and its organizations) and the listed
// Repositories (lowercase owner/name), the viewer's collaborations elsewhere.
// An owner of one of those repositories is not searched as a whole, since
// the viewer has no permission on its other repositories.
type SearchScope struct {
	Owners       []string
	Repositories []string
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
		Login         string
		Organizations struct {
			Nodes []struct{ Login string }
		} `graphql:"organizations(first: 100)"`
		Collaborations struct {
			Nodes []struct{ NameWithOwner string }
		} `graphql:"collaborations: repositories(first: 100, affiliations: [COLLABORATOR], isArchived: false, orderBy: {field: PUSHED_AT, direction: DESC})"`
		TopRepositories struct {
			Nodes    []viewerRepositoryNode
			PageInfo pageInfo
		} `graphql:"topRepositories(first: $first, after: $cursor, orderBy: {field: PUSHED_AT, direction: DESC})"`
		Repositories struct{ TotalCount int } `graphql:"repositories(first: 1, affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER], isArchived: false)"`
	}
}

// ViewerRepositories reads one topRepositories page, without following it.
func (c *Client) ViewerRepositories(ctx context.Context, host string, first int, cursor *string) (ViewerRepositoryList, error) {
	var q viewerRepositoriesQuery
	if err := c.query(ctx, host, "ViewerRepositories", &q, map[string]any{"first": graphql.Int(first), "cursor": (*graphql.String)(cursor)}); err != nil {
		return ViewerRepositoryList{}, err
	}
	out := ViewerRepositoryList{Repositories: viewerRepositoriesOf(q.Viewer.TopRepositories.Nodes), TotalCount: q.Viewer.Repositories.TotalCount}
	out.Scope.Owners = []string{q.Viewer.Login}
	for _, org := range q.Viewer.Organizations.Nodes {
		out.Scope.Owners = append(out.Scope.Owners, org.Login)
	}
	for _, r := range q.Viewer.Collaborations.Nodes {
		out.Scope.Repositories = append(out.Scope.Repositories, r.NameWithOwner)
	}
	out.Scope = out.Scope.normalized()
	info := q.Viewer.TopRepositories.PageInfo
	if info.HasNextPage {
		if info.EndCursor == nil || *info.EndCursor == "" || (cursor != nil && *cursor == *info.EndCursor) {
			return ViewerRepositoryList{}, errInvalidPagination
		}
		out.NextCursor = info.EndCursor
	}
	return out, nil
}

type searchRepositoriesQuery struct {
	Search struct {
		Nodes []struct {
			Repository viewerRepositoryNode `graphql:"... on Repository"`
		}
		RepositoryCount int
		PageInfo        pageInfo
	} `graphql:"search(query: $search, type: REPOSITORY, first: 100, after: $cursor)"`
}

// normalized lowercases, validates and deduplicates the scope, drops
// repositories whose owner is searched whole, and keeps at most
// MaxSearchQualifiers entries, owners first.
func (s SearchScope) normalized() SearchScope {
	out := SearchScope{Owners: []string{}, Repositories: []string{}}
	seen := map[string]bool{}
	for _, owner := range s.Owners {
		owner = strings.ToLower(owner)
		if ownerPattern.MatchString(owner) && !seen[owner] && len(out.Owners) < MaxSearchQualifiers {
			seen[owner] = true
			out.Owners = append(out.Owners, owner)
		}
	}
	for _, repository := range s.Repositories {
		repository = strings.ToLower(repository)
		owner, name, ok := strings.Cut(repository, "/")
		if ok && ownerPattern.MatchString(owner) && namePattern.MatchString(name) && !seen[owner] && !seen[repository] && len(out.Owners)+len(out.Repositories) < MaxSearchQualifiers {
			seen[repository] = true
			out.Repositories = append(out.Repositories, repository)
		}
	}
	return out
}

// only keeps the parts of the scope owned by one of owners.
func (s SearchScope) only(owners map[string]bool) SearchScope {
	out := SearchScope{}
	for _, owner := range s.Owners {
		if owners[owner] {
			out.Owners = append(out.Owners, owner)
		}
	}
	for _, repository := range s.Repositories {
		if owner, _, _ := strings.Cut(repository, "/"); owners[owner] {
			out.Repositories = append(out.Repositories, repository)
		}
	}
	return out
}

// searchQuery is the GitHub search for query within scope, or false when
// nothing in scope can match. Owners a query names (owner/name, user:, org:
// or repo:) narrow the scope rather than widen it; their names become terms.
func searchQuery(query string, scope SearchScope) (string, bool) {
	scope = scope.normalized()
	var terms []string
	var named map[string]bool
	name := func(owner string) {
		if named == nil {
			named = map[string]bool{}
		}
		named[strings.ToLower(owner)] = true
	}
	for _, token := range strings.Fields(query) {
		qualifier, value, qualified := strings.Cut(token, ":")
		switch qualifier = strings.ToLower(qualifier); {
		case qualified && (qualifier == "user" || qualifier == "org") && ownerPattern.MatchString(value):
			name(value)
			continue
		case qualified && qualifier == "repo":
			token = value
		}
		if owner, repository, ok := strings.Cut(token, "/"); ok && ownerPattern.MatchString(owner) && namePattern.MatchString(repository) {
			name(owner)
			token = repository
		}
		terms = append(terms, token)
	}
	if named != nil {
		scope = scope.only(named)
	}
	if len(scope.Owners)+len(scope.Repositories) == 0 {
		return "", false
	}
	for _, owner := range scope.Owners {
		terms = append(terms, "user:"+owner)
	}
	for _, repository := range scope.Repositories {
		terms = append(terms, "repo:"+repository)
	}
	return strings.Join(terms, " ") + " in:name archived:false fork:true", true
}

// SearchRepositories asks GitHub's repository search for names within scope,
// including forks and private repositories the token can see. GitHub combines
// the owner and repository qualifiers with OR. With nothing in scope it
// returns nothing, never all of GitHub. It never enumerates repositories.
// cursor continues an earlier page of the same search.
func (c *Client) SearchRepositories(ctx context.Context, host, query string, scope SearchScope, cursor *string) (ViewerRepositoryList, error) {
	search, ok := searchQuery(query, scope)
	if !ok {
		return ViewerRepositoryList{Repositories: []ViewerRepository{}}, nil
	}
	var q searchRepositoriesQuery
	if err := c.query(ctx, host, "SearchRepositories", &q, map[string]any{"search": graphql.String(search), "cursor": (*graphql.String)(cursor)}); err != nil {
		return ViewerRepositoryList{}, err
	}
	nodes := make([]viewerRepositoryNode, 0, len(q.Search.Nodes))
	for _, n := range q.Search.Nodes {
		nodes = append(nodes, n.Repository)
	}
	out := ViewerRepositoryList{Repositories: viewerRepositoriesOf(nodes), TotalCount: q.Search.RepositoryCount}
	if info := q.Search.PageInfo; info.HasNextPage {
		if info.EndCursor == nil || *info.EndCursor == "" || (cursor != nil && *cursor == *info.EndCursor) {
			return ViewerRepositoryList{}, errInvalidPagination
		}
		out.NextCursor = info.EndCursor
	}
	return out, nil
}

func viewerRepositoriesOf(nodes []viewerRepositoryNode) []ViewerRepository {
	out := make([]ViewerRepository, 0, len(nodes))
	seen := map[string]bool{}
	for _, n := range nodes {
		name := strings.ToLower(n.NameWithOwner)
		if name == "" || n.IsArchived || seen[name] {
			continue
		}
		seen[name] = true
		r := ViewerRepository{Repository: name, Private: n.IsPrivate, Fork: n.IsFork, Description: n.Description}
		if n.PushedAt != nil {
			r.PushedAt = *n.PushedAt
		}
		out = append(out, r)
	}
	return out
}

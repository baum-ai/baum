package discovery

import (
	"context"

	"github.com/baum-ai/baum/internal/github"
)

// GraphQLBudget is the credential's GraphQL points budget as discovery last
// read it, for another reader of the same token, such as the background
// refresh, to leave discovery its share. It makes no request. Remaining is
// nil while no poll or probe has read it, and a Reset that has passed means
// a new window GitHub has not reported yet.
func (s *Service) GraphQLBudget(ctx context.Context, repository github.Repository) (RateLimit, error) {
	reader, err := s.client.REST(ctx, repository)
	if err != nil {
		return RateLimit{}, err
	}
	path, err := s.path(reader.CacheIdentity(), "")
	if err != nil {
		return RateLimit{}, err
	}
	budget, err := s.ledger(path + ".budget")
	if err != nil {
		return RateLimit{}, err
	}
	_, graph := budget.budgets()
	return graph, nil
}

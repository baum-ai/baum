package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// fingerprint is what a probe compares with the last complete poll: the
// newest open pull request, how many are open and the latest update to any of
// them. Opening one raises Newest, closing or merging one lowers Open, and a
// push, edit, review or comment moves Updated.
type fingerprint struct {
	Newest  int       `json:",omitempty"`
	Open    int       `json:",omitempty"`
	Updated time.Time `json:",omitzero"`
}

// probeBatch bounds the repositories one probe query asks about.
const probeBatch = 50

// Probe asks GitHub whether each repository's open pull requests changed
// since its last complete poll, and returns the ones that did, keyed by
// Repository.String(). It sends one GraphQL query per probeBatch repositories,
// about one point each, on the polls' shared budget. A repository never
// polled, or one GitHub does not return (renamed, or access lost), is left to
// its scheduled poll, as is one whose credential differs from the first
// repository's.
func (s *Service) Probe(ctx context.Context, repositories []github.Repository) (map[string]bool, error) {
	changed := map[string]bool{}
	if len(repositories) == 0 {
		return changed, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.liveMu.Lock()
	epoch := s.epoch
	s.active = cancel
	s.liveMu.Unlock()
	budgetMu.Lock()
	defer budgetMu.Unlock()
	reader, err := s.client.REST(ctx, repositories[0])
	if err != nil {
		return nil, err
	}
	type target struct {
		repository github.Repository
		known      fingerprint
	}
	var targets []target
	for _, repository := range repositories {
		if other, err := s.client.REST(ctx, repository); err != nil || other.CacheIdentity() != reader.CacheIdentity() {
			continue
		}
		path, err := s.path(reader.CacheIdentity(), repository.String())
		if err != nil {
			return nil, err
		}
		cache, err := load(path)
		if err != nil {
			continue
		}
		targets = append(targets, target{repository, cache.Fingerprint})
	}
	if len(targets) == 0 {
		return changed, nil
	}
	budgetPath, err := s.path(reader.CacheIdentity(), "")
	if err != nil {
		return nil, err
	}
	budgetPath += ".budget"
	budget, err := load(budgetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	p := poll{reader: reader, rate: budget.Snapshot.Rate, graphRate: budget.GraphQLRate, now: s.now()}
	if p.ResetIfExhausted().After(p.now) {
		return nil, errRateLimit
	}
	var probeErr error
	for start := 0; start < len(targets); start += probeBatch {
		batch := targets[start:min(start+probeBatch, len(targets))]
		asked := make([]github.Repository, len(batch))
		for i, t := range batch {
			asked[i] = t.repository
		}
		query, variables := probeQuery(asked)
		data, _, err := p.graphQL(ctx, query, variables)
		if err != nil {
			probeErr = err
			break
		}
		var response map[string]json.RawMessage
		if json.Unmarshal(data, &response) != nil {
			probeErr = errors.New("invalid GitHub discovery response")
			break
		}
		for i, t := range batch {
			var found *struct {
				Newest struct {
					TotalCount int
					Nodes      []struct{ Number int }
				}
				Recent struct {
					Nodes []struct{ UpdatedAt time.Time }
				}
			}
			if json.Unmarshal(response[fmt.Sprintf("r%d", i)], &found) != nil || found == nil {
				continue
			}
			current := fingerprint{Open: found.Newest.TotalCount}
			if len(found.Newest.Nodes) > 0 {
				current.Newest = found.Newest.Nodes[0].Number
			}
			if len(found.Recent.Nodes) > 0 {
				current.Updated = found.Recent.Nodes[0].UpdatedAt
			}
			if current.Newest != t.known.Newest || current.Open != t.known.Open || !current.Updated.Equal(t.known.Updated) {
				changed[t.repository.String()] = true
			}
		}
	}
	if s.Epoch() != epoch || ctx.Err() != nil {
		return nil, github.ErrIdentityChanged
	}
	if err := save(budgetPath, diskCache{Snapshot: Snapshot{Rate: p.rate}, GraphQLRate: p.graphRate}); err != nil {
		return nil, fmt.Errorf("save discovery rate budget: %w", err)
	}
	if probeErr != nil {
		return nil, probeErr
	}
	return changed, nil
}

// probeQuery asks, under the aliases r0, r1, …, for each repository's newest
// open pull request, how many are open and the latest one updated.
func probeQuery(repositories []github.Repository) (string, map[string]any) {
	var parameters []string
	var fields strings.Builder
	variables := map[string]any{}
	for i, repository := range repositories {
		parameters = append(parameters, fmt.Sprintf("$o%d: String!, $n%d: String!", i, i))
		fmt.Fprintf(&fields, ` r%d: repository(owner: $o%d, name: $n%d) {
  newest: pullRequests(states: OPEN, orderBy: {field: CREATED_AT, direction: DESC}, first: 1) { totalCount nodes { number } }
  recent: pullRequests(states: OPEN, orderBy: {field: UPDATED_AT, direction: DESC}, first: 1) { nodes { updatedAt } }
 }
`, i, i, i)
		variables[fmt.Sprintf("o%d", i)] = repository.Owner
		variables[fmt.Sprintf("n%d", i)] = repository.Name
	}
	return "query Probe(" + strings.Join(parameters, ", ") + ") {\n rateLimit { cost remaining resetAt }\n" + fields.String() + "}", variables
}

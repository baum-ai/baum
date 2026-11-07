package discovery

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"time"
)

// teamsInterval is how long one account's team memberships are reused by
// every repository's poll before /user/teams is read again.
const teamsInterval = 10 * time.Minute

// ledger is one credential's GitHub budgets and team memberships. Every poll
// and probe of the credential's repositories shares it in memory, so a rate
// limit one of them meets stops the next request of the others, including
// polls that run at the same time. It is saved beside the repository caches,
// so a restart honours it too.
type ledger struct {
	mu    sync.Mutex
	rest  RateLimit
	graph RateLimit
	teams teamMembership
	// teamsMu lets one poll read /user/teams while the others wait for it.
	teamsMu sync.Mutex
}

// teamMembership is the account's teams, as /user/teams last listed them.
// Known is false when GitHub refused the list (a token without read:org).
type teamMembership struct {
	IDs   []int64   `json:",omitempty"`
	Known bool      `json:",omitempty"`
	Read  time.Time `json:",omitzero"`
}

// ledgers holds a ledger per application database and credential.
var ledgers sync.Map

// ledger returns the credential's shared ledger, merged with what is saved:
// another process, or an earlier one, may have met a limit since.
func (s *Service) ledger(key string) (*ledger, error) {
	database, err := s.databasePath()
	if err != nil {
		return nil, err
	}
	value, _ := ledgers.LoadOrStore(database+"\x00"+key, &ledger{})
	l := value.(*ledger)
	saved, err := s.load(key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	absorb(&l.rest, saved.Snapshot.Rate)
	absorb(&l.graph, saved.GraphQLRate)
	if saved.Teams.Read.After(l.teams.Read) {
		l.teams = saved.Teams
	}
	return l, nil
}

// absorb merges a saved budget into the live one, keeping the stricter.
func absorb(live *RateLimit, saved RateLimit) {
	observe(live, saved.Remaining, saved.Reset)
	live.RetryAt = maxTime(live.RetryAt, saved.RetryAt)
	if saved.Cost != nil && live.Cost == nil {
		live.Cost = saved.Cost
	}
}

// observe records a reading of the remaining budget. A later window replaces
// an earlier one; within one window the lower count stands, so a response
// sent before another exhausted the budget cannot restore it.
func observe(rate *RateLimit, remaining *int, reset time.Time) {
	switch {
	case remaining == nil:
	case rate.Remaining == nil, reset.After(rate.Reset), reset.IsZero() && rate.Reset.IsZero():
		rate.Remaining = remaining
	case reset.Equal(rate.Reset) && *remaining < *rate.Remaining:
		rate.Remaining = remaining
	}
	if reset.After(rate.Reset) {
		rate.Reset = reset
	}
}

func (l *ledger) budgets() (rest, graph RateLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rest, l.graph
}

func (l *ledger) update(change func(rest, graph *RateLimit)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	change(&l.rest, &l.graph)
}

// record is what is saved of the ledger.
func (l *ledger) record() diskCache {
	l.mu.Lock()
	defer l.mu.Unlock()
	return diskCache{Snapshot: Snapshot{Rate: l.rest}, GraphQLRate: l.graph, Teams: l.teams}
}

// membership returns the account's team IDs and whether they are known,
// reading /user/teams when the shared answer is older than teamsInterval.
// GitHub refusing the list is an answer and is kept as long; a failed
// request keeps the previous answer and is retried by the next poll.
func (p *poll) membership(ctx context.Context) (map[int64]bool, bool, error) {
	l := p.budget
	l.teamsMu.Lock()
	defer l.teamsMu.Unlock()
	l.mu.Lock()
	current := l.teams
	l.mu.Unlock()
	if current.Read.IsZero() || !p.now.Before(current.Read.Add(teamsInterval)) || p.now.Before(current.Read) {
		teams, err := collect[team](ctx, p, map[string]cachedResponse{}, "/user/teams")
		var status *statusError
		switch {
		case errors.Is(err, errRateLimit):
			return nil, false, err
		case err == nil:
			current = teamMembership{Known: true, Read: p.now}
			for _, t := range teams {
				current.IDs = append(current.IDs, t.ID)
			}
			slices.Sort(current.IDs)
		case errors.As(err, &status) && status.refused():
			current = teamMembership{Read: p.now}
		default:
			if current.Read.IsZero() {
				return map[int64]bool{}, false, nil
			}
		}
		if err == nil || status != nil && status.refused() {
			l.mu.Lock()
			l.teams = current
			l.mu.Unlock()
		}
	}
	ids := map[int64]bool{}
	for _, id := range current.IDs {
		ids[id] = true
	}
	return ids, current.Known, nil
}

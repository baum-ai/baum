package orchestrate

import (
	"sync"

	"github.com/baum-ai/baum/agent"
)

// budgets splits a run's call and token budgets between the four budget
// levels (the map, the sections with the context, the tiles, the overview)
// by Shares. Each call takes an equal part of what its level has left among
// the calls of the level still to start, and gives back what it did not
// spend; a level that closes passes what it has left to the next. A zero
// (unlimited) budget stays unlimited.
type budgets struct {
	mu      sync.Mutex
	run     agent.Budgets
	calls   [4]int
	tokens  [4]int
	waiting [4]int
}

func newBudgets(run agent.Budgets) *budgets {
	b := &budgets{run: run, waiting: [4]int{1, 0, 0, 1}}
	split := func(total int) [4]int {
		var out [4]int
		given := 0
		for i, share := range Shares {
			out[i] = int(float64(total) * share)
			given += out[i]
		}
		out[3] += total - given
		return out
	}
	b.calls, b.tokens = split(run.MaxCalls), split(run.MaxTokens)
	return b
}

// expect adds n calls (or removes -n) to those a level still has to start.
func (b *budgets) expect(level, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.waiting[level] = max(b.waiting[level]+n, 0)
}

// take is the budgets of one call of a level: the run's, with its part of
// the level's calls and tokens. A bounded budget with nothing left is
// negative, which allows no call.
func (b *budgets) take(level int) agent.Budgets {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.run
	n := max(b.waiting[level], 1)
	part := func(total int, left *int) int {
		if total == 0 {
			return 0
		}
		share := *left / n
		if share < 1 {
			share = min(*left, 1)
		}
		*left -= share
		if share < 1 {
			return -1
		}
		return share
	}
	out.MaxCalls = part(b.run.MaxCalls, &b.calls[level])
	out.MaxTokens = part(b.run.MaxTokens, &b.tokens[level])
	b.waiting[level] = max(b.waiting[level]-1, 0)
	return out
}

// give returns what a call did not spend of what it took.
func (b *budgets) give(level int, took agent.Budgets, spent agent.Outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if took.MaxCalls > 0 {
		b.calls[level] += max(took.MaxCalls-spent.Calls, 0)
	}
	if took.MaxTokens > 0 {
		b.tokens[level] += max(took.MaxTokens-spent.Tokens, 0)
	}
}

// close passes what a level has left to the next.
func (b *budgets) close(level int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if level >= 3 {
		return
	}
	b.calls[level+1] += b.calls[level]
	b.tokens[level+1] += b.tokens[level]
	b.calls[level], b.tokens[level] = 0, 0
}

// left is what a level has left, for tests.
func (b *budgets) left(level int) (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[level], b.tokens[level]
}

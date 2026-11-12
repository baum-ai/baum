package uiserver

import (
	"context"
	"fmt"
	"sync"

	"github.com/baum-ai/baum/internal/repos"
)

// The changes the Settings routes make, shared by each route's handler and
// the save of an agent's proposal of the same change (proposers.go), so the
// two save alike: the same check of the input, the same change of the saved
// preferences and what follows a save.

// updatePreferences saves edit's change of the preferences when check (if
// set) accepts them as the writing transaction reads them; with no check it
// is Registry.UpdatePreferences. An error of check writes nothing.
func (a LocalAPI) updatePreferences(ctx context.Context, check func(repos.Preferences) error, edit func(repos.Preferences) repos.Preferences) (repos.Preferences, error) {
	if check == nil {
		return a.Registry.UpdatePreferences(ctx, edit)
	}
	return a.Registry.UpdatePreferencesIf(ctx, func(p repos.Preferences) (repos.Preferences, error) {
		if err := check(p); err != nil {
			return p, err
		}
		return edit(p), nil
	})
}

// checkTimeLimits refuses what PUT /api/settings/time-limits refuses (400).
func checkTimeLimits(review, run int) error {
	if review < 1 || run < 1 || repos.ValidateTimeLimit(review) != nil || repos.ValidateTimeLimit(run) != nil {
		return fmt.Errorf("time limits must be between 1 and %d minutes", repos.MaxTimeLimit)
	}
	return nil
}

// setTimeLimits is PUT /api/settings/time-limits' change.
func setTimeLimits(p repos.Preferences, review, run int) repos.Preferences {
	p.ReviewTimeLimit, p.RunTimeLimit = review, run
	return p
}

// setAgentEnv is PUT /api/settings/agent-env's change, of an environment
// config.ValidateAgentEnv accepted: the app's environment replaces the
// config file's, and an empty one goes back to the file's.
func setAgentEnv(p repos.Preferences, env map[string]string) repos.Preferences {
	if len(env) == 0 {
		env = nil
	}
	p.AgentEnv = env
	return p
}

// checkJobConcurrency refuses what PUT /api/settings/job-concurrency
// refuses (400).
func checkJobConcurrency(n int) error {
	if n < 1 || n > repos.MaxJobConcurrency {
		return fmt.Errorf("job_concurrency must be between 1 and %d", repos.MaxJobConcurrency)
	}
	return nil
}

// concurrencyMu makes each change of the job concurrency, its save and its
// application to the running queue, one step, so the queue ends with the
// saved value.
var concurrencyMu sync.Mutex

// saveJobConcurrency is PUT /api/settings/job-concurrency's save of a checked
// n, when check (if set) accepts the saved preferences, and applies the
// saved value to the running queue.
func (a LocalAPI) saveJobConcurrency(ctx context.Context, n int, check func(repos.Preferences) error) (repos.Preferences, error) {
	concurrencyMu.Lock()
	defer concurrencyMu.Unlock()
	prefs, err := a.updatePreferences(ctx, check, func(p repos.Preferences) repos.Preferences {
		p.JobConcurrency = n
		return p
	})
	if err != nil {
		return repos.Preferences{}, err
	}
	if a.ConcurrencyChanged != nil {
		a.ConcurrencyChanged(prefs.Concurrency())
	}
	return prefs, nil
}

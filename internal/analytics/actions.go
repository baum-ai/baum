package analytics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/prupdate"
)

// The GitHub writes the app makes besides a fix, as the ledger records them.
const (
	ActionMerge        = "merge"
	ActionUpdateBranch = "update_branch"
	ActionRerunChecks  = "rerun_checks"
)

// Action is one GitHub write the person made from the app.
type Action struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Repository string    `json:"repository"`
	PR         int       `json:"pr"`
}

// Actions is the small ledger of merges, branch updates and check re-runs
// made from the app, which the jobs list does not hold. It keeps Retention of
// them in one file in the daemon's state directory.
type Actions struct {
	Path string
	Now  func() time.Time

	mu      sync.Mutex
	actions []Action
	loaded  bool
}

func (a *Actions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Record notes a write that succeeded. A failure to save is not the write's
// failure, so it is only returned for the caller to log.
func (a *Actions) Record(kind, repository string, pr int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	now := a.now().UTC()
	cutoff := now.Add(-Retention)
	a.actions = slices.DeleteFunc(a.actions, func(x Action) bool { return x.At.Before(cutoff) })
	a.actions = append(a.actions, Action{At: now, Kind: kind, Repository: strings.ToLower(repository), PR: pr})
	raw, err := json.Marshal(a.actions)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.Path), 0o700); err != nil {
		return err
	}
	return generate.WriteFileAtomic(a.Path, raw)
}

// Since lists the writes recorded at or after t.
func (a *Actions) Since(t time.Time) []Action {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	var out []Action
	for _, x := range a.actions {
		if !x.At.Before(t) {
			out = append(out, x)
		}
	}
	return out
}

func (a *Actions) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	if raw, err := os.ReadFile(a.Path); err == nil {
		_ = json.Unmarshal(raw, &a.actions)
	}
}

// Merger is the merge write uiserver.MergeHandler makes.
type Merger interface {
	MergeOptions(ctx context.Context, repository github.Repository, number int) (github.MergeOptions, error)
	MergePullRequest(ctx context.Context, repository github.Repository, number int, method, expectedHead string) error
}

// Updater is the branch update, check re-run and thread resolution writes
// uiserver.UpdateHandler makes. Resolving a thread is not recorded.
type Updater interface {
	UpdateOptions(ctx context.Context, repository github.Repository, number int) (prupdate.UpdateOptions, error)
	UpdateBranch(ctx context.Context, repository github.Repository, number int, expectedHead string) error
	RerunOptions(ctx context.Context, repository github.Repository, number int) (prupdate.RerunOptions, error)
	RerunFailedChecks(ctx context.Context, repository github.Repository, number int, expectedHead string) (prupdate.RerunResult, error)
	ReviewThreads(ctx context.Context, repository github.Repository, number, unresolved int) (prupdate.Threads, error)
	ResolveThread(ctx context.Context, repository github.Repository, number int, id string) error
}

// RecordMerges records each merge m makes.
func (a *Actions) RecordMerges(m Merger) Merger { return recordingMerger{m, a} }

// RecordUpdates records each branch update and check re-run u makes.
func (a *Actions) RecordUpdates(u Updater) Updater { return recordingUpdater{u, a} }

type recordingMerger struct {
	Merger
	actions *Actions
}

func (r recordingMerger) MergePullRequest(ctx context.Context, repository github.Repository, number int, method, expectedHead string) error {
	err := r.Merger.MergePullRequest(ctx, repository, number, method, expectedHead)
	if err == nil {
		_ = r.actions.Record(ActionMerge, repository.String(), number)
	}
	return err
}

type recordingUpdater struct {
	Updater
	actions *Actions
}

func (r recordingUpdater) UpdateBranch(ctx context.Context, repository github.Repository, number int, expectedHead string) error {
	err := r.Updater.UpdateBranch(ctx, repository, number, expectedHead)
	if err == nil {
		_ = r.actions.Record(ActionUpdateBranch, repository.String(), number)
	}
	return err
}

func (r recordingUpdater) RerunFailedChecks(ctx context.Context, repository github.Repository, number int, expectedHead string) (prupdate.RerunResult, error) {
	result, err := r.Updater.RerunFailedChecks(ctx, repository, number, expectedHead)
	if err == nil {
		_ = r.actions.Record(ActionRerunChecks, repository.String(), number)
	}
	return result, err
}

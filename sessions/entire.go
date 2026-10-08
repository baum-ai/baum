package sessions

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/sessions/collector"
	"github.com/lastpersonlabs/baum/sessions/collector/entire"
	"github.com/lastpersonlabs/baum/sessions/entirecheckpoint"
	"github.com/lastpersonlabs/baum/sessions/entiresession"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// maxParallel bounds how many sessions are mapped, or checkpoints read, at
// once. Each holds a whole transcript in memory.
func maxParallel() int {
	return min(runtime.GOMAXPROCS(0), 8)
}

type mapped struct {
	graph *session.Graph
	notes []string
	err   error
}

// mapAll maps each found session, several at once. The outcome of each is at
// its index; only a cancelled context is an error.
func mapAll(ctx context.Context, found []collector.Session) ([]mapped, error) {
	out := make([]mapped, len(found))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallel())
	for i, s := range found {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			out[i].graph, out[i].notes, out[i].err = Map(s)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return out, ctx.Err()
}

// checkpointStore is the clone's checkpoint branch, read in a private
// repository with no remote, so a partial clone's missing objects are
// reported, never downloaded.
type checkpointStore struct {
	repo    *gitlocal.Repo
	objects string
	commit  string
}

// openCheckpoints opens the checkpoint branch of the clone at root; ok is
// false when it has none. The caller closes a store it got.
func openCheckpoints(ctx context.Context, root string) (store *checkpointStore, ok bool, err error) {
	commit, ok, err := entire.Resolve(ctx, root)
	if err != nil || !ok {
		return nil, false, err
	}
	repo, err := gitlocal.Open(ctx, root, gitlocal.Options{})
	if err != nil {
		return nil, false, fmt.Errorf("opening the clone to read Entire checkpoints: %w", err)
	}
	return &checkpointStore{repo: repo, objects: repo.PrivateDir(), commit: commit}, true, nil
}

func (s *checkpointStore) Close() { _ = s.repo.Close() }

// readOutcome is one checkpoint read whole and mapped.
type readOutcome struct {
	results []entiresession.Result
	err     error
}

// read reads one checkpoint whole and maps every session it records.
func (s *checkpointStore) read(ctx context.Context, id string) readOutcome {
	fsys, err := entire.Read(ctx, s.objects, s.commit, id)
	if err != nil {
		return readOutcome{err: err}
	}
	parsed, err := entirecheckpoint.ParseCheckpoint(fsys)
	if err != nil {
		return readOutcome{err: err}
	}
	results, _, err := entiresession.Build(parsed)
	return readOutcome{results: results, err: err}
}

// readAll reads the checkpoints ids names, several at once.
func (s *checkpointStore) readAll(ctx context.Context, ids []string) (map[string]readOutcome, error) {
	outcomes := make([]readOutcome, len(ids))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallel())
	for i, id := range ids {
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			outcomes[i] = s.read(groupCtx, id)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byID := make(map[string]readOutcome, len(ids))
	for i, id := range ids {
		byID[id] = outcomes[i]
	}
	return byID, nil
}

// pick is one checkpoint holding a snapshot of a session.
type pick struct {
	checkpoint string
	at         time.Time
}

// loadEntire returns the sessions the clone's checkpoint branch records, each
// once. A checkpoint usually holds the whole transcript of every session it
// records as it stood when the checkpoint was written, so a long session
// appears in many checkpoints, and reading them is the slow part. Only the
// newest snapshot of each session is mapped: the metadata of every checkpoint
// written since opts.Since is read first, which says which session each holds
// and when, and only the checkpoints holding a newest snapshot are read whole.
// A session discovery already found in the agent's own store is skipped: the
// native transcript is the fuller record. A newest snapshot that cannot be
// mapped, or that carries no transcript (Entire sometimes writes only the
// metadata and the prompt), gives way to the next newest; a checkpoint whose
// metadata cannot be read is read whole.
func loadEntire(ctx context.Context, root string, opts Options, native map[string]bool) ([]Session, string, []Failure, error) {
	store, ok, err := openCheckpoints(ctx, root)
	if err != nil || !ok {
		return nil, "", nil, err
	}
	defer store.Close()
	commit := store.commit
	checkpoints, err := entire.List(ctx, store.objects, commit)
	if err != nil {
		return nil, commit, nil, err
	}
	// A store holds checkpoints for every session ever recorded, so with a
	// Since the ones last written before it are skipped unread. If they
	// cannot be dated, all are considered.
	var written map[string]time.Time
	if !opts.Since.IsZero() {
		if written, err = entire.Touched(ctx, store.objects, commit); err != nil {
			written = nil
		}
	}
	var ids []string
	for _, checkpoint := range checkpoints {
		if at, dated := written[checkpoint.ID]; dated && at.Before(opts.Since) {
			continue
		}
		ids = append(ids, checkpoint.ID)
	}
	known, err := entire.Sessions(ctx, store.objects, commit, ids)
	if err != nil {
		if ctx.Err() != nil {
			return nil, commit, nil, ctx.Err()
		}
		known = nil // read every checkpoint whole
	}

	picks := map[string][]pick{}
	var whole []string
	for _, id := range ids {
		snapshots, ok := known[id]
		if !ok {
			whole = append(whole, id)
			continue
		}
		for _, snapshot := range snapshots {
			if native[snapshot.SessionID] || (!opts.Since.IsZero() && snapshot.CreatedAt.Before(opts.Since)) {
				continue
			}
			picks[snapshot.SessionID] = append(picks[snapshot.SessionID], pick{checkpoint: id, at: snapshot.CreatedAt})
		}
	}
	sessionIDs := make([]string, 0, len(picks))
	first := slices.Clone(whole)
	for id, list := range picks {
		slices.SortFunc(list, func(a, b pick) int {
			if c := b.at.Compare(a.at); c != 0 {
				return c
			}
			return strings.Compare(b.checkpoint, a.checkpoint)
		})
		sessionIDs = append(sessionIDs, id)
		first = append(first, list[0].checkpoint)
	}
	slices.Sort(sessionIDs)
	slices.Sort(first)
	first = slices.Compact(first)
	outcomes, err := store.readAll(ctx, first)
	if err != nil {
		return nil, commit, nil, err
	}

	var failures []Failure
	reported := map[string]bool{}
	fail := func(path string, err error) {
		if !reported[path] {
			reported[path] = true
			failures = append(failures, Failure{Source: Entire, Path: path, Err: err})
		}
	}
	best := map[string]Session{}
	offer := func(checkpoint string, result entiresession.Result) {
		next := Session{
			Source: Entire, Vendor: result.Vendor, ID: result.SessionID, Path: checkpoint,
			RepoRoot: root, ModifiedAt: result.CreatedAt, Graph: result.Graph, Notes: result.Warnings,
		}
		if current, ok := best[result.SessionID]; ok && !Fuller(next, current) {
			return
		}
		best[result.SessionID] = next
	}
	for _, id := range whole {
		outcome := outcomes[id]
		if outcome.err != nil {
			fail(id, outcome.err)
			continue
		}
		for _, result := range outcome.results {
			switch {
			case result.Err != nil:
				fail(id+"/"+result.SessionID, result.Err)
			case result.Graph == nil || native[result.SessionID]:
			case !opts.Since.IsZero() && result.CreatedAt.Before(opts.Since):
			default:
				offer(id, result)
			}
		}
	}
	// The newest snapshot with a transcript wins. One without (Entire wrote
	// only metadata and a prompt) is kept only while nothing older has one.
	for _, sessionID := range sessionIDs {
		for _, p := range picks[sessionID] {
			outcome, read := outcomes[p.checkpoint]
			if !read {
				if err := ctx.Err(); err != nil {
					return nil, commit, failures, err
				}
				outcome = store.read(ctx, p.checkpoint)
				outcomes[p.checkpoint] = outcome
			}
			if outcome.err != nil {
				fail(p.checkpoint, outcome.err)
				continue
			}
			i := slices.IndexFunc(outcome.results, func(r entiresession.Result) bool { return r.SessionID == sessionID })
			if i < 0 {
				continue
			}
			if result := outcome.results[i]; result.Err != nil {
				fail(p.checkpoint+"/"+sessionID, result.Err)
				continue
			} else if result.Graph != nil {
				offer(p.checkpoint, result)
				if HasTranscript(result.Graph) {
					break
				}
			}
		}
	}

	sessions := make([]Session, 0, len(best))
	for _, s := range best {
		sessions = append(sessions, s)
	}
	slices.SortFunc(sessions, func(a, b Session) int { return strings.Compare(a.ID, b.ID) })
	return sessions, commit, failures, nil
}

// CheckpointSessions returns, for each of ids that is a checkpoint on the
// checkpoint branch of the clone containing dir, the ids of the sessions it
// records, each once, in the order it lists them. It reads checkpoint metadata
// only, from the local branch or a remote-tracking copy of it, and never
// fetches. A checkpoint the branch does not hold, or whose metadata cannot be
// read, is absent from the result; so is everything when the clone has no
// checkpoint branch.
func CheckpointSessions(ctx context.Context, dir string, ids []string) (map[string][]string, error) {
	root, err := collector.RepoRoot(dir)
	if err != nil {
		return nil, err
	}
	store, ok, err := openCheckpoints(ctx, root)
	if err != nil || !ok {
		return map[string][]string{}, err
	}
	defer store.Close()
	known, err := entire.Sessions(ctx, store.objects, store.commit, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(known))
	for id, snapshots := range known {
		var sessionIDs []string
		for _, snapshot := range snapshots {
			if !slices.Contains(sessionIDs, snapshot.SessionID) {
				sessionIDs = append(sessionIDs, snapshot.SessionID)
			}
		}
		if len(sessionIDs) > 0 {
			out[id] = sessionIDs
		}
	}
	return out, nil
}

// LoadCheckpoint maps the sessions one checkpoint on the clone's checkpoint
// branch records, as Load maps an Entire session, with no Since applied. A
// session that cannot be mapped is a Failure; an error is a checkpoint that
// could not be read at all.
func LoadCheckpoint(ctx context.Context, dir, id string) ([]Session, []Failure, error) {
	root, err := collector.RepoRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	store, ok, err := openCheckpoints(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("the clone has no Entire checkpoint branch")
	}
	defer store.Close()
	outcome := store.read(ctx, strings.ToLower(id))
	if outcome.err != nil {
		return nil, nil, outcome.err
	}
	var sessions []Session
	var failures []Failure
	for _, result := range outcome.results {
		switch {
		case result.Err != nil:
			failures = append(failures, Failure{Source: Entire, Path: id + "/" + result.SessionID, Err: result.Err})
		case result.Graph != nil:
			sessions = append(sessions, Session{
				Source: Entire, Vendor: result.Vendor, ID: result.SessionID, Path: id,
				RepoRoot: root, ModifiedAt: result.CreatedAt, Graph: result.Graph, Notes: result.Warnings,
			})
		}
	}
	return sessions, failures, nil
}

package entire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// Listing answers List, Touched, Sessions and Located for one clone and
// remembers what it learned, so a later call over stores that did not move
// runs no git at all. Everything it keeps is named by an object id, which
// never changes meaning:
//
//   - the branch's checkpoints by the branch commit;
//   - the branch's dates by the branch commit and the clone's shallow
//     boundary, which ends its history. When the branch moved forward, only
//     the new commits (<old>..<new>) are read and merged into the old dates;
//   - each ref's tree and date by the ref's commit;
//   - each checkpoint's metadata by its tree.
//
// A move is seen because the Store passed in, which Resolve reads from the
// refs on every call, names other commits. What the old commits alone
// named (a ref that went away, the branch's old listing) is dropped. Only
// answers git gave in full are kept: an object that is missing, as in a
// partial clone, is asked for again on the next call, and is never fetched.
//
// repoRoot is a private repository that borrows the clone's objects, as for
// List. A Listing must only be used for one clone. It is safe for concurrent
// use; calls run one at a time.
type Listing struct {
	mu sync.Mutex

	// branch is the commit branchList and branchPaths describe.
	branch     string
	branchList []Checkpoint
	// branchPaths says, for each checkpoint in branchList, whether its
	// directory is spelled exactly Path(id): only then can Located tell from
	// the listing alone that the branch holds it.
	branchPaths map[string]bool

	// dated is the commit, and shallow the shallow boundary, branchDates
	// was read for.
	dated, shallow string
	branchDates    map[string]time.Time

	refTrees map[string]string     // ref commit → its root tree
	refDates map[string]objectDate // ref commit → its commit time
	metadata map[string]recorded   // checkpoint tree → the sessions it records
}

// recorded is what a checkpoint's metadata says it records: its snapshots,
// or, when found is false, that the metadata names none Sessions can use.
type recorded struct {
	snapshots []Snapshot
	found     bool
}

// objectDate is what dating one ref's object found: its commit time, or that it
// is not a commit and cannot be dated.
type objectDate struct {
	at     time.Time
	commit bool
}

// List is List, from what the Listing already knows where it can.
func (l *Listing) List(ctx context.Context, repoRoot string, store Store) ([]Checkpoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list(ctx, repoRoot, store)
}

func (l *Listing) list(ctx context.Context, repoRoot string, store Store) ([]Checkpoint, error) {
	var branch []Checkpoint
	if store.Branch != "" {
		if store.Branch == l.branch {
			workstats.Inc("entire.listing.branch_reused")
		} else {
			listed, paths, err := listBranch(ctx, repoRoot, store.Branch)
			if err != nil {
				return nil, err
			}
			l.branch, l.branchList, l.branchPaths = store.Branch, listed, paths
		}
		branch = l.branchList
	}
	var trees map[string]string
	if len(store.Refs) > 0 {
		var err error
		if trees, err = l.refTreesOf(ctx, repoRoot, store.Refs); err != nil {
			return nil, err
		}
	}
	keepOnly(l.refTrees, store.Refs)
	checkpoints := merged(branch, store.Refs, trees)
	l.keepMetadataOf(checkpoints)
	return checkpoints, nil
}

// refTreesOf is refTrees, asking git only about commits it has not seen.
func (l *Listing) refTreesOf(ctx context.Context, repoRoot string, refs map[string]string) (map[string]string, error) {
	trees := make(map[string]string, len(refs))
	unknown := map[string]string{}
	for id, commit := range refs {
		if tree, ok := l.refTrees[commit]; ok {
			trees[id] = tree
		} else {
			unknown[id] = commit
		}
	}
	if len(unknown) > 0 {
		found, err := refTrees(ctx, repoRoot, unknown)
		if err != nil {
			return nil, err
		}
		if l.refTrees == nil {
			l.refTrees = map[string]string{}
		}
		for id, tree := range found {
			trees[id] = tree
			l.refTrees[unknown[id]] = tree
		}
	}
	return trees, nil
}

// keepOnly drops the entries of byCommit for commits no ref names now.
func keepOnly[V any](byCommit map[string]V, refs map[string]string) {
	if len(byCommit) == 0 {
		return
	}
	named := make(map[string]bool, len(refs))
	for _, commit := range refs {
		named[commit] = true
	}
	maps.DeleteFunc(byCommit, func(commit string, _ V) bool { return !named[commit] })
}

// keepMetadataOf drops the metadata of checkpoints no longer listed.
func (l *Listing) keepMetadataOf(checkpoints []Checkpoint) {
	if len(l.metadata) == 0 {
		return
	}
	listed := make(map[string]bool, len(checkpoints))
	for _, checkpoint := range checkpoints {
		listed[checkpoint.TreeOID] = true
	}
	maps.DeleteFunc(l.metadata, func(tree string, _ recorded) bool { return !listed[tree] })
}

// Touched is Touched, from what the Listing already knows where it can.
func (l *Listing) Touched(ctx context.Context, repoRoot string, store Store) (map[string]time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	touched := map[string]time.Time{}
	if store.Branch != "" {
		dates, err := l.branchTouched(ctx, repoRoot, store.Branch)
		if err != nil {
			return nil, err
		}
		maps.Copy(touched, dates)
	}
	var ids []string
	var unknown []string
	for id, commit := range store.Refs {
		if _, dated := touched[id]; dated || !commitPattern.MatchString(commit) {
			continue
		}
		ids = append(ids, id)
		if _, ok := l.refDates[commit]; !ok {
			unknown = append(unknown, commit)
		}
	}
	if len(unknown) > 0 {
		found, err := dateObjects(ctx, repoRoot, unknown)
		if err != nil {
			return nil, err
		}
		if l.refDates == nil {
			l.refDates = map[string]objectDate{}
		}
		maps.Copy(l.refDates, found)
	}
	for _, id := range ids {
		if date := l.refDates[store.Refs[id]]; date.commit {
			touched[id] = date.at
		}
	}
	keepOnly(l.refDates, store.Refs)
	return touched, nil
}

// branchTouched dates the branch's checkpoints at commit: from memory when
// neither the commit nor the shallow boundary moved, from the new commits
// alone when the branch only moved forward, else from its whole history.
func (l *Listing) branchTouched(ctx context.Context, repoRoot, commit string) (map[string]time.Time, error) {
	shallow, known := shallowBoundary(repoRoot)
	if known && l.dated != "" && l.shallow == shallow {
		if l.dated == commit {
			workstats.Inc("entire.listing.dates_reused")
			return l.branchDates, nil
		}
		if dates, ok, err := l.datesSince(ctx, repoRoot, commit); err != nil {
			return nil, err
		} else if ok {
			workstats.Inc("entire.listing.dates_extended")
			l.dated, l.branchDates = commit, dates
			return dates, nil
		}
	}
	dates := map[string]time.Time{}
	if err := touchedBranch(ctx, repoRoot, commit, dates); err != nil {
		return nil, err
	}
	l.dated, l.shallow, l.branchDates = "", "", nil
	if known {
		l.dated, l.shallow, l.branchDates = commit, shallow, dates
	}
	return dates, nil
}

// datesSince dates the branch at commit from the dates already read for
// l.dated and the commits since it, when commit descends from it. ok is false
// when it does not (the branch was rewritten) or that cannot be told, and
// the whole history must be read.
func (l *Listing) datesSince(ctx context.Context, repoRoot, commit string) (map[string]time.Time, bool, error) {
	if !commitPattern.MatchString(commit) || !commitPattern.MatchString(l.dated) {
		return nil, false, nil
	}
	descends, err := isAncestor(ctx, repoRoot, l.dated, commit)
	if err != nil || !descends {
		return nil, false, ctx.Err()
	}
	newer := map[string]time.Time{}
	if err := touchedBranch(ctx, repoRoot, l.dated+".."+commit, newer); err != nil {
		return nil, false, ctx.Err()
	}
	dates := maps.Clone(l.branchDates)
	for id, at := range newer {
		// A newer commit usually has the later time; where clocks disagree,
		// the later time wins, as it does in a whole-history log.
		if before, ok := dates[id]; !ok || at.After(before) {
			dates[id] = at
		}
	}
	return dates, true, nil
}

// Sessions is Sessions, reading only the metadata of checkpoints whose
// trees it has not read before. An id the listing does not hold is left out
// without asking git: Sessions could not find it either.
func (l *Listing) Sessions(ctx context.Context, repoRoot string, store Store, ids []string) (map[string][]Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	checkpoints, err := l.list(ctx, repoRoot, store)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return Sessions(ctx, repoRoot, store, ids)
	}
	trees := make(map[string]string, len(checkpoints))
	for _, checkpoint := range checkpoints {
		trees[checkpoint.ID] = checkpoint.TreeOID
	}
	result := map[string][]Snapshot{}
	var ask []string
	seen := map[string]bool{}
	for _, id := range ids {
		canonical, ok := entirecheckpoint.CanonicalID(id)
		if !ok || seen[canonical] {
			continue
		}
		seen[canonical] = true
		tree, listed := trees[canonical]
		if !listed {
			continue
		}
		if known, ok := l.metadata[tree]; ok && tree != "" {
			if known.found {
				result[canonical] = slices.Clone(known.snapshots)
			}
			continue
		}
		ask = append(ask, canonical)
	}
	if len(ask) == 0 {
		return result, nil
	}
	found, settled, err := sessionsOf(ctx, repoRoot, store, ask)
	if err != nil {
		return nil, err
	}
	if l.metadata == nil {
		l.metadata = map[string]recorded{}
	}
	for _, id := range ask {
		snapshots, ok := found[id]
		if ok {
			result[id] = snapshots
		}
		if tree := trees[id]; tree != "" && settled[id] {
			l.metadata[tree] = recorded{snapshots: slices.Clone(snapshots), found: ok}
		}
	}
	return result, nil
}

// Located is Located, from the listing where it can tell: a ref is held,
// and the branch holds exactly the checkpoint directories it lists.
func (l *Listing) Located(ctx context.Context, repoRoot string, store Store, ids []string) (map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.list(ctx, repoRoot, store); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return Located(ctx, repoRoot, store, ids)
	}
	held := map[string]bool{}
	var unsure []string
	for _, id := range ids {
		canonical, ok := entirecheckpoint.CanonicalID(id)
		if !ok || held[canonical] {
			continue
		}
		loc, ok := store.locate(canonical)
		switch exact, listed := l.branchPaths[canonical]; {
		case !ok:
		case loc.dir == "" || (listed && exact):
			held[canonical] = true
		case listed:
			// Listed under another spelling of its directory, which a lookup
			// by Path(id) may or may not find: ask git.
			unsure = append(unsure, canonical)
		}
	}
	if len(unsure) > 0 {
		found, err := Located(ctx, repoRoot, store, unsure)
		if err != nil {
			return nil, err
		}
		maps.Copy(held, found)
	}
	return held, nil
}

// shallowBoundary fingerprints the private repository's shallow file, which
// gitlocal copies from the clone: the commits its history ends at. known is
// false when it cannot be read, and nothing read before may then be reused.
func shallowBoundary(repoRoot string) (fingerprint string, known bool) {
	content, err := os.ReadFile(filepath.Join(repoRoot, "shallow"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), true
}

// isAncestor reports whether ancestor is commit or one of its ancestors.
func isAncestor(ctx context.Context, repoRoot, ancestor, commit string) (bool, error) {
	workstats.Git("merge-base")
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "merge-base", "--is-ancestor", ancestor, commit)
	cmd.Env = gitlocal.ReadEnv(repoRoot)
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, err
	}
}

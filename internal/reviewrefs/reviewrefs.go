// Package reviewrefs keeps the commits saved review versions record (head,
// base and merge base) in the clone for as long as the versions exist, so a
// git gc in the clone cannot prune what a version's lazy patches are read
// from. Each version gets refs under gitlocal.RetainPrefix, written right
// after the version is stored (Keep) and reconciled with the clone's stores
// by the daemon (Reconcile): refs the clone's versions lack are created,
// and refs of versions no store of the clone holds are deleted. Both run
// outside the store's transaction, so a failure never undoes a version; a
// commit the clone does not hold is skipped, never fetched. A symbolic ref
// at a retained name was not written by baum and is left alone.
package reviewrefs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
)

// Limits bound one Reconcile pass.
type Limits struct {
	// Check is how many commits are looked up at once for refs to write; a
	// pass looks up at most checkBatches times as many.
	Check int
	// Write bounds the refs created or moved; Delete the refs deleted.
	Write, Delete int
}

// checkBatches bounds the lookups of one pass, in batches of Limits.Check.
const checkBatches = 4

// DefaultLimits are the daemon's: a pass looks up commits 3000 at a time, at
// most 12000, and writes or deletes at most 600 refs (200 versions) each.
var DefaultLimits = Limits{Check: 3000, Write: 600, Delete: 600}

// Result is what one Reconcile pass did.
type Result struct {
	Written, Deleted int
	// Missing counts refs not written because the clone lacks the commit.
	Missing int
	// Symbolic counts symbolic refs at retained names, left alone.
	Symbolic int
	// Failed are the refs that could not be written or deleted even alone.
	Failed []string
	// More is set when a bound left refs to write or delete for a next pass.
	More bool
}

// refs are the retained refs a version should have: one per role whose
// commit the metadata records.
func refs(key store.Key, version int, head, base, mergeBase string) map[string]string {
	out := map[string]string{}
	for role, oid := range map[string]string{"head": head, "base": base, "merge-base": mergeBase} {
		if oid != "" {
			out[gitlocal.RetainedName{Repository: key.Repository, Number: key.Number, Version: version, Role: role}.Ref()] = oid
		}
	}
	return out
}

// ownStore reports whether root is one of the clone's own store roots, the
// ones Reconcile reads. A version in an explicit store elsewhere gets no refs:
// nothing would keep them for as long as it exists.
func ownStore(commonDir, root string) (bool, error) {
	roots, err := store.Roots(commonDir)
	if err != nil {
		return false, err
	}
	canonical := func(path string) string {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		return filepath.Clean(path)
	}
	root = canonical(root)
	return slices.ContainsFunc(roots, func(r string) bool { return canonical(r) == root }), nil
}

// Keep writes the refs of the version m just stored in versions under key.
// A ref that already names its commit is left; a commit the clone lacks is
// skipped. Nothing is written for a store that is not the clone's own. A
// write that fails because another writer got there first is no error; a
// symbolic ref at one of the version's names is left and reported.
func Keep(ctx context.Context, repo *gitlocal.Repo, versions *store.Store, key store.Key, m store.Metadata) error {
	own, err := ownStore(repo.CommonDir(), versions.Root())
	if err != nil || !own {
		return err
	}
	want := refs(key, m.Version, m.Head, m.Base, m.MergeBase)
	if len(want) == 0 {
		return nil
	}
	prefix := gitlocal.RetainVersionPrefix(key.Repository, key.Number, m.Version)
	plan := func() ([]gitlocal.RefUpdate, gitlocal.Retained, error) {
		existing, err := repo.RetainedRefs(ctx, prefix)
		if err != nil {
			return nil, existing, err
		}
		updates, _, _, err := writes(ctx, repo, want, existing, len(want), len(want))
		return updates, existing, err
	}
	updates, existing, err := plan()
	if err != nil {
		return err
	}
	if beforeKeepWrite != nil {
		beforeKeepWrite()
	}
	if err := repo.UpdateRetained(ctx, updates); err != nil {
		again, now, planErr := plan()
		if planErr != nil || len(again) > 0 {
			return err
		}
		existing = now
	}
	var symbolic []string
	for name := range want {
		if existing.Symbolic[name] {
			symbolic = append(symbolic, name)
		}
	}
	if len(symbolic) > 0 {
		slices.Sort(symbolic)
		return fmt.Errorf("left symbolic refs at retained names: %s", strings.Join(symbolic, ", "))
	}
	return nil
}

// beforeKeepWrite runs between Keep's plan and its write; tests set it.
var beforeKeepWrite func()

// newestFirst orders retained ref names by version, newest first, then by
// name, so commits an old version lost never hold up a new one's.
func newestFirst(names []string) {
	version := func(name string) int {
		parsed, _ := gitlocal.ParseRetainRef(name)
		return parsed.Version
	}
	slices.SortFunc(names, func(a, b string) int {
		if va, vb := version(a), version(b); va != vb {
			return vb - va
		}
		return strings.Compare(a, b)
	})
}

// writes plans the creation or move of the refs in want that existing lacks
// as plain refs or has at another commit, newest version first: commits are
// looked up check at a time, up to checkBatches batches, and at most limit
// refs are written. A symbolic ref at a wanted name is skipped. more reports
// refs the write limit left unchecked, or the lookup bound cut off after
// some were written (one cut off with nothing written would cut off the same
// commits again).
func writes(ctx context.Context, repo *gitlocal.Repo, want map[string]string, existing gitlocal.Retained, check, limit int) (updates []gitlocal.RefUpdate, missing int, more bool, err error) {
	var names []string
	for name, oid := range want {
		if !existing.Symbolic[name] && existing.Refs[name] != oid {
			names = append(names, name)
		}
	}
	newestFirst(names)
	for start := 0; start < len(names); start += check {
		if len(updates) == limit {
			return updates, missing, true, nil
		}
		if start >= checkBatches*check {
			return updates, missing, len(updates) > 0, nil
		}
		batch := names[start:min(start+check, len(names))]
		var oids []string
		for _, name := range batch {
			oids = append(oids, want[name])
		}
		present, err := repo.HasCommits(ctx, oids)
		if err != nil {
			return nil, 0, false, err
		}
		for _, name := range batch {
			if !present[want[name]] {
				missing++
				continue
			}
			if len(updates) == limit {
				return updates, missing, true, nil
			}
			updates = append(updates, gitlocal.RefUpdate{Name: name, Old: existing.Refs[name], New: want[name]})
		}
	}
	return updates, missing, false, nil
}

// saved reads the refs every version in the clone's own stores should have.
// found is false when no root has a store at all; any store that exists but
// cannot be read is an error, and nothing may be deleted on its account.
func saved(ctx context.Context, commonDir string) (want map[string]string, found bool, err error) {
	roots, err := store.Roots(commonDir)
	if err != nil {
		return nil, false, err
	}
	want = map[string]string{}
	for _, root := range roots {
		s, err := store.Open(commonDir, root, false)
		if err != nil {
			return nil, false, err
		}
		commits, err := s.Commits(ctx)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		found = true
		for _, c := range commits {
			for name, oid := range refs(c.Key, c.Version, c.Head, c.Base, c.MergeBase) {
				want[name] = oid
			}
		}
	}
	return want, found, nil
}

// plan lists the retained refs, then reads the stores, and plans one pass's
// writes and deletes. Listing first means a version published meanwhile,
// whose refs are written after it is stored, is in the stores read whenever
// its refs are in the listing, so they are never taken for orphans.
func plan(ctx context.Context, repo *gitlocal.Repo, limits Limits) (updates, deletes []gitlocal.RefUpdate, result Result, err error) {
	existing, err := repo.RetainedRefs(ctx, gitlocal.RetainPrefix)
	if err != nil {
		return nil, nil, Result{}, err
	}
	want, found, err := saved(ctx, repo.CommonDir())
	if err != nil {
		return nil, nil, Result{}, err
	}
	updates, missing, more, err := writes(ctx, repo, want, existing, limits.Check, limits.Write)
	if err != nil {
		return nil, nil, Result{}, err
	}
	result = Result{Missing: missing, More: more, Symbolic: len(existing.Symbolic)}
	if !found {
		return updates, nil, result, nil
	}
	var orphans []string
	for name := range existing.Refs {
		if _, kept := want[name]; kept {
			continue
		}
		if _, ok := gitlocal.ParseRetainRef(name); ok {
			orphans = append(orphans, name)
		}
	}
	slices.Sort(orphans)
	if len(orphans) > limits.Delete {
		orphans, result.More = orphans[:limits.Delete], true
	}
	for _, name := range orphans {
		deletes = append(deletes, gitlocal.RefUpdate{Name: name, Old: existing.Refs[name]})
	}
	return updates, deletes, result, nil
}

// apply applies updates in one transaction, and when that fails, in halves,
// down to single refs, so one ref that cannot change holds up no other. It
// returns how many changed and the refs that could not change alone.
func apply(ctx context.Context, repo *gitlocal.Repo, updates []gitlocal.RefUpdate) (int, []string) {
	if len(updates) == 0 {
		return 0, nil
	}
	if repo.UpdateRetained(ctx, updates) == nil {
		return len(updates), nil
	}
	if len(updates) == 1 || ctx.Err() != nil {
		var failed []string
		for _, u := range updates {
			failed = append(failed, u.Name)
		}
		return 0, failed
	}
	mid := len(updates) / 2
	left, leftFailed := apply(ctx, repo, updates[:mid])
	right, rightFailed := apply(ctx, repo, updates[mid:])
	return left + right, append(leftFailed, rightFailed...)
}

// Reconcile brings the clone's retained refs in line with the versions in
// its own stores (store.Roots), within limits: it creates the refs of
// versions that lack them, for commits the clone holds, and deletes the
// plain refs, under gitlocal.RetainPrefix only and only names ParseRetainRef
// reads, of versions no store holds. A symbolic ref is never written or
// deleted. A store that exists but cannot be read fails the pass before
// anything changes; when no store exists at all, nothing is deleted, since a
// store gone missing cannot be told from one moved. When a transaction
// fails (another writer, a name that clashes with another ref), the pass is
// planned once more from what is there now and applied in ever smaller
// transactions; the refs that cannot change even alone are Result.Failed.
func Reconcile(ctx context.Context, repo *gitlocal.Repo, limits Limits) (Result, error) {
	updates, deletes, result, err := plan(ctx, repo, limits)
	if err != nil {
		return Result{}, err
	}
	writeErr, deleteErr := repo.UpdateRetained(ctx, updates), repo.UpdateRetained(ctx, deletes)
	if writeErr == nil {
		result.Written = len(updates)
	}
	if deleteErr == nil {
		result.Deleted = len(deletes)
	}
	if writeErr == nil && deleteErr == nil {
		return result, nil
	}
	written, deleted := result.Written, result.Deleted
	if updates, deletes, result, err = plan(ctx, repo, limits); err != nil {
		return Result{Written: written, Deleted: deleted}, err
	}
	n, failed := apply(ctx, repo, updates)
	result.Written = written + n
	result.Failed = failed
	n, failed = apply(ctx, repo, deletes)
	result.Deleted = deleted + n
	result.Failed = append(result.Failed, failed...)
	return result, nil
}

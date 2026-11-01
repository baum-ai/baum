// Package reviewrefs keeps the commits saved review versions record (head,
// base and merge base) in the clone for as long as the versions exist, so a
// git gc in the clone cannot prune what a version's lazy patches are read
// from. Each version gets refs under gitlocal.RetainPrefix, written right
// after the version is stored (Keep) and reconciled with the clone's stores
// by the daemon (Reconcile): refs the clone's versions lack are created,
// and refs of versions no store of the clone holds are deleted. Both run
// outside the store's transaction, so a failure never undoes a version; a
// commit the clone does not hold is skipped, never fetched.
package reviewrefs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
)

// Limits bound one Reconcile pass.
type Limits struct {
	// Check bounds the commits looked up for refs to write.
	Check int
	// Write bounds the refs created or moved; Delete the refs deleted.
	Write, Delete int
}

// DefaultLimits are the daemon's: a pass checks at most 3000 commits and
// writes or deletes at most 600 refs (200 versions) each.
var DefaultLimits = Limits{Check: 3000, Write: 600, Delete: 600}

// Result is what one Reconcile pass did.
type Result struct {
	Written, Deleted int
	// Missing counts refs not written because the clone lacks the commit.
	Missing int
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
// skipped. Nothing is written for a store that is not the clone's own.
func Keep(ctx context.Context, repo *gitlocal.Repo, versions *store.Store, key store.Key, m store.Metadata) error {
	own, err := ownStore(repo.CommonDir(), versions.Root())
	if err != nil || !own {
		return err
	}
	want := refs(key, m.Version, m.Head, m.Base, m.MergeBase)
	if len(want) == 0 {
		return nil
	}
	existing, err := repo.RetainedRefs(ctx, gitlocal.RetainVersionPrefix(key.Repository, key.Number, m.Version))
	if err != nil {
		return err
	}
	updates, _, _, err := writes(ctx, repo, want, existing, len(want), len(want))
	if err != nil {
		return err
	}
	return repo.UpdateRetained(ctx, updates)
}

// writes plans the creation or move of the refs in want that existing lacks
// or has at another commit, looking up at most check commits and writing at
// most limit refs, in name order. more reports a ref the limit left.
func writes(ctx context.Context, repo *gitlocal.Repo, want, existing map[string]string, check, limit int) (updates []gitlocal.RefUpdate, missing int, more bool, err error) {
	var names, oids []string
	for name, oid := range want {
		if existing[name] != oid {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	names = names[:min(len(names), check)]
	for _, name := range names {
		oids = append(oids, want[name])
	}
	present, err := repo.HasCommits(ctx, oids)
	if err != nil {
		return nil, 0, false, err
	}
	for _, name := range names {
		if !present[want[name]] {
			missing++
			continue
		}
		if len(updates) == limit {
			return updates, missing, true, nil
		}
		updates = append(updates, gitlocal.RefUpdate{Name: name, Old: existing[name], New: want[name]})
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

// Reconcile brings the clone's retained refs in line with the versions in
// its own stores (store.Roots), within limits: it creates the refs of
// versions that lack them, for commits the clone holds, and deletes the
// refs, under gitlocal.RetainPrefix only and only names ParseRetainRef
// reads, of versions no store holds. A store that exists but cannot be read
// fails the pass before anything changes; when no store exists at all,
// nothing is deleted, since a store gone missing cannot be told from one
// moved. The refs are listed before the stores are read, so a version
// published meanwhile, whose refs are written after it is stored, never has
// its refs deleted.
func Reconcile(ctx context.Context, repo *gitlocal.Repo, limits Limits) (Result, error) {
	existing, err := repo.RetainedRefs(ctx, gitlocal.RetainPrefix)
	if err != nil {
		return Result{}, err
	}
	want, found, err := saved(ctx, repo.CommonDir())
	if err != nil {
		return Result{}, err
	}
	updates, missing, more, err := writes(ctx, repo, want, existing, limits.Check, limits.Write)
	if err != nil {
		return Result{}, err
	}
	result := Result{Missing: missing, More: more}
	var deletes []gitlocal.RefUpdate
	if found {
		var orphans []string
		for name := range existing {
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
			deletes = append(deletes, gitlocal.RefUpdate{Name: name, Old: existing[name]})
		}
	}
	var errs []error
	if err := repo.UpdateRetained(ctx, updates); err != nil {
		errs = append(errs, err)
	} else {
		result.Written = len(updates)
	}
	if err := repo.UpdateRetained(ctx, deletes); err != nil {
		errs = append(errs, err)
	} else {
		result.Deleted = len(deletes)
	}
	return result, errors.Join(errs...)
}

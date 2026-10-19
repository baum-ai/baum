package entire

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// Touched maps each checkpoint id to when it was last written. For the
// branch that is the commit time of the newest commit that changed it, from
// one git log over trees alone: Entire writes a checkpoint, and rewrites it
// when its session goes on, in a commit of its own. For a checkpoint only a
// ref holds it is the commit time of the ref's commit. An id in both stores
// keeps the branch's date, since a migrated ref's commit dates from the
// migration, not from when the session was written, and would make an old
// checkpoint look new. Refs that do not point at a commit are not dated. It
// is known without reading the checkpoint, so a caller that only wants recent
// checkpoints uses it to skip reading the rest. repoRoot may be a private
// repository that borrows the clone's objects.
func Touched(ctx context.Context, repoRoot string, store Store) (map[string]time.Time, error) {
	touched := map[string]time.Time{}
	if store.Branch != "" {
		if err := touchedBranch(ctx, repoRoot, store.Branch, touched); err != nil {
			return nil, err
		}
	}
	var ids []string
	for id, commit := range store.Refs {
		if _, dated := touched[id]; !dated && commitPattern.MatchString(commit) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return touched, nil
	}
	sort.Strings(ids)
	env := gitlocal.ReadEnv(repoRoot)
	// Only commits can be dated: keep what cat-file says is one.
	var input strings.Builder
	for _, id := range ids {
		input.WriteString(store.Refs[id] + "\n")
	}
	kinds, err := runGit(ctx, env, repoRoot, strings.NewReader(input.String()), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	types := strings.Split(strings.TrimSpace(string(kinds)), "\n")
	if len(types) != len(ids) {
		return touched, nil
	}
	input.Reset()
	commits := map[string]bool{}
	for i, id := range ids {
		if strings.TrimSpace(types[i]) == "commit" {
			commits[store.Refs[id]] = true
		}
	}
	if len(commits) == 0 {
		return touched, nil
	}
	for commit := range commits {
		input.WriteString(commit + "\n")
	}
	out, err := runGit(ctx, env, repoRoot, strings.NewReader(input.String()),
		"log", "--no-walk", "--no-color", "--no-show-signature", "--stdin", "--format=%H %ct")
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	dates := map[string]time.Time{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		commit, seconds, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		unix, err := strconv.ParseInt(strings.TrimSpace(seconds), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dating Entire checkpoints: bad commit time %q", seconds)
		}
		dates[commit] = time.Unix(unix, 0).UTC()
	}
	for _, id := range ids {
		if at, ok := dates[store.Refs[id]]; ok {
			touched[id] = at
		}
	}
	return touched, nil
}

func touchedBranch(ctx context.Context, repoRoot, commit string, touched map[string]time.Time) error {
	out, err := privateGit(ctx, repoRoot, "-c", "core.quotepath=false", "log", "--no-renames", "--no-color", "--no-show-signature",
		"--name-only", "--format=%x01%ct", commit)
	if err != nil {
		return fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	var at time.Time
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return fmt.Errorf("dating Entire checkpoints: bad commit time %q", rest)
			}
			at = time.Unix(seconds, 0).UTC()
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(line), "/", 3)
		if len(parts) < 3 {
			continue
		}
		id, ok := entirecheckpoint.CanonicalID(parts[0] + parts[1])
		if !ok || len(id) != branchIDLength {
			continue
		}
		// The log runs newest first, so the first sight of an id is its latest.
		if _, seen := touched[id]; !seen {
			touched[id] = at
		}
	}
	return nil
}

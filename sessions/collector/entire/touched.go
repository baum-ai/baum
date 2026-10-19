package entire

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// Touched maps each checkpoint id to when it was last written. For the
// branch that is the commit time of the newest commit that changed it, from
// one git log over trees alone: Entire writes a checkpoint, and rewrites it
// when its session goes on, in a commit of its own. For a ref it is the commit
// time of the ref's commit (a migrated ref dates from the migration, so it
// reads as newer than it was written, never older). It is known without
// reading the checkpoint, so a caller that only wants recent checkpoints uses
// it to skip reading the rest. repoRoot may be a private repository that
// borrows the clone's objects.
func Touched(ctx context.Context, repoRoot string, store Store) (map[string]time.Time, error) {
	touched := map[string]time.Time{}
	if store.Branch != "" {
		if err := touchedBranch(ctx, repoRoot, store.Branch, touched); err != nil {
			return nil, err
		}
	}
	if len(store.Refs) == 0 {
		return touched, nil
	}
	var input strings.Builder
	for _, commit := range store.Refs {
		input.WriteString(commit + "\n")
	}
	out, err := runGit(ctx, gitlocal.ReadEnv(repoRoot), repoRoot, strings.NewReader(input.String()),
		"log", "--no-walk", "--no-color", "--no-show-signature", "--stdin", "--format=%H %ct")
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	commits := map[string]time.Time{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		commit, seconds, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		unix, err := strconv.ParseInt(strings.TrimSpace(seconds), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dating Entire checkpoints: bad commit time %q", seconds)
		}
		commits[commit] = time.Unix(unix, 0).UTC()
	}
	for id, commit := range store.Refs {
		if at, ok := commits[commit]; ok {
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

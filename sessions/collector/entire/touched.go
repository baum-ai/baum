package entire

import (
	"context"
	"fmt"
	"slices"
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
// repository that borrows the clone's objects. A Listing answers the same and
// remembers it.
func Touched(ctx context.Context, repoRoot string, store Store) (map[string]time.Time, error) {
	return new(Listing).Touched(ctx, repoRoot, store)
}

// dateObjects finds what each of objects is and dates the commits among them
// by their commit time. An object git does not report, such as one a partial
// clone lacks, is left out.
func dateObjects(ctx context.Context, repoRoot string, objects []string) (map[string]objectDate, error) {
	objects = slices.Clone(objects)
	slices.Sort(objects)
	objects = slices.Compact(objects)
	env := gitlocal.ReadEnv(repoRoot)
	// Only commits can be dated: keep what cat-file says is one.
	var input strings.Builder
	for _, object := range objects {
		input.WriteString(object + "\n")
	}
	kinds, err := runGit(ctx, env, repoRoot, strings.NewReader(input.String()), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	found := map[string]objectDate{}
	types := strings.Split(strings.TrimSpace(string(kinds)), "\n")
	if len(types) != len(objects) {
		return found, nil
	}
	input.Reset()
	var commits int
	for i, object := range objects {
		switch kind := strings.TrimSpace(types[i]); kind {
		case "commit":
			commits++
			input.WriteString(object + "\n")
		case "tree", "blob", "tag":
			found[object] = objectDate{}
		}
	}
	if commits == 0 {
		return found, nil
	}
	out, err := runGit(ctx, env, repoRoot, strings.NewReader(input.String()),
		"log", "--no-walk", "--no-color", "--no-show-signature", "--stdin", "--format=%H %ct")
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		commit, seconds, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		unix, err := strconv.ParseInt(strings.TrimSpace(seconds), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dating Entire checkpoints: bad commit time %q", seconds)
		}
		found[commit] = objectDate{at: time.Unix(unix, 0).UTC(), commit: true}
	}
	return found, nil
}

// touchedBranch dates the checkpoints the commits in revisions (a commit, or
// a range old..new) changed, each by the newest commit that changed it.
func touchedBranch(ctx context.Context, repoRoot, revisions string, touched map[string]time.Time) error {
	out, err := privateGit(ctx, repoRoot, "-c", "core.quotepath=false", "log", "--no-renames", "--no-color", "--no-show-signature",
		"--name-only", "--format=%x01%ct", revisions)
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

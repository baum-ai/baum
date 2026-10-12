package entire

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Touched maps each checkpoint id to the commit time of the newest commit of
// the branch that changed it, from one git log over trees alone. Entire writes
// a checkpoint, and rewrites it when its session goes on, in a commit of its
// own, so this is when the checkpoint was last written, and it is known
// without reading the checkpoint. A caller that only wants recent
// checkpoints uses it to skip reading the rest. repoRoot may be a private
// repository that borrows the clone's objects.
func Touched(ctx context.Context, repoRoot, commit string) (map[string]time.Time, error) {
	out, err := privateGit(ctx, repoRoot, "-c", "core.quotepath=false", "log", "--no-renames", "--no-color", "--no-show-signature",
		"--name-only", "--format=%x01%ct", commit)
	if err != nil {
		return nil, fmt.Errorf("dating Entire checkpoints: %w", err)
	}
	touched := map[string]time.Time{}
	var at time.Time
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("dating Entire checkpoints: bad commit time %q", rest)
			}
			at = time.Unix(seconds, 0).UTC()
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(line), "/", 3)
		if len(parts) < 3 {
			continue
		}
		id := parts[0] + parts[1]
		if !checkpointIDPattern.MatchString(id) {
			continue
		}
		// The log runs newest first, so the first sight of an id is its latest.
		if _, seen := touched[id]; !seen {
			touched[id] = at
		}
	}
	return touched, nil
}

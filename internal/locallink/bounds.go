package locallink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/linkage"
)

// SessionWindow is how long before a pull request's first commit a session may
// have last been written and still be read. The matcher links a commit only to
// a session whose end, or last write when it recorded none, is within its
// timeframe tolerance (a day) of the commit, or that created the commit by id,
// and a session that created a commit was written to at or after it. A day of
// margin covers clock skew. Older sessions cannot match, and skipping them is
// what keeps a machine with years of sessions from reading all of them.
var SessionWindow = linkage.DefaultConfig().TimeframeTolerance() + 24*time.Hour

// sessionsSince is the oldest modification time worth loading for commits, and
// false when a commit's date cannot be read, in which case everything loads.
func sessionsSince(commits []gitlocal.Commit) (time.Time, bool) {
	var earliest time.Time
	for _, c := range commits {
		at, err := time.Parse(time.RFC3339, c.CommittedAt)
		if err != nil {
			return time.Time{}, false
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	if earliest.IsZero() {
		return time.Time{}, false
	}
	return earliest.Add(-SessionWindow), true
}

// resolveBounds expands the request's head and range start to full commit ids
// the clone holds. The range starts at the merge base when the clone has it
// and at the base otherwise. A non-nil Result says why the pull request cannot
// be read here.
func resolveBounds(ctx context.Context, repo *gitlocal.Repo, req Request) (head, start string, unavailable *Result, err error) {
	fail := func(code, reason string) (string, string, *Result, error) {
		result := Unavailable(code, reason)
		return "", "", &result, nil
	}
	resolve := func(id string) (string, *Result, error) {
		oid, err := repo.ResolveCommit(ctx, id)
		switch {
		case err == nil:
			return oid, nil, nil
		case errors.Is(err, gitlocal.ErrInvalidInput):
			result := Unavailable(CodeNoHead, "the document's head and base are not commit ids")
			return "", &result, nil
		case errors.Is(err, gitlocal.ErrAmbiguous):
			result := Unavailable(CodeAmbiguousCommit, "the abbreviated commit id "+id+" matches more than one commit in this clone")
			return "", &result, nil
		case errors.Is(err, gitlocal.ErrObjectNotFound):
			return "", nil, err
		}
		return "", nil, fmt.Errorf("resolving commit %s: %w", id, err)
	}

	head, unavailable, err = resolve(req.Head)
	if unavailable != nil {
		return "", "", unavailable, nil
	}
	if errors.Is(err, gitlocal.ErrObjectNotFound) {
		return fail(CodeCommitsMissing, "this clone does not have the pull request's head commit; fetch it with git to see linked sessions")
	}
	if err != nil {
		return "", "", nil, err
	}
	for _, id := range []string{req.MergeBase, req.Base} {
		if id == "" {
			continue
		}
		start, unavailable, err = resolve(id)
		if unavailable != nil {
			return "", "", unavailable, nil
		}
		if errors.Is(err, gitlocal.ErrObjectNotFound) {
			continue
		}
		if err != nil {
			return "", "", nil, err
		}
		return head, start, nil, nil
	}
	return fail(CodeCommitsMissing, "this clone does not have the pull request's base commit; fetch it with git to see linked sessions")
}

// originReason says which repository the clone's origin names and which one
// the document is for, as owner/name only: a remote URL can carry credentials.
func originReason(origin, repository string) string {
	switch got, ok := NormalizeRepository(origin); {
	case origin == "":
		return "this clone has no origin remote, so it cannot be matched to " + repository
	case !ok:
		return "this clone's origin is not a repository URL that can be matched to " + repository
	default:
		return "this clone's origin is " + got + ", not " + repository + "; only the origin remote is checked"
	}
}

package locallink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/linkage"
	"github.com/lastpersonlabs/baum/sessions"
)

var checkpointIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

var errInvalidCheckpointTrailer = errors.New("invalid Entire-Checkpoint trailer")

// entireCheckpointID reads a commit's Entire-Checkpoint trailers the way the
// hosted resolver does: present is whether there is one at all, and err a
// value that is not a 12-hex checkpoint id or more than one distinct id. The
// key is matched without regard to case and the id is lower-cased.
func entireCheckpointID(trailers []string) (id string, present bool, err error) {
	var distinct []string
	for _, trailer := range trailers {
		key, value, found := strings.Cut(trailer, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "Entire-Checkpoint") {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if !checkpointIDPattern.MatchString(value) {
			return "", true, fmt.Errorf("%w: %q", errInvalidCheckpointTrailer, value)
		}
		if !slices.Contains(distinct, value) {
			distinct = append(distinct, value)
		}
	}
	switch len(distinct) {
	case 0:
		return "", false, nil
	case 1:
		return distinct[0], true, nil
	default:
		return "", true, fmt.Errorf("%w: multiple distinct checkpoint ids", errInvalidCheckpointTrailer)
	}
}

// checkpointLinks is the ground truth Entire left in the pull request's
// commits: for each commit whose Entire-Checkpoint trailer names a checkpoint
// on the clone's checkpoint branch, the sessions that checkpoint records.
//
// Every such session must be listed with the result, so one that loading did
// not return (it ran before the sessions read, or only the checkpoint records
// it) is mapped from the checkpoint and returned in extra. A session that
// cannot be mapped is left out, and a commit none of whose sessions are left
// has no ground truth. A malformed trailer, a checkpoint the clone does not
// hold, and a branch that cannot be read all leave the commit to the
// heuristic steps, as in the hosted resolver.
func checkpointLinks(ctx context.Context, dir string, commits []gitlocal.Commit, found []sessions.Session, warn io.Writer) (truth map[string][]string, extra []sessions.Session) {
	named := map[string]string{} // commit → checkpoint
	var ids []string
	for _, c := range commits {
		id, present, err := entireCheckpointID(c.Trailers)
		if !present || err != nil {
			continue
		}
		named[c.OID] = id
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	recorded, err := sessions.CheckpointSessions(ctx, dir, ids)
	if err != nil {
		warnf(warn, "warning: reading the Entire checkpoints the commits name: %v\n", err)
		return nil, nil
	}

	have := make(map[string]bool, len(found))
	for _, s := range found {
		have[s.ID] = true
	}
	for _, id := range ids {
		missing := slices.ContainsFunc(recorded[id], func(sessionID string) bool { return !have[sessionID] })
		if !missing {
			continue
		}
		mapped, failures, err := sessions.LoadCheckpoint(ctx, dir, id)
		if err != nil {
			warnf(warn, "warning: entire checkpoint %s: %v\n", id, err)
			continue
		}
		for _, failure := range failures {
			warnf(warn, "warning: %s session %s: %v\n", failure.Source, failure.Path, failure.Err)
		}
		for _, s := range mapped {
			if slices.Contains(recorded[id], s.ID) && !have[s.ID] {
				have[s.ID] = true
				extra = append(extra, s)
			}
		}
	}

	truth = map[string][]string{}
	for _, c := range commits {
		id, ok := named[c.OID]
		if !ok {
			continue
		}
		var linked []string
		for _, sessionID := range recorded[id] {
			if have[sessionID] {
				linked = append(linked, sessionID)
			}
		}
		if len(linked) > 0 {
			truth[c.OID] = linked
		}
	}
	return truth, extra
}

// entireEdges are a commit's links to the sessions its checkpoint records,
// exactly as the hosted resolver writes them: matched, certain, and by the
// entire_checkpoint method.
func entireEdges(commitSHA string, sessionIDs []string) []linkage.Edge {
	edges := make([]linkage.Edge, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		edges = append(edges, linkage.Edge{
			CommitSHA: commitSHA, RunID: sessionID, Status: linkage.StatusMatched,
			Method: linkage.MethodEntireCheckpoint, Confidence: 1,
			MatchedBy:           string(linkage.MethodEntireCheckpoint),
			CandidatesRemaining: len(sessionIDs),
		})
	}
	return edges
}

func warnf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}

package locallink

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/linkage"
	"github.com/baum-ai/baum/linkage/steps"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/collector/entire"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// checkpointLinks is the ground truth Entire left in the pull request's
// commits: for each commit whose Entire-Checkpoint trailers name checkpoints
// in the clone, on the per-checkpoint refs (refs/entire/checkpoints/*) or the
// checkpoint branch, the sessions those checkpoints record.
// Trailers are read as the hosted resolver reads them
// (entirecheckpoint.ParseTrailers): a commit naming several checkpoints links
// to the union of their sessions, in trailer order, each once, and a malformed
// value beside valid ones is skipped with a warning.
//
// Every such session must be listed with the result, so one that loading did
// not return (it ran before the sessions read, or only the checkpoint records
// it) is mapped from the checkpoint and returned in extra. A session that
// cannot be mapped is left out, and a commit none of whose sessions are left
// has no ground truth. A trailer with no valid id, a checkpoint the clone does
// not hold (warned about, with the command that fetches the refs: nothing is
// fetched here), and a store that cannot be read all leave the commit to the
// heuristic steps, as in the hosted resolver.
//
// The checkpoints are read whatever Request.Sessions.Sources says: a
// trailer is evidence in the commit, not a store the caller chose to read.
func checkpointLinks(ctx context.Context, dir string, commits []gitlocal.Commit, found []sessions.Session, warn io.Writer) (truth map[string][]string, extra []sessions.Session) {
	named := map[string][]string{} // commit → checkpoints, in trailer order
	firstNamedBy := map[string]string{}
	var ids []string
	for _, c := range commits {
		valid, malformed := entirecheckpoint.ParseTrailers(c.Trailers)
		if len(valid) == 0 {
			continue
		}
		for _, value := range malformed {
			warnf(warn, "warning: commit %s: skipping malformed Entire-Checkpoint value %q\n", c.OID, value)
		}
		named[c.OID] = valid
		for _, id := range valid {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
				firstNamedBy[id] = c.OID
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	branch, err := sessions.OpenCheckpoints(ctx, dir)
	if err != nil {
		warnf(warn, "warning: reading the Entire checkpoints the commits name: %v\n", err)
		return nil, nil
	}
	if branch == nil {
		warnMissing(warn, ids, nil, nil, nil, firstNamedBy)
		return nil, nil
	}
	defer branch.Close()
	recorded, err := branch.Sessions(ctx, ids)
	if err != nil {
		warnf(warn, "warning: reading the Entire checkpoints the commits name: %v\n", err)
		return nil, nil
	}

	located, checkErr := branch.Located(ctx, ids)
	warnMissing(warn, ids, recorded, located, checkErr, firstNamedBy)

	have := make(map[string]bool, len(found))
	for _, s := range found {
		have[s.ID] = true
	}
	for _, id := range ids {
		missing := slices.ContainsFunc(recorded[id], func(sessionID string) bool { return !have[sessionID] })
		if !missing {
			continue
		}
		mapped, failures, err := branch.Load(ctx, id)
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
		var linked []string
		for _, id := range named[c.OID] {
			for _, sessionID := range recorded[id] {
				if have[sessionID] && !slices.Contains(linked, sessionID) {
					linked = append(linked, sessionID)
				}
			}
		}
		if len(linked) > 0 {
			truth[c.OID] = linked
		}
	}
	return truth, extra
}

// warnMissing reports the checkpoints a trailer names that Sessions did not
// return. Those the clone holds but cannot read (metadata that is unreadable,
// too large or malformed, or objects a partial clone never downloaded) get a
// line each, with no fetch hint: fetching would not help. Those it does not
// hold are listed in one line with the command that downloads them: the refs
// for any id, and the legacy branch too when one is 12-hex, which only the
// branch may hold. Nothing here runs a command: local linking never fetches,
// and the commits are left to the heuristic steps.
func warnMissing(warn io.Writer, ids []string, recorded map[string][]string, located map[string]bool, checkErr error, firstNamedBy map[string]string) {
	var missing []string
	var unchecked []string
	legacy := false
	for _, id := range ids {
		if _, ok := recorded[id]; ok {
			continue
		}
		if checkErr != nil {
			unchecked = append(unchecked, id)
			continue
		}
		if located[id] {
			warnf(warn, "warning: commit %s: Entire checkpoint %s is in this clone but cannot be read (its metadata is unreadable or its objects are not downloaded), so the commit is linked without it\n",
				firstNamedBy[id], id)
			continue
		}
		missing = append(missing, id)
		legacy = legacy || len(id) == 12
	}
	if len(unchecked) > 0 {
		// Whether they are in the clone is unknown, so no fetch is suggested.
		warnf(warn, "warning: could not check whether Entire checkpoints %s are in this clone, so the commits naming them are linked without them: %v\n",
			strings.Join(unchecked, ", "), checkErr)
	}
	if len(missing) == 0 {
		return
	}
	hint := entire.FetchHint
	if legacy {
		hint += " or " + entire.BranchFetchHint
	}
	warnf(warn, "warning: Entire checkpoints not in this clone, so the commits naming them are linked without them: %s; fetch them with: %s\n",
		strings.Join(missing, ", "), hint)
}

// entireEdges are a commit's links to the sessions its checkpoints record,
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

// commitEdges links one commit as the hosted resolver does: the sessions its
// Entire checkpoints record first (linked; none for a commit without one),
// then, while the files the linked sessions edited do not cover the commit's,
// the heuristic matcher again over the other sessions, up to the link cap,
// and finally only the links at or above the review confidence once one is
// (a checkpoint's always are). Only matched edges are returned: the hosted
// resolver's unmatched edge and its reasons, and a trailer-only attribution,
// name no session and have nothing to show here.
func commitEdges(commit linkage.EnrichedCommit, linked []string, candidates []linkage.RunCandidate, cfg linkage.Config) ([]linkage.Edge, error) {
	edges := entireEdges(commit.OID, linked)
	byID := make(map[string]linkage.RunCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.RunID] = candidate
	}
	// The hosted groundTruthCoverage: the checkpoints' sessions' edits paired
	// against the commit as one set.
	var edited []string
	for _, id := range linked {
		edited = append(edited, byID[id].FilesEdited...)
	}
	explained := map[string]bool{}
	for _, path := range steps.ExplainedFiles(commit, linkage.RunCandidate{FilesEdited: edited}) {
		explained[path] = true
	}
	taken := make(map[string]bool, len(linked))
	for _, id := range linked {
		taken[id] = true
	}
	matched, err := matchRemaining(commit, candidates, byID, taken, explained, cfg)
	if err != nil {
		return nil, err
	}
	return dropSupersededGuesses(append(edges, matched...)), nil
}

// matchRemaining is the hosted resolver's: it runs linkage.MatchCommit over
// the sessions not yet linked until the commit's files are all explained, the
// per-commit link cap is reached, or nothing matches. Only matched edges are
// returned; a commit with ground truth never needs the unmatched one.
func matchRemaining(
	commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, byID map[string]linkage.RunCandidate,
	linked, explained map[string]bool, cfg linkage.Config,
) ([]linkage.Edge, error) {
	wanted := countDistinct(commit.FilesTouched)
	limit := cfg.FilesOverlapMaxLinksPerCommit
	links := 0
	var matched []linkage.Edge
	for {
		if limit > 0 && links >= limit {
			return matched, nil
		}
		if wanted > 0 && len(explained) >= wanted {
			return matched, nil
		}
		pool := make([]linkage.RunCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if !linked[candidate.RunID] {
				pool = append(pool, candidate)
			}
		}
		edges, err := linkage.MatchCommit(commit, pool, cfg, steps.Default())
		if err != nil {
			return nil, err
		}
		if len(edges) == 0 || edges[0].Status != linkage.StatusMatched {
			return matched, nil
		}
		if limit > 0 {
			edges = capTiedEdges(edges, limit-links)
		}
		for _, edge := range edges {
			linked[edge.RunID] = true
			if edge.Method == linkage.MethodFilesOverlap {
				links++
			}
			for _, path := range steps.ExplainedFiles(commit, byID[edge.RunID]) {
				explained[path] = true
			}
		}
		matched = append(matched, edges...)
	}
}

// capTiedEdges is the hosted resolver's: it keeps room edges of a tie and
// says the tie was capped.
func capTiedEdges(edges []linkage.Edge, room int) []linkage.Edge {
	if room >= len(edges) {
		return edges
	}
	edges = edges[:room]
	for i := range edges {
		if edges[i].Tie != nil && edges[i].Tie.Resolution == linkage.TieResolutionLinkedAll {
			edges[i].Tie.Resolution = linkage.TieResolutionLinkedCapped
		}
	}
	return edges
}

// dropSupersededGuesses is the hosted resolver's: once a commit has a matched
// edge at or above the review confidence, matched edges below it go.
func dropSupersededGuesses(edges []linkage.Edge) []linkage.Edge {
	settled := slices.ContainsFunc(edges, func(edge linkage.Edge) bool {
		return edge.Status == linkage.StatusMatched && edge.Confidence >= linkage.ReviewConfidence
	})
	if !settled {
		return edges
	}
	return slices.DeleteFunc(edges, func(edge linkage.Edge) bool {
		return edge.Status == linkage.StatusMatched && edge.Confidence < linkage.ReviewConfidence
	})
}

func countDistinct(paths []string) int {
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		seen[path] = true
	}
	return len(seen)
}

func warnf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}

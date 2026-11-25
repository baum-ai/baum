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

// checkpointLinks reads the candidate sessions named by each commit's checkpoints.
// Membership is an association, not proof that the session contributed. Missing
// local transcripts are loaded from the checkpoint when possible; an unreadable
// transcript does not erase the recorded membership. Nothing is fetched.
// Checkpoints are read even when Sessions.Sources excludes the Entire store,
// from the stores d found.
func checkpointLinks(ctx context.Context, d *sessions.Discovery, commits []gitlocal.Commit, found []sessions.Session, remote string, warn io.Writer) (associated map[string][]string, extra []sessions.Session) {
	named := map[string][]string{} // commit → checkpoints, in trailer order
	firstNamedBy := map[string]string{}
	var ids []string
	for _, c := range commits {
		valid, malformed := entirecheckpoint.ParseMessage(c.Message)
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
	branch, err := d.Checkpoints(ctx)
	if err != nil {
		warnf(warn, "warning: reading the Entire checkpoints the commits name: %v\n", err)
		return nil, nil
	}
	if branch == nil {
		warnMissing(warn, ids, nil, nil, nil, firstNamedBy, remote)
		return nil, nil
	}
	defer branch.Close()
	recorded, err := branch.Sessions(ctx, ids)
	if err != nil {
		warnf(warn, "warning: reading the Entire checkpoints the commits name: %v\n", err)
		return nil, nil
	}

	located, checkErr := branch.Located(ctx, ids)
	warnMissing(warn, ids, recorded, located, checkErr, firstNamedBy, remote)

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

	associated = map[string][]string{}
	for _, c := range commits {
		var linked []string
		for _, id := range named[c.OID] {
			for _, sessionID := range recorded[id] {
				if !slices.Contains(linked, sessionID) {
					linked = append(linked, sessionID)
				}
			}
		}
		if len(linked) > 0 {
			associated[c.OID] = linked
		}
	}
	return associated, extra
}

// warnMissing reports the checkpoints a trailer names that Sessions did not
// return. Those the clone holds but cannot read (metadata that is unreadable,
// too large or malformed, or objects a partial clone never downloaded) get a
// line each, with no fetch hint: fetching would not help. Those it does not
// hold are listed in one line with the command that downloads them: the refs
// for any id, and the legacy branch too when one is 12-hex, which only the
// branch may hold, from remote ("" names a placeholder). Nothing here runs a
// command: local linking never fetches, and the commits are left to the
// heuristic steps.
func warnMissing(warn io.Writer, ids []string, recorded map[string][]string, located map[string]bool, checkErr error, firstNamedBy map[string]string, remote string) {
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
	hint := entire.FetchHint(remote)
	if legacy {
		hint += " or " + entire.BranchFetchHint(remote)
	}
	warnf(warn, "warning: Entire checkpoints not in this clone, so the commits naming them are linked without them: %s; fetch them with: %s\n",
		strings.Join(missing, ", "), hint)
}

// commitEdges links every session that recorded creating the commit
// (commit_ref): a hook, reflog or transcript creation that the loaders put in
// its commit refs. The checkpoint reader adds candidate transcripts before
// this runs; membership alone links nothing.
func commitEdges(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) ([]linkage.Edge, error) {
	if cfg.ExcludeMergeCommits && commit.IsMerge {
		return nil, nil
	}
	// Editing the same files is not evidence: it linked colleagues' commits to
	// sessions here that edited those files too.
	var edges []linkage.Edge
	for _, candidate := range candidates {
		result := (steps.CommitRefStep{}).Apply(commit, []linkage.RunCandidate{candidate}, cfg)
		for _, edge := range result.Edges {
			if edge.Status == linkage.StatusMatched {
				edges = append(edges, edge)
			}
		}
	}
	return edges, nil
}

func warnf(w io.Writer, format string, args ...any) {
	if w != nil {
		_, _ = fmt.Fprintf(w, format, args...)
	}
}

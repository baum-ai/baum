package linkage

import "fmt"

// MatchCommit resolves one commit's edges against the full candidate set.
// Merge exclusion runs first; then cfg.EnabledMatchSteps run in order,
// selected by id from availableSteps, until one returns final edges or the
// chain runs out of steps. A finalizer may return several matched edges; an
// excluded or unmatched result always contains exactly one. This function
// never touches Postgres or git and does not persist the edges it returns.
func MatchCommit(commit EnrichedCommit, candidates []RunCandidate, cfg Config, availableSteps []Step) ([]Edge, error) {
	if cfg.ExcludeMergeCommits && commit.IsMerge {
		return []Edge{{
			CommitSHA: commit.OID,
			Status:    StatusExcludedMerge,
			Reason:    ReasonMergeCommit,
		}}, nil
	}

	steps, err := selectSteps(cfg, availableSteps)
	if err != nil {
		return nil, err
	}

	current := candidates
	lastStepID := ""
	// Reason from the step that took a non-empty candidate set to empty: the
	// commit had runs to consider and that step ruled all of them out, which
	// is more useful than a bare "no candidates".
	emptiedReason := ""
	for _, step := range steps {
		result := step.Apply(commit, current, cfg)
		if result.IsFinal() {
			return result.Edges, nil
		}
		if len(current) > 0 && len(result.Candidates) == 0 && result.Reason != "" {
			emptiedReason = result.Reason
		}
		current = result.Candidates
		lastStepID = step.ID()
	}

	reason := ReasonNoCandidates
	switch {
	case len(current) > 0 && lastStepID == string(MethodFilesOverlap):
		reason = ReasonNoFileOverlap
	case len(current) > 0:
		reason = ReasonNoStepFinalized
	case emptiedReason != "":
		reason = emptiedReason
	}
	return []Edge{{
		CommitSHA:           commit.OID,
		Status:              StatusUnmatched,
		CandidatesRemaining: len(current),
		Reason:              reason,
	}}, nil
}

// selectSteps resolves cfg.EnabledMatchSteps into a concrete, ordered Step
// slice from availableSteps (ablation support), rejecting duplicate ids in
// either list and any enabled id with no matching available step.
func selectSteps(cfg Config, availableSteps []Step) ([]Step, error) {
	byID := make(map[string]Step, len(availableSteps))
	for _, step := range availableSteps {
		id := step.ID()
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("duplicate available match step id: %q", id)
		}
		byID[id] = step
	}

	selected := make([]Step, 0, len(cfg.EnabledMatchSteps))
	seen := make(map[string]bool, len(cfg.EnabledMatchSteps))
	for _, id := range cfg.EnabledMatchSteps {
		if seen[id] {
			return nil, fmt.Errorf("duplicate enabled match step id: %q", id)
		}
		step, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown enabled match step id: %q", id)
		}
		selected = append(selected, step)
		seen[id] = true
	}
	return selected, nil
}

// MatchCommits resolves edges for every commit against the same candidate set,
// preserving commit order while flattening multi-edge matches.
func MatchCommits(commits []EnrichedCommit, candidates []RunCandidate, cfg Config, availableSteps []Step) ([]Edge, error) {
	var edges []Edge
	for _, commit := range commits {
		matched, err := MatchCommit(commit, candidates, cfg, availableSteps)
		if err != nil {
			return nil, fmt.Errorf("matching commit %s: %w", commit.OID, err)
		}
		edges = append(edges, matched...)
	}
	return edges, nil
}

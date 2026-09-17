package linkage

import "testing"

// OutcomeClass is the one place a reason becomes a finding, and every report and
// screen groups on what it returns. A reason silently landing in the wrong class
// is the failure the single classifier exists to prevent, so the whole mapping
// is pinned here rather than inferred from the reports downstream.
func TestOutcomeClassGroupsEveryReason(t *testing.T) {
	for reason, want := range map[string]string{
		ReasonAwaitingCheckpoint:   ClassAwaitingSession,
		ReasonAwaitingAgentSession: ClassAwaitingSession,
		ReasonAgentMismatch:        ClassAwaitingSession,

		ReasonNoFileOverlap:                  ClassNoEvidence,
		ReasonNoCandidates:                   ClassNoEvidence,
		ReasonNoStepFinalized:                ClassNoEvidence,
		ReasonOutsideTimeframe:               ClassNoEvidence,
		ReasonBothTimestampsMissing:          ClassNoEvidence,
		ReasonInvalidCommitTimestamp:         ClassNoEvidence,
		ReasonUserEmailMismatch:              ClassNoEvidence,
		ReasonBranchMismatch:                 ClassNoEvidence,
		ReasonEntireCheckpointTrailerInvalid: ClassNoEvidence,
		ReasonGitObjectUnavailable:           ClassNoEvidence,
		ReasonManuallyUnlinked:               ClassNoEvidence,

		// A matched edge carries no reason, and a merge is not an unattributed
		// commit at all.
		"":                ClassNotApplicable,
		ReasonMergeCommit: ClassNotApplicable,

		// An unknown reason must read as "nothing here says an agent wrote it".
		// Falling the other way would inflate the share the product is about.
		"a_reason_from_the_future": ClassNoEvidence,
	} {
		if got := OutcomeClass(reason); got != want {
			t.Errorf("OutcomeClass(%q) = %q, want %q", reason, got, want)
		}
	}
}

// The list a query is handed has to be exactly the reasons the classifier calls
// awaiting, or SQL and Go would disagree about the same commit.
func TestAwaitingSessionReasonsMatchTheClassifier(t *testing.T) {
	reasons := AwaitingSessionReasons()
	if len(reasons) == 0 {
		t.Fatal("AwaitingSessionReasons() is empty")
	}
	for _, reason := range reasons {
		if got := OutcomeClass(reason); got != ClassAwaitingSession {
			t.Errorf("OutcomeClass(%q) = %q, but it is in AwaitingSessionReasons()", reason, got)
		}
	}
	// A caller must not be able to shrink the set the next call returns.
	reasons[0] = "mutated"
	if AwaitingSessionReasons()[0] == "mutated" {
		t.Error("AwaitingSessionReasons() handed out its own backing array")
	}
}

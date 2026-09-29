package steps

import (
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/linkage"
)

func mustParse(t *testing.T, value string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return tm
}

func timePtr(t *testing.T, value string) *time.Time {
	t.Helper()
	tm := mustParse(t, value)
	return &tm
}

func TestRunInTimeframe(t *testing.T) {
	tests := []struct {
		name       string
		startedAt  *string
		endedAt    *string
		ingestedAt *string
		committed  string
		toleranceS int
		want       bool
	}{
		{"inside window", ptr("2024-06-01T11:00:00Z"), ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T11:30:00Z", 600, true},
		{"after ended but within tolerance", ptr("2024-06-01T11:00:00Z"), ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T12:05:00Z", 600, true},
		{"after ended plus tolerance", ptr("2024-06-01T11:00:00Z"), ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T12:15:00Z", 600, false},
		{"before started", ptr("2024-06-01T11:00:00Z"), ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T10:59:00Z", 600, false},
		{"both missing", nil, nil, nil, "2024-06-01T12:00:00Z", 600, false},
		{"both missing with an upload time", nil, nil, ptr("2024-06-01T12:00:00Z"), "2024-06-01T12:00:00Z", 600, false},
		{"open ended, start only", ptr("2024-06-01T11:00:00Z"), nil, nil, "2024-06-01T15:00:00Z", 600, true},
		{"no end, bounded by the upload", ptr("2024-06-01T11:00:00Z"), nil, ptr("2024-06-01T12:00:00Z"), "2024-06-01T12:05:00Z", 600, true},
		{"no end, past the upload plus tolerance", ptr("2024-06-01T11:00:00Z"), nil, ptr("2024-06-01T12:00:00Z"), "2024-06-01T12:20:00Z", 600, false},
		{"recorded end outranks a later upload", ptr("2024-06-01T11:00:00Z"), ptr("2024-06-01T12:00:00Z"), ptr("2024-06-03T12:00:00Z"), "2024-06-01T12:20:00Z", 600, false},
		{"end only, within tolerance", nil, ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T12:05:00Z", 600, true},
		{"end only, past tolerance", nil, ptr("2024-06-01T12:00:00Z"), nil, "2024-06-01T12:20:00Z", 600, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := linkage.RunCandidate{}
			if tt.startedAt != nil {
				candidate.StartedAt = timePtr(t, *tt.startedAt)
			}
			if tt.endedAt != nil {
				candidate.EndedAt = timePtr(t, *tt.endedAt)
			}
			if tt.ingestedAt != nil {
				candidate.IngestedAt = timePtr(t, *tt.ingestedAt)
			}
			committed := mustParse(t, tt.committed)
			tolerance := time.Duration(tt.toleranceS) * time.Second
			if got := runInTimeframe(candidate, committed, tolerance); got != tt.want {
				t.Errorf("runInTimeframe() = %v, want %v", got, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestTimeframeStepFiltersCandidates(t *testing.T) {
	step := TimeframeStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{CommittedAt: "2024-06-01T12:00:00Z"}}
	keep := linkage.RunCandidate{RunID: "keep", StartedAt: timePtr(t, "2024-06-01T11:50:00Z"), EndedAt: timePtr(t, "2024-06-01T12:05:00Z")}
	dropEarly := linkage.RunCandidate{RunID: "early", StartedAt: timePtr(t, "2024-06-01T13:00:00Z"), EndedAt: timePtr(t, "2024-06-01T14:00:00Z")}
	dropMissing := linkage.RunCandidate{RunID: "missing"}

	cfg := linkage.DefaultConfig()
	cfg.Timing.Tolerance = 10 * time.Minute
	result := step.Apply(commit, []linkage.RunCandidate{keep, dropEarly, dropMissing}, cfg)

	if result.IsFinal() {
		t.Fatal("expected a filtered result, got a final edge")
	}
	if len(result.Candidates) != 1 || result.Candidates[0].RunID != "keep" {
		t.Fatalf("candidates = %+v, want only %q", result.Candidates, "keep")
	}
}

func TestTimeframeStepDefaultAllowsCommitWithinOneHourAfterSession(t *testing.T) {
	step := TimeframeStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{CommittedAt: "2024-06-01T12:59:00Z"}}
	candidate := linkage.RunCandidate{
		RunID:     "within-one-hour",
		StartedAt: timePtr(t, "2024-06-01T11:00:00Z"),
		EndedAt:   timePtr(t, "2024-06-01T12:00:00Z"),
	}

	result := step.Apply(commit, []linkage.RunCandidate{candidate}, linkage.DefaultConfig())
	if len(result.Candidates) != 1 || result.Candidates[0].RunID != candidate.RunID {
		t.Fatalf("candidates = %+v, want %q", result.Candidates, candidate.RunID)
	}
}

// A commit timestamp nobody can parse is a bad commit record, not a verdict on
// when any run ran, so it gets its own reason code rather than surfacing as the
// generic "no candidates".
func TestTimeframeReportsUnparseableCommitTimestamp(t *testing.T) {
	step := TimeframeStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{CommittedAt: "not-a-timestamp"}}
	candidates := []linkage.RunCandidate{{RunID: "a", StartedAt: timePtr(t, "2024-06-01T11:00:00Z")}}

	result := step.Apply(commit, candidates, linkage.DefaultConfig())
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none (fail closed)", result.Candidates)
	}
	if result.Reason != linkage.ReasonInvalidCommitTimestamp {
		t.Fatalf("reason = %q, want %q", result.Reason, linkage.ReasonInvalidCommitTimestamp)
	}
}

func TestTimeframeDistinguishesMissingTimestampsFromWrongWindow(t *testing.T) {
	step := TimeframeStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{CommittedAt: "2024-06-01T12:00:00Z"}}

	untimed := step.Apply(commit, []linkage.RunCandidate{{RunID: "a"}}, linkage.DefaultConfig())
	if untimed.Reason != linkage.ReasonBothTimestampsMissing {
		t.Errorf("reason for a run with no timestamps = %q, want %q", untimed.Reason, linkage.ReasonBothTimestampsMissing)
	}

	elsewhere := step.Apply(commit, []linkage.RunCandidate{
		{RunID: "a", StartedAt: timePtr(t, "2024-06-02T11:00:00Z")},
	}, linkage.DefaultConfig())
	if elsewhere.Reason != linkage.ReasonOutsideTimeframe {
		t.Errorf("reason for a run outside the window = %q, want %q", elsewhere.Reason, linkage.ReasonOutsideTimeframe)
	}
}

package steps

import (
	"testing"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

func TestUserEmailMatchesAuthor(t *testing.T) {
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{AuthorEmail: "ada@example.com"}}
	tests := []struct {
		userEmail string
		want      bool
	}{
		{"", true},
		{"ada@example.com", true},
		{"ADA@EXAMPLE.COM", true},
		{" ada@example.com ", true},
		{"ada", false},
		{"Ada Lovelace", false},
		{"bob@example.com", false},
	}
	for _, tt := range tests {
		candidate := linkage.RunCandidate{UserEmail: tt.userEmail}
		if got := userEmailMatchesAuthor(candidate, commit); got != tt.want {
			t.Errorf("userEmailMatchesAuthor(email=%q) = %v, want %v", tt.userEmail, got, tt.want)
		}
	}
}

func TestUserEmailStepExcludesMismatches(t *testing.T) {
	step := UserEmailStep{}
	commit := linkage.EnrichedCommit{Commit: linkage.Commit{AuthorEmail: "ada@example.com"}}
	keepEmail := linkage.RunCandidate{RunID: "a", UserEmail: "ada@example.com"}
	keepMissing := linkage.RunCandidate{RunID: "b"}
	drop := linkage.RunCandidate{RunID: "c", UserEmail: "other@x.com"}

	result := step.Apply(commit, []linkage.RunCandidate{keepEmail, keepMissing, drop}, linkage.DefaultConfig())
	if len(result.Candidates) != 2 || result.Candidates[0].RunID != "a" || result.Candidates[1].RunID != "b" {
		t.Fatalf("candidates = %+v, want [a, b]", result.Candidates)
	}
}

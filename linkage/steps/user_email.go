package steps

import (
	"strings"

	"github.com/lastpersonlabs/reviewpal/linkage"
)

const UserEmailStepID = "user_email"

// UserEmailStep excludes candidates with a present UserEmail that disagrees
// with the commit author's email, after trimming and case-folding. A
// missing candidate email never excludes. Entire ground-truth attribution
// emails are out of scope here — never used for matching.
type UserEmailStep struct{}

func (UserEmailStep) ID() string { return UserEmailStepID }

func (UserEmailStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	kept := make([]linkage.RunCandidate, 0, len(candidates))
	for _, c := range candidates {
		if userEmailMatchesAuthor(c, commit) {
			kept = append(kept, c)
		}
	}
	return linkage.FilteredWithReason(kept, linkage.ReasonUserEmailMismatch)
}

func userEmailMatchesAuthor(candidate linkage.RunCandidate, commit linkage.EnrichedCommit) bool {
	if strings.TrimSpace(candidate.UserEmail) == "" {
		return true
	}
	return normalizeEmail(candidate.UserEmail) == normalizeEmail(commit.AuthorEmail)
}

func normalizeEmail(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

package prompt

import _ "embed"

// TriageVersion is the built-in version of the prompt that decides whether a
// push to a reviewed pull request needs a new review (IDReviewTriage).
const TriageVersion = "review-triage-public-v1"

//go:embed review-triage-public-v1.md
var triagePublic string

// Triage is the system prompt of the call that decides whether new commits
// need a new review, with the override for IDReviewTriage when it has one,
// and the version it is: TriageVersion, with CustomSuffix for an override.
func Triage(o Overrides) (string, string) {
	text, custom := o.text(IDReviewTriage, triagePublic)
	return text, withCustom(TriageVersion, custom)
}

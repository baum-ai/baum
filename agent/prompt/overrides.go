package prompt

import "strings"

// ID names one prompt a host can replace.
type ID string

// The prompts a host can replace.
const (
	IDReview              ID = "review"
	IDReviewGroup         ID = "review-group"
	IDReviewStagedShared  ID = "review-staged-shared"
	IDReviewStagedOutline ID = "review-staged-outline"
	IDReviewStagedDetails ID = "review-staged-details"
	IDReviewStagedContext ID = "review-staged-context"
	IDFix                 ID = "fix"
	IDFixConflicts        ID = "fix-conflicts"
	IDFixChecks           ID = "fix-checks"
	IDFixThreads          ID = "fix-threads"
	IDFixChat             ID = "fix-chat"
	IDExtraction          ID = "extraction"
)

// Overrides maps a prompt to the text that replaces its built-in text. A nil
// map, a missing ID and an empty text all mean the built-in text.
type Overrides map[ID]string

// CustomSuffix is appended to a built-in version when the composition used an
// override.
const CustomSuffix = "+custom"

func withCustom(version string, custom bool) string {
	if custom {
		return version + CustomSuffix
	}
	return version
}

// The built-in texts without their trailing Submitting heading, which stays
// baum's: an override replaces only what comes before it.
const stagedSubmitting = "## Submitting\n"

var (
	reviewBody        = strings.TrimSuffix(reviewPublic, submitting)
	stagedOutlineBody = strings.TrimSuffix(stagedOutline, stagedSubmitting)
	stagedDetailsBody = strings.TrimSuffix(stagedDetails, stagedSubmitting)
	stagedContextBody = strings.TrimSuffix(stagedContext, stagedSubmitting)
)

// text returns the override for id, else builtin, and whether it is an
// override. An override that does not end in a newline gets one, so what
// follows it starts on its own line; the built-in text is never changed.
func (o Overrides) text(id ID, builtin string) (string, bool) {
	text := o[id]
	if text == "" {
		return builtin, false
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text, true
}

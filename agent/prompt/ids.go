package prompt

// ReviewIDs are the prompts a review composes, by mode: a pull request group
// uses review-group; a staged review of one pull request the four
// review-staged prompts; a one-pass review none, as it is not replaceable. A
// group is never staged. An override of any other prompt does not touch the
// review.
func ReviewIDs(group, staged bool) []ID {
	switch {
	case group:
		return []ID{IDReviewGroup}
	case staged:
		return []ID{IDReviewStagedShared, IDReviewStagedOutline, IDReviewStagedDetails, IDReviewStagedContext}
	}
	return nil
}

package prompt

// ReviewIDs are the prompts a review composes, by mode: a pull request group
// uses review and review-group; a staged review of one pull request the four
// review-staged prompts; a one-pass review review. A group is never staged.
// An override of any other prompt does not touch the review.
func ReviewIDs(group, staged bool) []ID {
	switch {
	case group:
		return []ID{IDReview, IDReviewGroup}
	case staged:
		return []ID{IDReviewStagedShared, IDReviewStagedOutline, IDReviewStagedDetails, IDReviewStagedContext}
	}
	return []ID{IDReview}
}

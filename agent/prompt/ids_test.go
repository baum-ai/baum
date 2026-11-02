package prompt

import (
	"slices"
	"testing"
)

func TestReviewIDsByMode(t *testing.T) {
	for _, c := range []struct {
		group, staged bool
		want          []ID
	}{
		{false, false, []ID{IDReview}},
		{true, false, []ID{IDReview, IDReviewGroup}},
		{true, true, []ID{IDReview, IDReviewGroup}},
		{false, true, []ID{IDReviewStagedShared, IDReviewStagedOutline, IDReviewStagedDetails, IDReviewStagedContext}},
	} {
		if got := ReviewIDs(c.group, c.staged); !slices.Equal(got, c.want) {
			t.Errorf("ReviewIDs(%v, %v) = %v, want %v", c.group, c.staged, got, c.want)
		}
	}
}
